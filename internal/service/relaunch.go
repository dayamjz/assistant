package service

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Relaunching a task in place: PRD section 5's Failed to Dispatched edge,
// "relaunched with context", and section 11's "relaunch in that same copy
// with the same instructions plus a note".
//
// relaunchTask is the one function that relaunches, so the coordinator's
// task.relaunch method (#95 line J, coordinator.go), the start-time pass in
// reconcile.go and a retry answered on a relaunch hold (taskholds.go) go
// through the same claim and cannot both relaunch one task. A retry is not a
// second path: the answer is a record this call reads, so the bound gives
// way here only when the bound's hold for the latest attempt this call read
// says retry, and a platform with no sweep only when the no-sweep hold for
// that attempt does. It is
// anchored rather than idempotent by convention: the attempt it begins is
// store.BeginTaskAttempt anchored to the highest attempt this call read, so
// two relaunchers that read the same latest attempt begin one between them
// and the other is refused with ErrTaskMoved before it appends or launches
// anything (P6). Reading the log to learn whether a relaunch was already
// tried is the P8 failure and nothing here does it; the attempt history is
// the record, and it is anchored.
//
// # The order
//
// The call decides from a read of its own, so a caller's reading is never
// what it acts on. It resolves the task and applies reconcile.go's table,
// refusing with errRelaunchNotDue a task the table does not relaunch, which
// is what makes a coordinator's request on a live task a refusal rather
// than a second supervisor, and a task past relaunch_attempts whose hold is
// not answered retry a refusal too. A task with a hold naming it still open
// resolves Held from internal/taskstate's hold tier, which the table leaves
// alone, so a relaunch asked while the bound's or the no-sweep hold waits is
// refused naming that hold, and only its answer gives the task back to the
// rows that relaunch. It re-runs internal/tasks' floor over the same
// read, through tasks.HoldsBranch, the function Release runs it through,
// and refuses with *relaunchHeld when a live task now holds the branch:
// Failed is not terminal in the lifecycle, so a successor dispatched past
// the Failed blocker may be live on the branch, and a relaunch beside it
// would share its runs. It re-probes the latest attempt's session
// immediately before the claim, because the read's probe was taken after
// the records and a terminal can come back, and refuses with
// errRelaunchNotDead when the answer is no longer Dead. Then the sweep of the
// copy, read on sweep.go's terms, which refuses with *copyUnswept before
// anything is recorded when it ran and did not clear the copy. A sweep the
// platform cannot make, agents.ErrSweepUnsupported, proves nothing about the
// copy, so it is refused too (P6, #95 line O): the call registers the
// no-sweep hold for the attempt it read, on taskholds.go's terms, and
// refuses with *relaunchUnswept, recording nothing else, unless that hold is
// already answered retry, in which case it goes on and the note and the log
// say that nothing was proven and that the relaunch rests on that answer.
// Then the claim, the
// "relaunched" note anchored to the history the decision read, carrying what
// the sweep did, the assertion of the standing copy, a not-isolated refusal
// when the copy path was not the copy itself and so was not swept, the
// "dispatched" entry anchored to the
// note, and the acknowledgement of the wakes the relaunch superseded. The
// launch is the caller's, through launchTask over the dispatched value this
// returns, which asserts the copy once more before the process starts, on
// launch.go's terms.
//
// The claim precedes the assertion, as it does at dispatch, and that is a
// deliberate reading of the issue's order: a refusal recorded before a new
// attempt exists lands on an attempt whose terminal is dead, and the
// process tier then outranks the refusal, so the task would read Unknown
// from its dead terminal and be relaunched again at the next start. Recorded
// on the new attempt, which has no session, the refusal decides the state
// and the task reads Failed from it, which the table surfaces rather than
// relaunches.
//
// # The copy is reused, never cut
//
// The relaunch asserts the directory standing at the task's copy path
// through assertCopy, the assertion dispatch and launch share, and cuts
// nothing: a copy that no longer stands is refused with the copy-missing
// token rather than replaced by a fresh clone, because PRD section 11's
// relaunch is in that same copy and "never a fresh generic launch while the
// copy is unaccounted for". The primary is not read and no snapshot is
// written: the attempt row and the two events are the relaunch's record,
// and the copy's start snapshot stays what the dispatch that proved it
// recorded.
//
// # The note
//
// The worker is started with the task's instructions file, second check
// included, followed by relaunchNote: which attempt this is, that the
// previous terminal was found gone with the work not landed, and that the
// copy is as the previous worker left it. It is appended at launch and not
// written into the file, so the file stays the coordinator's text plus the
// second check and a later attempt does not read an earlier attempt's note.

// relaunchedKind is the note internal/taskstate gives a relaunch, appended
// before the "dispatched" entry on that package's ordering rule; Lookup
// checks the spelling is one that package knows.
const relaunchedKind = "relaunched"

// errRelaunchNotDue reports a relaunch asked of a task reconcile.go's table
// does not relaunch: its terminal is live or unobserved, or its state is one
// a dead terminal is the ordinary shape of, or the decision is a surface.
// Nothing is recorded.
var errRelaunchNotDue = errors.New("service: the task is not one reconciliation relaunches")

// errRelaunchUnprobed reports a relaunch asked of a Service with no
// Terminals, which Open never produces and this package's tests build by
// hand. A relaunch rests on a dead reading this service made itself, and a
// service that cannot ask has none; it refuses rather than relaunching on a
// reading somebody else reported.
var errRelaunchUnprobed = errors.New("service: no terminal provider is wired into this service, so a terminal cannot be read as dead")

// errRelaunchInFlight reports a relaunch asked of a task whose slot a
// dispatch, another relaunch or a stop (steer.go) in this service holds
// unsettled. Nothing is
// recorded; the claim in flight decides, and a relaunch asked again after
// it settles is decided from the records it left.
var errRelaunchInFlight = errors.New("service: a dispatch or a relaunch of the task is in flight in this service")

// errRelaunchNotDead reports a re-probe immediately before the claim that
// did not answer Dead: the terminal came back, or could not be observed
// this time. Nothing is recorded, and PRD section 11 leaves it alone.
var errRelaunchNotDead = errors.New("service: the terminal did not read dead immediately before the relaunch")

// relaunchHeld reports a relaunch the floor refused: a live task now holds
// the branch, so the relaunch waits on it, on Release's terms about what
// holds a branch.
type relaunchHeld struct {
	// Task is the task that was not relaunched.
	Task string
	// Holder names the task holding the branch, and State is its state.
	Holder string
	State  taskstate.State
}

// Error names both tasks.
func (e *relaunchHeld) Error() string {
	return fmt.Sprintf("service: task %s is not relaunched while task %s, which is %s, holds its branch",
		e.Task, e.Holder, e.State)
}

// relaunchUnswept reports a relaunch refused because the sweep of the copy
// answered agents.ErrSweepUnsupported and no person has answered retry on
// the hold for the attempt the relaunch read. Hold is that hold as
// registered, open unless it was already answered; nothing else is
// recorded, and the relaunch goes ahead only after a retry answer.
type relaunchUnswept struct {
	Task string
	Hold store.Hold
}

// Error names the task and the hold it waits on.
func (e *relaunchUnswept) Error() string {
	return fmt.Sprintf("service: task %s is not relaunched, because this platform has no process sweep to prove nothing "+
		"still runs in its copy; it holds for a person on %s, answered %q or %q", e.Task, e.Hold.Key, machine.AnswerRetry, machine.AnswerStop)
}

// relaunched is what a relaunch established: the launch to make, the
// session reference of the attempt it superseded, and what the sweep of the
// copy did, in sweep.go's words.
type relaunched struct {
	launch     dispatched
	superseded string
	swept      string
}

// relaunchTask relaunches one task in place, on the terms this file's
// opening comment states, and returns the launch to make. It takes the
// task's slot first, through claimRelaunch, so dispatchDue passes the task
// over and a second relaunch in this service while this one is in flight is
// refused; the store's anchor is what refuses a second relauncher on
// another path, in another service, or after this one settled. A slot this
// service already held, because it dispatched or relaunched the task's
// earlier attempt, is taken again, which is what lets a coordinator
// relaunch a terminal that died while this service was up. A relaunch
// that claimed its attempt returns with the slot still unsettled and
// launch.settle set, as dispatch.go's dispatchTask does, so the slot
// stays taken against a stop and the rest through the launch that
// follows; a refused relaunch settles it before returning.
//
// Once the attempt is claimed, the session handle this service still holds
// for the superseded attempt, if any, is released through the provider:
// its process is gone, which the re-probe just read, and the launch that
// follows replaces the handle, so a handle left here would be one Close
// never reaches. A handle that does not name the superseded reference is
// left alone, and a release that fails is logged; neither changes what was
// recorded.
func (s *Service) relaunchTask(ctx context.Context, id string) (relaunched, error) {
	return s.relaunchTaskWith(ctx, id, "")
}

// relaunchTaskWith is relaunchTask carrying the note a retry on a stop's
// hold gave, which steer.go's steeringNote places after the relaunch note;
// it is added only when the superseded attempt's stop hold is answered
// retry, the one relaunch a stop's note is for.
func (s *Service) relaunchTaskWith(ctx context.Context, id, stopNote string) (relaunched, error) {
	settle, err := s.claimRelaunch(id)
	if err != nil {
		return relaunched{}, err
	}
	out, err := s.relaunchClaimed(ctx, id, stopNote)
	if err != nil {
		settle(out.launch.proof)
		return out, err
	}
	out.launch.settle = settle
	s.releaseSuperseded(id, out.superseded)
	return out, nil
}

// releaseSuperseded closes and forgets the session this service holds for
// a task when it is the one a relaunch superseded, on relaunchTask's terms.
func (s *Service) releaseSuperseded(id, ref string) {
	s.mu.Lock()
	held, ok := s.launched[id]
	if ok && held.session != nil && held.host != nil && held.session.Reference().String() == ref {
		delete(s.launched, id)
	} else {
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	if err := held.host.Close(held.session); err != nil {
		s.log.Printf("the superseded session %s of task %s was not released: %v", ref, id, err)
	}
}

// relaunchClaimed is relaunchTask once the in-memory slot is taken.
func (s *Service) relaunchClaimed(ctx context.Context, id, stopNote string) (relaunched, error) {
	if s.terminals == nil {
		return relaunched{}, errRelaunchUnprobed
	}
	bound, cancel := context.WithTimeout(ctx, reconcileProbeBound)
	defer cancel()
	probe := s.boundedProbe(bound)
	fleet, err := s.reader(probe).All(ctx)
	if err != nil {
		return relaunched{}, err
	}
	var r taskstate.Resolved
	found := false
	for _, candidate := range fleet {
		if candidate.Record.ID == id {
			r, found = candidate, true
			break
		}
	}
	if !found {
		return relaunched{}, fmt.Errorf("service: task %s: %w", id, store.ErrNotFound)
	}
	attempts, err := s.store.TaskAttempts(ctx, id)
	if err != nil {
		return relaunched{}, err
	}
	answered, err := s.relaunchAnswersOf(ctx, id, attempts)
	if err != nil {
		return relaunched{}, err
	}
	decision := decideReconciliation(r.State, attempts, s.cfg.RelaunchAttempts, answered)
	if decision.action != reconcileRelaunch && r.State.Source == taskstate.SourceHold {
		return relaunched{}, fmt.Errorf("%w: task %s waits on an answer to hold %s, and nothing relaunches it while that hold is open",
			errRelaunchNotDue, id, r.State.Basis.Hold)
	}
	if decision.action != reconcileRelaunch {
		return relaunched{}, fmt.Errorf("%w: task %s resolves %s from %s with its terminal %s, which calls for %s",
			errRelaunchNotDue, id, r.State.State, r.State.Source, r.State.Basis.Process, decision.action)
	}
	if holder, held := tasks.HoldsBranch(r, fleet); held {
		return relaunched{}, &relaunchHeld{Task: id, Holder: holder.Record.ID, State: holder.State.State}
	}
	e, err := s.expectationOf(ctx, r.Record)
	if err != nil {
		return relaunched{}, err
	}

	// The re-probe, then the sweep, then the claim.
	latest := attempts[len(attempts)-1]
	ref, _ := latest.SessionRef.Get()
	if liveness := probe(ctx, ref); liveness != taskstate.Dead {
		return relaunched{}, fmt.Errorf("%w: task %s: it reads %s", errRelaunchNotDead, id, liveness)
	}
	swept, err := s.sweepStandingCopy(ctx, id, e.copyPath)
	if err != nil {
		return relaunched{}, err
	}
	reason := decision.reason
	if swept.words != "" {
		reason += "; " + swept.words
	}
	if swept.unsupported {
		// P6: nothing proves the copy unowned, so a person decides. Only a
		// retry on the hold for the attempt read lets this relaunch past,
		// and the anchor below spends it on one attempt.
		if answered.unswept != boundRetry {
			h, err := s.holdUnswept(ctx, id, latest.Sequence, ref, swept.words)
			if err != nil {
				return relaunched{}, fmt.Errorf("service: task %s is not relaunched with no process sweep, "+
					"and its hold could not be registered: %w", id, err)
			}
			return relaunched{}, &relaunchUnswept{Task: id, Hold: h}
		}
		reason += ", and its hold for a relaunch without one was answered " + machine.AnswerRetry
	}
	if s.betweenAttemptReadAndClaim != nil {
		s.betweenAttemptReadAndClaim()
	}
	attempt, err := s.store.BeginTaskAttempt(ctx, id, latest.Sequence)
	if err != nil {
		return relaunched{}, err
	}

	// The note, anchored to the history the decision read.
	if _, ok := taskstate.Lookup(relaunchedKind); !ok {
		return relaunched{}, fmt.Errorf("service: the %q kind is not one internal/taskstate knows", relaunchedKind)
	}
	note, err := s.store.AppendTaskEventAfter(ctx, id, r.State.Basis.EventSequence, relaunchedKind,
		fmt.Sprintf("attempt %d, after session %s was found gone: %s", attempt.Sequence, ref, reason))
	if err != nil {
		return relaunched{}, fmt.Errorf("service: recording the relaunch of task %s: %w", id, err)
	}

	// The standing copy, asserted and never cut.
	if _, err := os.Lstat(e.copyPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return relaunched{}, s.refuseIsolation(ctx, id, attempt.Sequence, note.Sequence, taskstate.RefusedCopyMissing, err)
		}
		return relaunched{}, fmt.Errorf("service: reading the copy of task %s at %s: %w", id, e.copyPath, err)
	}
	proof, err := s.assertCopy(ctx, id, attempt.Sequence, note.Sequence, e.copyPath, e.want)
	if err != nil {
		return relaunched{}, err
	}
	if swept.unswept != nil {
		return relaunched{}, s.refuseIsolation(ctx, id, attempt.Sequence, note.Sequence, taskstate.RefusedNotIsolated,
			fmt.Errorf("the copy passed the assertion and was not swept: %w", swept.unswept))
	}

	// The entry that moves the task, then the wakes this supersedes.
	moved, err := s.store.AppendTaskEventAfter(ctx, id, note.Sequence, store.DispatchedEventKind,
		fmt.Sprintf("relaunched in place as attempt %d", attempt.Sequence))
	if err != nil {
		return relaunched{}, fmt.Errorf("service: recording the dispatch of task %s: %w", id, err)
	}
	s.acknowledgeSuperseded(ctx, id, ref)
	told := relaunchNote(attempt.Sequence, reason)
	if answered.stopped == boundRetry {
		// A stopped attempt's relaunch carries what steer.go says it does,
		// read from the records the stop left; a read that fails is logged
		// and the relaunch, already recorded, goes on with the note alone.
		unacknowledged, err := s.unacknowledgedOf(ctx, id, latest.Sequence)
		if err != nil {
			s.log.Printf("the unacknowledged messages of task %s attempt %d could not be read for its relaunch: %v", id, latest.Sequence, err)
		}
		told += steeringNote(stopNote, unacknowledged)
	}
	return relaunched{
		launch: dispatched{
			id: id, proof: proof, attempt: attempt.Sequence, want: e.want, instructions: e.instructions,
			anchor: moved.Sequence, note: told,
		},
		superseded: ref,
		swept:      swept.words,
	}, nil
}

// acknowledgeSuperseded acknowledges, as the program, every queued wake that
// is about the session the relaunch replaced, on supersededBy's terms, and
// leaves every other row for the coordinator. It reads the queue with
// store.QueuedWakes, which takes nothing, because a drain would hand every
// row it returned out to the program instead of leaving it for the
// coordinator. It reports to the log: the
// relaunch has already been recorded, and a row left queued is a wake the
// coordinator reads about a session that is gone, which is the safe side.
func (s *Service) acknowledgeSuperseded(ctx context.Context, id, ref string) {
	queued, err := s.store.QueuedWakes(ctx, reconcileDrainLimit)
	if err != nil {
		s.log.Printf("the wakes superseded by relaunching task %s could not be read: %v", id, err)
		return
	}
	var ids []int64
	for _, w := range queued {
		if supersededBy(w, id, ref) {
			ids = append(ids, w.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	if err := s.store.AcknowledgeWakes(ctx, ids, store.AcknowledgedByProgram()); err != nil {
		s.log.Printf("the %d wake(s) superseded by relaunching task %s could not be acknowledged: %v", len(ids), id, err)
		return
	}
	s.log.Printf("relaunching task %s superseded %d queued wake(s) about session %s, acknowledged by the program", id, len(ids), ref)
}

// relaunchNote is PRD section 11's note about what already happened, in the
// words a worker is given after the instructions file.
func relaunchNote(attempt int64, reason string) string {
	return "\n\n## Relaunched by the program\n\n" +
		fmt.Sprintf("This is attempt %d of this task. The program found the previous worker's terminal gone with "+
			"the work not landed (%s), kept this copy exactly as that worker left it, and started you in it with "+
			"the same instructions. Read what is in the copy before continuing: its branch, its log and its "+
			"uncommitted changes are that worker's progress, and you continue from them rather than starting over.\n",
			attempt, reason)
}
