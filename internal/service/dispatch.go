package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Dispatching a task: the clone, the claim, the two snapshots, and the proof.
//
// PRD section 6 moves a task from Dispatched to Running only once isolation
// is proven, and ends it with the refusal otherwise. This file is the step
// between: for a task that resolves Dispatched it cuts the copy PRD section 5
// gives the task's shape, observes it, asserts it, and either holds the proof
// or records the refusal. Nothing in this file launches a worker. The proof
// it ends at is kept in this service's memory and nowhere else, and launch.go
// is what takes it: dispatchDue hands a proven dispatch straight to the
// launch, which asserts the copy again and starts the worker, so a dispatch
// that reaches proven is followed by a launch in the same pass. What the
// launch does and does not move is launch.go's to say.
//
// # The copy is a clone of the primary, with the gate as its one remote
//
// A delivery copy is vcs.Clone of the person's primary checkout, the working
// copy the repository record names, into home.TaskCopy, with exactly one
// remote: gate.RemoteName at this home's gate repository for the task's
// repository. The task branch is created in the clone at the primary's
// default-branch tip, which is read off the primary before the clone and is
// the base the task starts from. An investigation copy is the same clone with
// no remote at all, detached at that same tip, because it creates no branch
// and pushes nothing. The clone reads the primary and writes nothing in it,
// which is the stance ensureCopy takes toward a working copy and PRD
// principle P1's concern; what is read is recorded as the primary snapshot.
//
// vcs.Clone makes the copy under a remote name it then removes, and
// internal/vcs's own tests read the copy's configuration back to establish
// that only the named remote's URL survives, so the investigation copy's
// configuration is not scrubbed here: a key that could survive would be that
// package's to remove.
//
// The clone is a full clone with configuration of its own, so it is given
// gc.auto=0 and maintenance.auto=false there through
// vcs.DisableAutomaticMaintenance before it is shaped, and a clone that cannot
// be given them is removed like one that cannot be shaped. The primary's
// configuration is not written, and the dispatch test reads it back to show
// it is the same before and after. A copy cut before that write existed keeps
// git's defaults until it is cut again: nothing here goes back to one.
//
// # The claim is the attempt, and the dispatched entry is not it
//
// Two dispatchers of one task cannot both cut: the dispatch is claimed by
// store.BeginFirstTaskAttempt for a task with no attempt, and by
// store.BeginDispatchAttempt for a retry, which is store.BeginTaskAttempt's
// anchored claim with the kept work hold asked in the same write (the
// unheld BeginTaskAttempt is the relaunch's), each anchored to the highest
// attempt this dispatcher read, and the loser is refused with ErrTaskMoved before it
// reads the primary. Before a first claim nothing touches the path either:
// no attempt has cut a copy, so a dispatch sweeps nothing then (#562), and
// whatever it finds standing after it wins is refused rather than swept or
// used, below, but for the copy a carry on adopted for the task, which the
// winner sweeps after its claim and then uses. A retry sweeps before its claim, so that a sweep that does
// not clear records nothing; two retries anchored to the same attempt would
// both sweep before the anchor decided between them, and it is the slot and
// the home's lock below, not the anchor, that keep there from being two.
// The first claim records the task's
// machine, and it is always this one: this build reads no remote host, so it
// places every task here, and a retry stays where the first claim put it.
// Before a first claim the repository's trusted copy is read (unhonored.go's
// placeable), and a copy that does not allow this machine, or that cannot be
// read, leaves the task queued with nothing claimed. The "dispatched" entry intake or Release
// appended is the state fact the dispatch is decided from, and it is never
// the claim: it says the task is to be dispatched, not that anyone is.
//
// What the anchor refuses is a dispatcher that read the same latest attempt
// this one did. A dispatcher that read a later one is a retry by the record's
// lights, and nothing in the record tells an attempt that crashed from one
// still in flight: within one service the in-memory slot below keeps a retry
// from beginning behind a live dispatch, and across services the home's lock
// is what keeps there from being two. That is the residual gap, and it is
// the same one internal/runs names for its branch gate.
//
// A dispatch is of a task that was never launched. A task whose latest
// attempt names a terminal session is passed over and refused with
// errTaskLaunched, because what to do about a launched worker whose service
// restarted is PRD section 11's question and not this file's: reconcile.go
// reads the terminal, relaunches it in place when it is gone, and leaves it
// alone when the reading is ambiguous, which a second dispatch here would
// not.
//
// A retry after a crash is a new attempt of the same task, and it finds
// whatever the crashed one left at the path. A path already standing there
// when the retry reads its attempts is swept before the claim, on
// sweep.go's terms, because a crash between a
// worker's start and the record of its session leaves a process no record
// names, and the sweep finds it by the copy instead. Where the platform has
// no sweep, agents.ErrSweepUnsupported, nothing proves that process absent,
// so the retry is refused (P6, #95 line O as the owner extended it to
// dispatch): it registers a hold naming the task, keyed by the latest
// attempt it read, on taskholds.go's terms, and returns *dispatchUnswept
// before the claim, recording nothing else. Only a retry answer on that
// hold lets a dispatch of that attempt past the refusal, and the claim's
// anchor spends it on one attempt; a stop leaves the task where it is. The
// standing copy is then observed and asserted exactly as a fresh clone is,
// and is never removed or written into (P6; copyStanding takes the same
// stance toward a run's copy): a half-written directory fails the
// assertion, and that failure is recorded as the refusal with the path
// untouched. A path that stands after the claim and that this dispatch
// neither cut nor asked the sweep of, at a first claim or appearing after a
// retry's check, is asserted the same way and then refused as not isolated
// even when the assertion passes, because nothing proves it unowned, and it
// too is left as it stands. The one copy a first claim finds standing and
// owns is the one a carry on adopted for the task (keptcarryon.go): an
// entry answered carry-on naming the task records that directory's file
// identity, and adoptedCopyStands holds what stands to it, so a clone of
// the same history is not taken for it. The winner sweeps that copy after
// its claim, on sweep.go's terms, since the previous owner's processes may
// still be in it, and a sweep that does not clear leaves the attempt
// claimed with no worker, which the next dispatch retries over its standing
// copy as above; with no sweep on the platform it raises the no-sweep hold
// keyed by the attempt it claimed, which that retry reads. The one directory this file does remove is a
// clone this same call just made and could not shape - the task
// branch could not be created in it, or HEAD could not be detached - which
// holds no work and is the program's own; it is removed so that the next
// attempt cuts a fresh one rather than proving a clone standing on the wrong
// commit.
//
// # Records precede directories
//
// The order is PRD section 8's: the attempt row, then the primary snapshot
// row, then the clone, then the observation, and the copy's own snapshot only
// once the copy is proven. A task record names its copy's path from intake as
// a reservation, and the path home.TaskCopy composes is the one owner of it;
// a record whose reservation disagrees is refused before anything is claimed.
//
// # What the environment check is made of
//
// The assertion takes the environment a worker would be launched with, which
// agents.WorkerEnvironment computes from the same base and extras a
// WorkerRunner starts a worker under. This service passes os.Environ() and no
// extras, which is the base every runner the default catalog builds has;
// WorkerEnvironment's doc states the gap for a runner built with another.
// Because that builder strips the redirecting variables, the assertion's
// environment comparison cannot fail on this path, and nothing here claims it
// can. The environment half of P11 is therefore only as strong as every
// launch path building its environment through agents.WorkerEnvironment:
// launch.go's second assertion builds it the same way, and the worker is
// started by a WorkerRunner that builds it the same way again; no second
// strip list exists, and none should.
//
// # Kept work holds a new task's dispatch
//
// PRD section 6's "kept work is answered before new task work starts": while
// the task's project has an open kept work entry, a task whose worker launch
// was never claimed is not dispatched or launched. internal/store owns which
// entries hold which task (keptworkhold.go), and three places ask it: this
// file through KeptWorkHolding right after the task's resolution is read,
// before any sweep, clone or claim; inside the attempt claim
// (store.BeginFirstTaskAttempt, or store.BeginDispatchAttempt for a retry);
// and store.ClaimAttemptLaunch inside the launch claim
// launch.go makes as its last write before the worker's process starts. The
// last is what binds the hold to the launch itself: an entry recorded while
// the copy is cut or asserted is read by that claim, and the worker is not
// started. Each refusal is a *store.DispatchHeldError, and nothing is written
// for it. The one window left is between the launch claim's commit and the
// process start, where an entry recorded does not stop that launch. Only a
// recorded session, written after the process started, makes the task work
// under way for later reads of the hold; a launch whose process never
// started leaves the task held like a fresh one at its next dispatch. The task keeps resolving Dispatched from its history, because nothing
// here appends an entry for the wait: the wait is read off the kept work
// record every time (P8), which taskView composes into the task's answer as
// machine.Task.KeptWork.
//
// A held dispatch or launch gives its slot back, through releaseHeldSlot,
// rather than settling it, because the slot is otherwise kept for the
// service's lifetime and a held task has to dispatch as soon as its
// project's entries are answered or closed. What asks again is every call of
// dispatchDue: at open, after every release, after every answer that closes
// an entry (keptanswer.go, keptcarryon.go), and after a
// cleanup that removed a copy, which is the one way an entry closes without
// an answer (cleanup.go). A pass that runs while the slot is still taken
// passes the task over, so releaseHeldSlot reads the hold again after the
// slot is gone and asks for another pass when it no longer holds: an entry
// closed before that read is seen by it, and one closed after it is closed
// after the slot was free, so the pass its closure asks for finds the task.
// A read that fails asks for that pass too, since it cannot tell which.
//
// A dispatch that ends before it has read the hold, because the task's
// resolution or the hold could not be read (holdUnread), gives its slot
// back as well and asks for nothing: it claimed nothing, so the rule that
// keeps a slot after a failed claim has nothing to keep from repeating,
// and a slot kept here would pass the task over at the pass a closure asks
// for. It asks for no pass of its own because a read that keeps failing
// would then ask without end. What remains is the case where every read
// fails: an entry closed while the slot was taken, whose release's read
// and whose following pass's read both fail, is dispatched at the next
// pass anything asks for (a release, an answer, a removal, or the next
// open), and not before.
// A relaunch is not new work and claims through store.BeginTaskAttempt, so
// it is never held.
//
// # The second check
//
// tasks.Expect builds the shell line PRD principle P11 has a worker run
// before it creates a branch, from the same values the assertion compares
// against, and this appends it to the task's instructions file under a
// heading of its own once the copy is proven, when the file does not already
// carry the line. The instructions file is the one thing a worker is started
// with and the place intake wrote the coordinator's text, so it is the
// minimal place a line the worker is to obey can go. It is detection by an
// obedient worker and not prevention, which Expect's doc says.

// errTaskNotDispatched reports a dispatch asked of a task that does not
// resolve Dispatched from its history: it is queued, already running, ended,
// or decided by a higher tier. Nothing is recorded. It and the two below are
// unexported because their one consumer is dispatchDue's log: nothing outside
// this package asks for a dispatch yet, and a type nothing consumes answers
// to nobody.
var errTaskNotDispatched = errors.New("service: the task does not resolve dispatched, so there is nothing to dispatch")

// errTaskLaunched reports a dispatch asked of a task whose latest attempt
// already names a terminal session: it was launched, and whether that
// terminal is gone is reconciliation's question, answered by reconcile.go's
// table from a probe, never by dispatching again. A second dispatch would
// start a second worker in the copy beside one that may still be there,
// which is PRD section 11's two supervisors for one task. Nothing is
// recorded.
var errTaskLaunched = errors.New("service: the task's latest attempt already runs in a terminal session, so a restart is a relaunch question and not a dispatch")

// errTaskRecord reports a task record a dispatch cannot cut a copy from as
// recorded: no repository, a reserved copy path that is not home.TaskCopy, or
// no instructions file. Nothing is recorded; the record is the thing to fix.
var errTaskRecord = errors.New("service: the task's record cannot be dispatched as recorded")

// isolationRefused reports a dispatch whose copy failed PRD principle P11's
// assertion. The refusal has been recorded in the task's history as an
// "isolation-refused" event carrying Reason, which is the closed token
// internal/taskstate defines, and the task resolves Failed from it; what was
// found is in Cause, which went to the service log and not into the record.
// It unwraps to tasks.ErrNotIsolated, which is the class a caller tests.
type isolationRefused struct {
	// Task names the task.
	Task string
	// Attempt is the attempt the dispatch was claimed as.
	Attempt int64
	// Reason is the recorded token.
	Reason taskstate.IsolationRefusal
	// Event is the entry recorded.
	Event store.TaskEvent
	// Cause is the assertion's own refusal, or the observation's failure
	// when the path could not be observed as a repository at all.
	Cause error
}

// Error names the task, the token and the cause.
func (e *isolationRefused) Error() string {
	return fmt.Sprintf("service: task %s attempt %d was refused a worker (%s): %v", e.Task, e.Attempt, e.Reason, e.Cause)
}

// Unwrap makes every refused dispatch match tasks.ErrNotIsolated.
func (e *isolationRefused) Unwrap() error { return tasks.ErrNotIsolated }

// dispatchUnswept reports a dispatch retried over a copy already standing at
// the task's copy path, refused because the sweep of that copy answered
// agents.ErrSweepUnsupported and no person has answered retry on the hold
// for the attempt the dispatch read (P6). Hold is that hold as registered:
// open, or answered stop, in which case it stays refused for that attempt.
// Nothing else is recorded, and the dispatch goes ahead only after a retry
// answer, which the attempt anchor spends on one attempt.
type dispatchUnswept struct {
	Task string
	Hold store.Hold
}

// Error names the task and the hold it waits on, or the stop it was given.
func (e *dispatchUnswept) Error() string {
	if !e.Hold.Open() {
		return fmt.Sprintf("service: task %s is not dispatched again over its standing copy, because this platform has no "+
			"process sweep and its hold %s was answered %q", e.Task, e.Hold.Key, e.Hold.Resolution.Or(""))
	}
	return fmt.Sprintf("service: task %s is not dispatched again over its standing copy, because this platform has no "+
		"process sweep to prove nothing still runs in it; it holds for a person on %s, answered %q or %q",
		e.Task, e.Hold.Key, machine.AnswerRetry, machine.AnswerStop)
}

// secondCheckHeading is the heading the second check is appended to the
// instructions under. It is the marker that keeps the append idempotent
// across attempts, together with the line itself.
const secondCheckHeading = "## Isolation check, written by the program at dispatch"

// dispatchDue dispatches every task that resolves Dispatched and that this
// service has not already dispatched since it opened, and logs what came of
// each. It is called at open, after the queued tasks have been released, and
// after every release, which are the moments a task can have become
// Dispatched. Each dispatch runs on the caller's path, which for a release
// after a run settled is that run's segment; a clone of a local checkout is
// what that costs, and nothing here moves it to a goroutine of the service's.
//
// A task this service holds a slot for is passed over, whether that slot is a
// proof, a dispatch in flight, or a dispatch that ended without a proof, so
// one service begins at most one attempt of a task however it came out: a
// dispatch that failed after its claim, for a reason other than the
// assertion, is retried at the next open and not at the next release, which
// is what keeps a failure that repeats from adding an attempt per run that
// settles. A task refused resolves Failed from the entry the refusal appended
// and is kept out by the resolver as well as by the slot. The exceptions
// are dispatches that claimed nothing, whose slot is given back: a task
// placeable left queued, so the next pass reads its repository's trusted
// copy again, and one kept work held or whose hold could not be read, on
// this file's opening terms.
//
// The tasks are read under the read side of the intake exclusion
// createTask holds across an intake, so a pass never reads a task whose
// record is written and whose instructions file is not yet. A pass that
// finds an intake holding or taking that exclusion is skipped rather than waited on, so a release
// on a run's segment does not wait behind an intake's reads. Nothing is
// lost by the skip: createTask starts a pass once its intake has returned,
// refused or not, and that pass is either run or skipped for a later
// intake that starts one of its own; a service giving up its home starts
// none, and the next open dispatches. That covers this service's intakes
// only: a crash between the record and the file leaves a task with no
// instructions, which the pass refuses as errTaskRecord, internal/tasks'
// residual gap.
//
// A dispatch that reaches proven is launched in the same pass, through
// launch.go, and the slot covers the launch as well, in both senses: the
// slot stays taken for the service's lifetime, so a launch that failed is
// not retried until the next open either, because the attempt it would be
// launching was dispatch's and a second launch of one attempt would be a
// second process against one record; and it stays unsettled, which is what
// claimSteer and claimRelaunch refuse on, from the claim until the launch
// has settled the record of its session (dispatched.settle), a worker
// fallback at the launch included, which is handed the slot unsettled and
// settles it after the backup's launch (dispatched.handSlot), so nothing
// that takes the task's slot, a stop, a relaunch or a housekeeping pass's
// retirement, interleaves with a worker being created and not yet
// recorded, and the offer of a pass's retirement hold, which reads the
// slot through launching, is skipped meanwhile. What came of the launch is
// logged, and a refusal of the launch's own assertion is recorded the way
// dispatch's is.
func (s *Service) dispatchDue(ctx context.Context) {
	if !s.intake.TryRLock() {
		s.log.Printf("dispatching was left to the task intake in flight, which dispatches once it has returned")
		return
	}
	resolved, err := s.tasks().All(ctx)
	s.intake.RUnlock()
	if err != nil {
		s.log.Printf("dispatching: the tasks could not be read: %v", err)
		return
	}
	for _, r := range resolved {
		if r.State.State != taskstate.Dispatched || r.State.Source != taskstate.SourceEvent {
			continue
		}
		if _, held := s.isolationOf(r.Record.ID); held {
			continue
		}
		if r.Record.Housekeeping {
			// A pass retired before its dispatch is never begun: its claim
			// on its repository is released, and the store's claim would
			// refuse it anyway (store.ErrPassRetired).
			if _, retired, err := s.store.RetirementOf(ctx, r.Record.ID); err != nil || retired {
				if err != nil {
					s.log.Printf("dispatching: whether housekeeping pass %s is retired could not be read: %v", r.Record.ID, err)
				}
				continue
			}
		}
		// The slot is taken for the service's lifetime, so a task that is a
		// relaunch question is passed over before the slot rather than
		// refused behind it, where it would keep a relaunch out too.
		// dispatchClaimed makes the same refusal for a caller below this.
		attempts, err := s.store.TaskAttempts(ctx, r.Record.ID)
		if err != nil {
			s.log.Printf("dispatching: the attempts of task %s could not be read: %v", r.Record.ID, err)
			continue
		}
		if _, launched := latestSession(attempts); launched {
			s.log.Printf("task %s was not dispatched here: %v", r.Record.ID, errTaskLaunched)
			continue
		}
		d, err := s.dispatchTask(ctx, r.Record.ID)
		switch {
		case err == nil:
			s.log.Printf("task %s was dispatched: its copy at %s is proven isolated", r.Record.ID, d.proof.Copy)
		case errors.Is(err, store.ErrDispatchHeld):
			s.log.Printf("task %s waits: %v", r.Record.ID, err)
			continue
		case errors.Is(err, store.ErrTaskMoved):
			s.log.Printf("task %s was not dispatched here: %v", r.Record.ID, err)
			continue
		case errors.Is(err, errTaskUnplaced):
			s.log.Printf("task %s stays queued: %v", r.Record.ID, err)
			continue
		case errors.Is(err, store.ErrPassHeld):
			s.log.Printf("housekeeping pass %s was not dispatched, and is once its retirement hold is answered keep: %v", r.Record.ID, err)
			continue
		default:
			s.log.Printf("task %s was not dispatched: %v", r.Record.ID, err)
			continue
		}
		launched, err := s.launchTask(ctx, d)
		var refused *routeRefused
		switch {
		case err == nil:
			s.log.Printf("task %s attempt %d was launched: its worker runs in terminal session %s",
				d.id, d.attempt, launched.Reference())
		case errors.As(err, &refused):
			s.log.Printf("task %s attempt %d was dispatched and not launched; routing refused and the task failed: %v",
				d.id, d.attempt, err)
		default:
			s.log.Printf("task %s attempt %d was not launched: %v", d.id, d.attempt, err)
		}
	}
}

// dispatched is what a dispatch that reached proven established, and what
// the launch takes: the proof, the attempt the dispatch claimed, the
// expectation the copy was asserted against, the instructions file the
// worker is started with, and the history sequence the dispatch was decided
// from, which anchors a refusal the launch records.
type dispatched struct {
	id           string
	proof        tasks.Isolation
	attempt      int64
	want         tasks.Expectation
	instructions string
	anchor       int64
	// note is text the launch appends to the instructions after the file's
	// own, and empty for a first launch. A relaunch fills it with PRD
	// section 11's note about what already happened; relaunch.go owns the
	// text.
	note string
	// settle settles the task's slot the dispatch or relaunch took, once,
	// with the proof it is given; nil when the caller settles it itself.
	// launchTask calls it with proof at every return but a worker fallback,
	// which it hands the slot to unsettled (handSlot) for switchWorker to
	// settle once the backup's launch has settled its record, so the slot
	// is unsettled from the claim until that record is settled, across a
	// fallback too.
	settle func(tasks.Isolation)
}

// settleSlot settles the slot once with the dispatch's proof, on settle's
// terms, and is safe to call again or on a dispatch that carries none.
func (d *dispatched) settleSlot() {
	if settle := d.handSlot(); settle != nil {
		settle(d.proof)
	}
}

// handSlot gives the slot's settle to the caller, once, and leaves the
// dispatch with none to settle, so a later settleSlot settles nothing: it
// is how launchTask hands an unsettled slot to a worker fallback. It is nil
// for a dispatch that carries none.
func (d *dispatched) handSlot() func(tasks.Isolation) {
	settle := d.settle
	d.settle = nil
	return settle
}

// dispatchTask dispatches one task, on the terms this file's opening comment
// states, and returns what it established. The order of what it does is the order of
// the records it writes: the attempt claims the dispatch, the primary
// snapshot records what was read, the clone is cut, the copy is observed and
// asserted, and the copy's snapshot and the proof are written last. A task
// that does not resolve Dispatched is errTaskNotDispatched; a record that
// cannot be dispatched as written is errTaskRecord; a dispatch another caller
// claimed first is store.ErrTaskMoved; a standing copy the platform cannot
// sweep is a *dispatchUnswept, held for a person and otherwise unrecorded,
// a task kept work in its project holds is a *store.DispatchHeldError, with
// nothing recorded and the slot given back,
// unless that hold was answered retry; a sweep that ran and did not clear
// the copy is a *copyUnswept; a copy that fails the assertion is an
// *isolationRefused, recorded. Every other error is a failure of a read or a
// write, and leaves the attempt standing for a retry to find what it left.
func (s *Service) dispatchTask(ctx context.Context, id string) (dispatched, error) {
	settle, err := s.claimDispatch(id)
	if err != nil {
		return dispatched{}, err
	}
	d, err := s.dispatchClaimed(ctx, id)
	return s.settleDispatch(ctx, id, settle, d, err), err
}

// settleOrRelease ends a dispatch's hold on its slot: a dispatch left queued
// by placeable claimed nothing, so its slot is given back and the next pass
// reads the trusted copy again, and every other outcome settles the slot
// with the proof, or none, for the service's lifetime.
func (s *Service) settleOrRelease(id string, settle func(tasks.Isolation), proof tasks.Isolation, err error) {
	if errors.Is(err, errTaskUnplaced) {
		s.giveBackSlot(id)
		return
	}
	settle(proof)
}

// giveBackSlot frees the task's slot, so the next pass dispatches it, for a
// dispatch that claimed nothing.
func (s *Service) giveBackSlot(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.isolated, id)
	delete(s.claiming, id)
}

// redispatchTask is dispatchTask for a retry answered on the hold a dispatch
// over an unsweepable standing copy raised. It takes the task's slot through
// claimRelaunch, because the refusal that raised the hold settled the slot
// this service keeps for the task's lifetime and only a slot still in flight
// should refuse the answer, and then dispatches on dispatchClaimed's terms:
// the retry is read back from the hold's record there, never taken from the
// caller, and the attempt anchor spends it on one attempt, so a concurrent
// dispatcher that read the same answer is refused with store.ErrTaskMoved.
func (s *Service) redispatchTask(ctx context.Context, id string) (dispatched, error) {
	settle, err := s.claimRelaunch(id)
	if err != nil {
		return dispatched{}, err
	}
	d, err := s.dispatchClaimed(ctx, id)
	return s.settleDispatch(ctx, id, settle, d, err), err
}

// settleDispatch ends a dispatch's hold on its slot, or hands it to the
// launch: for a dispatch kept work held it gives the slot back through
// releaseHeldSlot, for one that ended before it could read the hold
// (*holdUnread) it gives the slot back and asks nothing, this file's
// opening comment says why for both; a dispatch refused because the pass's
// retirement hold is open (store.ErrPassHeld), at its claim or by reading
// the pass Held on that hold, claimed nothing, so its slot is given back
// through releasePassHeldSlot; every
// other failure is settleOrRelease's; and a dispatch that proved its copy
// keeps the slot unsettled and is returned with d.settle set, for
// launchTask to settle with the proof once the launch's record is settled,
// on the file comment's terms.
func (s *Service) settleDispatch(ctx context.Context, id string, settle func(tasks.Isolation), d dispatched, err error) dispatched {
	var unread *holdUnread
	switch {
	case errors.Is(err, store.ErrDispatchHeld):
		if s.betweenHeldAndRelease != nil {
			s.betweenHeldAndRelease()
		}
		s.releaseHeldSlot(ctx, id)
	case errors.Is(err, store.ErrPassHeld):
		if s.betweenHeldAndRelease != nil {
			s.betweenHeldAndRelease()
		}
		s.releasePassHeldSlot(ctx, id)
	case errors.As(err, &unread):
		s.giveBackSlot(id)
	case err != nil:
		s.settleOrRelease(id, settle, d.proof, err)
	default:
		d.settle = settle
	}
	return d
}

// holdUnread is a dispatch's failure before it read whether kept work holds
// the task: the task's resolution or the hold itself could not be read.
// Nothing was claimed, so the slot is given back rather than kept for the
// service's lifetime (settleDispatch). It unwraps to the read's own error.
type holdUnread struct{ err error }

// Error is the read's own error.
func (e *holdUnread) Error() string { return e.err.Error() }

// Unwrap is the read's own error.
func (e *holdUnread) Unwrap() error { return e.err }

// releaseHeldSlot gives back the slot of a task kept work held at its
// dispatch or its launch, and then reads the hold again: when nothing holds
// the task any more, or the read fails, it asks dispatchDue again through
// dispatchInBackground, so an entry that closed while the slot was taken is
// never a dispatch nobody asks for. A pass that cannot read the hold either
// gives the slot back again (holdUnread). This file's opening comment says
// why that order loses no closure, and what remains.
func (s *Service) releaseHeldSlot(ctx context.Context, id string) {
	s.giveBackSlot(id)
	holding, err := s.store.KeptWorkHolding(ctx, id)
	if err != nil || len(holding) == 0 {
		s.dispatchInBackground()
	}
}

// releasePassHeldSlot gives back the slot of a housekeeping pass whose
// dispatch its open retirement hold refused, and then reads the hold again
// (store.RetirementHoldOpen): when it is answered, or the read fails, it
// asks dispatchDue again through dispatchInBackground. A keep answer runs
// that dispatch itself, but one answered while this slot was taken passed
// the taken slot over; reading after the slot is given back means either
// this read sees the answer, or the answer's own dispatch finds the slot
// free. That is releaseHeldSlot's order, for the retirement hold.
func (s *Service) releasePassHeldSlot(ctx context.Context, id string) {
	s.giveBackSlot(id)
	if _, open, err := s.store.RetirementHoldOpen(ctx, id); err != nil || !open {
		s.dispatchInBackground()
	}
}

// claimDispatch takes the task's slot, so a second pass over the tasks does
// not begin a second attempt behind this one, and returns the function that
// settles it with what the dispatch ended at: the proof, or the zero value
// for a dispatch that ended without one. The slot is kept either way, which
// is dispatchDue's one-dispatch-per-service rule; only claimRelaunch takes a
// held slot again, for a relaunch's new attempt. The store's anchor is what
// refuses a second dispatcher in another service or on another path; this
// is the cheaper refusal within one.
func (s *Service) claimDispatch(id string) (func(proof tasks.Isolation), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slotMaps()
	if _, taken := s.isolated[id]; taken {
		return nil, fmt.Errorf("service: task %s has already been dispatched here", id)
	}
	return s.takeSlot(id), nil
}

// claimRelaunch takes the task's slot for a relaunch, or for a dispatch a
// person answered retry on the no-sweep hold of, which are the two ways a
// slot this service already holds is taken again: for a relaunch, the
// task's earlier attempt was dispatched or relaunched here and its terminal
// is gone, and the relaunch begins a new attempt; for the dispatch, the
// slot was settled by the refusal the answer lifts. A slot that a dispatch
// or a relaunch still holds unsettled is errRelaunchInFlight, so two
// relaunches in this service, or a relaunch and a dispatch, do not run side
// by side; between
// services, and between a settled slot's attempt and this one, the store's
// anchor is what refuses a second relauncher, as relaunch.go states. The
// slot is settled as claimDispatch's is.
func (s *Service) claimRelaunch(id string) (func(proof tasks.Isolation), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slotMaps()
	if s.claiming[id] {
		return nil, fmt.Errorf("%w: task %s", errRelaunchInFlight, id)
	}
	return s.takeSlot(id), nil
}

// slotMaps makes the slot maps a service built by hand in this package's
// tests may not have; Open makes both, and a nil map is no slot at all. It
// is called with s.mu held.
func (s *Service) slotMaps() {
	if s.isolated == nil {
		s.isolated = make(map[string]tasks.Isolation)
	}
	if s.claiming == nil {
		s.claiming = make(map[string]bool)
	}
	if s.steering == nil {
		s.steering = make(map[string]bool)
	}
}

// takeSlot marks the task's slot taken and unsettled and returns the
// function that settles it. It is called with s.mu held.
func (s *Service) takeSlot(id string) func(proof tasks.Isolation) {
	s.isolated[id] = tasks.Isolation{}
	s.claiming[id] = true
	return func(proof tasks.Isolation) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.isolated[id] = proof
		delete(s.claiming, id)
	}
}

// launching reports whether a dispatch or a relaunch holds the task's slot
// unsettled, which it does through the launch that follows it: the slot is
// claimed, and not by claimSteer, which marks what it holds in steering
// and can hold nothing while a claim in flight does. A stop or a cleanup
// ask holding the slot, and a dispatch that ended without a proof and
// settled, do not read as a launch. It is what the offer of a housekeeping
// pass's retirement hold reads (housekeeping.go), so that offer is skipped
// while a worker may be created and not yet recorded, and not otherwise.
func (s *Service) launching(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claiming[id] && !s.steering[id]
}

// isolationOf reports the proof this service holds for a task, and whether it
// holds a slot for it at all: a slot with an unproven value is a dispatch in
// flight or one that ended without a proof, and Proven is what tells a proof
// from either. It is the one reader of the slot map, and the slot is this
// service's memory: a service that restarts holds nothing and dispatches the
// task again, which is the retry this file describes.
func (s *Service) isolationOf(id string) (tasks.Isolation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	proof, held := s.isolated[id]
	return proof, held
}

// dispatchClaimed is dispatchTask once the in-memory slot is taken.
func (s *Service) dispatchClaimed(ctx context.Context, id string) (dispatched, error) {
	resolved, err := s.tasks().Resolved(ctx, id)
	if err != nil {
		return dispatched{}, &holdUnread{err: err}
	}
	if resolved.State.State != taskstate.Dispatched || resolved.State.Source != taskstate.SourceEvent {
		if resolved.Record.Housekeeping {
			// A pass offered its retirement hold after a dispatcher selected
			// it resolves Held from that hold: the hold's refusal, which
			// claims nothing and gives the slot back, not a pass with
			// nothing to dispatch, whose slot would be kept and so passed
			// over by the dispatch a keep answer runs.
			key, open, err := s.store.RetirementHoldOpen(ctx, id)
			if err != nil {
				return dispatched{}, &holdUnread{err: err}
			}
			if open {
				return dispatched{}, fmt.Errorf("%w: task %s holds on %s", store.ErrPassHeld, id, key)
			}
		}
		return dispatched{}, fmt.Errorf("%w: task %s resolves %s from %s", errTaskNotDispatched, id,
			resolved.State.State, resolved.State.Source)
	}
	record := resolved.Record
	// Kept work in the task's project holds it before anything is swept,
	// cut or claimed; the claim below asks again in its own transaction.
	if s.beforeHoldRead != nil {
		s.beforeHoldRead(id)
	}
	holding, err := s.store.KeptWorkHolding(ctx, id)
	if err != nil {
		return dispatched{}, &holdUnread{err: err}
	}
	if len(holding) > 0 {
		return dispatched{}, &store.DispatchHeldError{Task: id, Repository: record.RepositoryID.Or(""), Entries: holding}
	}
	e, err := s.expectationOf(ctx, record)
	if err != nil {
		return dispatched{}, err
	}
	repository, copyPath, instructions, gatePath, want, secondCheck :=
		e.repository, e.copyPath, e.instructions, e.gatePath, e.want, e.secondCheck

	// The claim. The anchor is the highest attempt read now, through the
	// attempts rather than the snapshot's copy of them, so what is anchored
	// is what this call saw.
	attempts, err := s.store.TaskAttempts(ctx, id)
	if err != nil {
		return dispatched{}, err
	}
	var latest int64
	for _, a := range attempts {
		latest = max(latest, a.Sequence)
	}
	if _, launched := latestSession(attempts); launched {
		return dispatched{}, fmt.Errorf("%w: task %s attempt %d", errTaskLaunched, id, latest)
	}
	// The first claim places the task, so it is made only once the
	// repository's trusted copy allows this machine; a later attempt stays
	// where the first claim put it (store.BeginDispatchAttempt places
	// nothing).
	if latest == 0 {
		if err := s.placeable(ctx, repository, id); err != nil {
			return dispatched{}, err
		}
	}
	// A copy already standing at a retry is the earlier attempt's, swept
	// before the claim on sweep.go's terms; a copy about to be cut has
	// nothing in it to sweep. A first claim sweeps nothing: no attempt has
	// cut a copy, so whatever stands at the path is a concurrent winner's,
	// which only the winner may touch (#562), the copy a carry on adopted,
	// which the winner sweeps after its claim, or nobody's attempt's, which
	// the check after the claim refuses.
	var swept copySwept
	var stood bool
	if latest > 0 {
		_, err := os.Lstat(copyPath)
		stood = err == nil
	}
	if stood {
		if swept, err = s.sweepStandingCopy(ctx, id, copyPath); err != nil {
			return dispatched{}, err
		}
		if swept.unsupported {
			// P6: nothing proves the copy unowned, so a person decides. Only a
			// retry on the hold for the attempt read lets this dispatch past,
			// and the anchor below spends it on one attempt.
			answer, err := s.relaunchAnswerOf(ctx, dispatchUnsweptKey(id, latest))
			if err != nil {
				return dispatched{}, err
			}
			if answer != boundRetry {
				h, err := s.holdDispatchUnswept(ctx, id, latest, swept.words)
				if err != nil {
					return dispatched{}, fmt.Errorf("service: task %s is not dispatched again with no process sweep, "+
						"and its hold could not be registered: %w", id, err)
				}
				return dispatched{}, &dispatchUnswept{Task: id, Hold: h}
			}
			swept.words += ", and its hold for a dispatch without one was answered " + machine.AnswerRetry
		}
		if swept.words != "" {
			s.log.Printf("task %s is dispatched again over its standing copy: %s", id, swept.words)
		}
	}
	if s.betweenAttemptReadAndClaim != nil {
		s.betweenAttemptReadAndClaim()
	}
	var attempt store.TaskAttempt
	if latest == 0 {
		attempt, err = s.store.BeginFirstTaskAttempt(ctx, id, store.OnThisMachine())
	} else {
		attempt, err = s.store.BeginDispatchAttempt(ctx, id, latest)
	}
	if err != nil {
		return dispatched{}, err
	}

	// The primary, read only, and recorded before the clone that reads it
	// again.
	base, err := s.recordPrimary(ctx, id, attempt.Sequence, repository)
	if err != nil {
		return dispatched{}, err
	}

	// The copy: a fresh clone when nothing stands at the path, what the
	// sweep before the claim was asked of, or at a first claim the copy a
	// carry on adopted, swept now. A path standing now that this dispatch
	// neither cut nor asked the sweep of, at a first claim or one that
	// appeared after the check before a retry's claim, is asserted and then
	// refused as not isolated, never given a worker unswept.
	if _, err := os.Lstat(copyPath); errors.Is(err, os.ErrNotExist) {
		if err := s.cutTaskCopy(ctx, record, repository.WorkingPath, copyPath, gatePath, base); err != nil {
			return dispatched{}, err
		}
	} else if err != nil {
		return dispatched{}, fmt.Errorf("service: reading the copy of task %s at %s: %w", id, copyPath, err)
	} else if !stood {
		adopted := false
		if latest == 0 {
			if adopted, err = s.adoptedCopyStands(ctx, id, copyPath); err != nil {
				return dispatched{}, err
			}
		}
		if !adopted {
			swept.unswept = fmt.Errorf("%s stands, and attempt %d of task %s neither cut it nor swept it before its claim",
				copyPath, attempt.Sequence, id)
		} else {
			// The copy a carry on adopted for this task, swept now that this
			// dispatch won the first claim, so only the winner touches it
			// (#562), on sweep.go's terms as a retry's is before its claim.
			if swept, err = s.sweepStandingCopy(ctx, id, copyPath); err != nil {
				return dispatched{}, err
			}
			if swept.unsupported {
				h, err := s.holdDispatchUnswept(ctx, id, attempt.Sequence, swept.words)
				if err != nil {
					return dispatched{}, fmt.Errorf("service: task %s is not dispatched with no process sweep, "+
						"and its hold could not be registered: %w", id, err)
				}
				return dispatched{}, &dispatchUnswept{Task: id, Hold: h}
			}
			if swept.words != "" {
				s.log.Printf("task %s is dispatched over the copy it carried on: %s", id, swept.words)
			}
		}
	}

	// The observation and the proof.
	env := agents.WorkerEnvironment(os.Environ(), nil)
	obs, err := vcs.Observe(ctx, copyPath, s.git()...)
	if err != nil {
		if errors.Is(err, vcs.ErrNotARepository) {
			return dispatched{}, s.refuseIsolation(ctx, id, attempt.Sequence, resolved.State.Basis.EventSequence,
				taskstate.RefusedNotIsolated, err)
		}
		return dispatched{}, fmt.Errorf("service: observing the copy of task %s: %w", id, err)
	}
	proof, err := tasks.AssertIsolated(obs, env, want)
	if err != nil {
		reason, refused := tasks.Refusal(err)
		if !refused {
			return dispatched{}, fmt.Errorf("service: asserting the copy of task %s: %w", id, err)
		}
		return dispatched{}, s.refuseIsolation(ctx, id, attempt.Sequence, resolved.State.Basis.EventSequence, reason, err)
	}
	if swept.unswept != nil {
		return dispatched{}, s.refuseIsolation(ctx, id, attempt.Sequence, resolved.State.Basis.EventSequence,
			taskstate.RefusedNotIsolated, fmt.Errorf("the standing copy passed the assertion and was not swept: %w", swept.unswept))
	}

	// The copy's snapshot, then the line the worker is told to run.
	snapshot, err := s.copySnapshot(ctx, proof.Copy)
	if err != nil {
		return dispatched{}, fmt.Errorf("service: reading the references of the copy of task %s: %w", id, err)
	}
	if err := s.store.SetTaskStartSnapshot(ctx, id, snapshot); err != nil {
		return dispatched{}, err
	}
	if err := appendSecondCheck(instructions, secondCheck); err != nil {
		return dispatched{}, fmt.Errorf("service: writing the second check into the instructions of task %s: %w", id, err)
	}
	return dispatched{
		id: id, proof: proof, attempt: attempt.Sequence, want: want, instructions: instructions,
		anchor: resolved.State.Basis.EventSequence,
	}, nil
}

// adoptedCopyStands reports whether what stands at copyPath is the copy a
// carry on adopted for task id: an entry answered carry-on naming the task
// records that copy's file identity (store.KeptWorkCarriedOnBy), and what
// stands is that directory (isCarriedOnCopy, keptcarryon.go's own check).
// Anything else standing there, a clone of the same history among it, is
// not, and a task no carry on created has no such entry.
func (s *Service) adoptedCopyStands(ctx context.Context, id, copyPath string) (bool, error) {
	entries, err := s.store.KeptWorkCarriedOnBy(ctx, id)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if s.isCarriedOnCopy(entry, copyPath) == nil {
			return true, nil
		}
	}
	return false, nil
}

// expectation is what a task's copy is asserted against and launched with,
// derived from the task's record: the repository it works in, the copy path
// the record reserved, its instructions file, the gate repository its one
// remote must resolve to, and tasks.Expect's expectation and second check.
// Dispatch and relaunch derive it through expectationOf, so the two cannot
// read the record differently.
type expectation struct {
	repository   store.Repository
	copyPath     string
	instructions string
	gatePath     string
	want         tasks.Expectation
	secondCheck  string
}

// expectationOf derives the expectation from a task's record, refusing with
// errTaskRecord a record that names no repository, reserved its copy
// somewhere other than home.TaskCopy, has no instructions file, or that
// tasks.Expect refuses. It reads and writes nothing of the task's.
func (s *Service) expectationOf(ctx context.Context, record store.Task) (expectation, error) {
	id := record.ID
	repositoryID, ok := record.RepositoryID.Get()
	if !ok {
		return expectation{}, fmt.Errorf("%w: task %s names no repository to cut a copy of", errTaskRecord, id)
	}
	repository, err := s.store.Repository(ctx, repositoryID)
	if err != nil {
		return expectation{}, fmt.Errorf("service: reading the repository task %s works in: %w", id, err)
	}
	copyPath, err := s.home.TaskCopy(id)
	if err != nil {
		return expectation{}, err
	}
	if filepath.Clean(record.WorktreePath) != copyPath {
		return expectation{}, fmt.Errorf("%w: task %s reserved its copy at %s and this home puts it at %s",
			errTaskRecord, id, record.WorktreePath, copyPath)
	}
	instructions, err := s.home.TaskInstructions(id)
	if err != nil {
		return expectation{}, err
	}
	if _, err := os.Stat(instructions); err != nil {
		return expectation{}, fmt.Errorf("%w: task %s has no instructions file at %s: %w", errTaskRecord, id, instructions, err)
	}
	gatePath, err := s.gatePathOf(ctx, repository)
	if err != nil {
		return expectation{}, err
	}
	want, secondCheck, err := tasks.Expect(s.home, id, record.Shape, gatePath, repository.WorkingPath, record.Branch.Or(""))
	if err != nil {
		return expectation{}, fmt.Errorf("%w: task %s: %w", errTaskRecord, id, err)
	}
	return expectation{repository: repository, copyPath: copyPath, instructions: instructions, gatePath: gatePath,
		want: want, secondCheck: secondCheck}, nil
}

// gatePathOf is the gate repository of a repository, which a task's copy
// names as its one remote and must stand outside of. A repository bound to no
// gate is refused naming the command that binds one, on branchInGate's terms.
func (s *Service) gatePathOf(ctx context.Context, repository store.Repository) (string, error) {
	binding, err := s.store.GateBinding(ctx, repository.WorkingPath)
	if err != nil {
		return "", fmt.Errorf("service: %s is not bound to a gate, so a task of it cannot be cut a copy; "+
			"run assistant init there: %w", repository.WorkingPath, err)
	}
	gatePath, err := gate.RepositoryFor(s.home.Root(), binding.GateID)
	if err != nil {
		return "", fmt.Errorf("service: locating the gate repository of %s: %w", repository.ID, err)
	}
	return gatePath, nil
}

// recordPrimary reads the primary checkout through internal/vcs's read-only
// operations - HEAD, the branch it is on, every local branch - records them
// as the attempt's primary snapshot, and returns the default branch's tip,
// which is the base a task's copy starts from. A primary with no local
// default branch is refused: the base would otherwise be invented.
func (s *Service) recordPrimary(ctx context.Context, id string, attempt int64, repository store.Repository) (string, error) {
	primary, err := vcs.OpenWorktree(ctx, repository.WorkingPath, s.git()...)
	if err != nil {
		return "", fmt.Errorf("service: opening the primary checkout of task %s: %w", id, err)
	}
	head, err := primary.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return "", fmt.Errorf("service: reading HEAD of the primary checkout of task %s: %w", id, err)
	}
	snapshot := store.PrimarySnapshot{TaskID: id, Attempt: attempt, Head: head}
	switch branch, err := primary.HeadBranch(ctx); {
	case err == nil:
		snapshot.HeadBranch = store.Known(branch)
	case !errors.Is(err, vcs.ErrDetachedHead):
		return "", fmt.Errorf("service: reading the branch of the primary checkout of task %s: %w", id, err)
	}
	branches, err := primary.ListRefs(ctx, "refs/heads/")
	if err != nil {
		return "", fmt.Errorf("service: reading the branches of the primary checkout of task %s: %w", id, err)
	}
	snapshot.Branches = make([]store.SnapshotRef, 0, len(branches))
	for _, b := range branches {
		snapshot.Branches = append(snapshot.Branches, store.SnapshotRef{Name: b.Name, Commit: b.Commit})
	}
	if repository.DefaultBranch == "" {
		return "", fmt.Errorf("%w: the repository task %s works in names no default branch to start from", errTaskRecord, id)
	}
	base, err := primary.ResolveCommit(ctx, "refs/heads/"+repository.DefaultBranch)
	if err != nil {
		return "", fmt.Errorf("service: the primary checkout of task %s has no local branch %s to start the task from: %w",
			id, repository.DefaultBranch, err)
	}
	if _, err := s.store.RecordPrimarySnapshot(ctx, snapshot); err != nil {
		return "", err
	}
	return base, nil
}

// cutTaskCopy clones the primary into the task's copy path and shapes the
// clone for the task's shape: a delivery copy gets the gate as its one remote
// and the task's branch at base; an investigation copy gets no remote and a
// detached HEAD at base. A clone that cannot be shaped is removed, on the
// terms this file's opening comment states, and the error reported.
func (s *Service) cutTaskCopy(ctx context.Context, record store.Task, primary, copyPath, gatePath, base string) error {
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		return fmt.Errorf("service: creating the directory of task %s: %w", record.ID, err)
	}
	cloned, err := vcs.Clone(ctx, primary, copyPath, "", s.git()...)
	if err != nil {
		return fmt.Errorf("service: cloning the primary checkout for task %s: %w", record.ID, err)
	}
	shape := func() error {
		// The clone is this program's and not the person's, so it is given
		// automatic maintenance off in its own configuration, on the terms the
		// opening comment of this file states.
		if err := cloned.DisableAutomaticMaintenance(ctx); err != nil {
			return fmt.Errorf("turning automatic maintenance off: %w", err)
		}
		switch record.Shape {
		case store.TaskDelivery:
			if err := cloned.SetRemote(ctx, gate.RemoteName, gatePath); err != nil {
				return fmt.Errorf("pointing %s at the gate: %w", gate.RemoteName, err)
			}
			if err := cloned.SwitchToNewBranch(ctx, record.Branch.Or(""), base); err != nil {
				return fmt.Errorf("creating the task's branch %s at %s: %w", record.Branch.Or(""), base, err)
			}
		case store.TaskInvestigation:
			if err := cloned.Detach(ctx, base); err != nil {
				return fmt.Errorf("detaching at %s: %w", base, err)
			}
		default:
			return fmt.Errorf("shape %q is not one a copy is cut for", record.Shape)
		}
		return nil
	}
	if err := shape(); err != nil {
		if removeErr := os.RemoveAll(copyPath); removeErr != nil {
			s.log.Printf("the clone cut for task %s could not be shaped and was not removed either: %v", record.ID, removeErr)
		}
		return fmt.Errorf("service: shaping the copy of task %s: %w", record.ID, err)
	}
	return nil
}

// refuseIsolation records the refusal as the task's "isolation-refused"
// event, anchored to the history the dispatch was decided from, and returns
// the typed refusal. The token is checked against the kind's own decoder
// before it is appended, because the store records any text and
// internal/taskstate's table is the one place the vocabulary is defined. The
// assertion's own words go to the log: the record carries the token and the
// history is not the place for prose.
func (s *Service) refuseIsolation(ctx context.Context, id string, attempt, anchor int64,
	reason taskstate.IsolationRefusal, cause error) error {
	row, ok := taskstate.Lookup(isolationRefusedKind)
	if !ok || row.Detail == nil {
		return fmt.Errorf("service: the %q kind has no decoder to check %q against", isolationRefusedKind, reason)
	}
	if err := row.Detail(string(reason)); err != nil {
		return fmt.Errorf("service: task %s: %w", id, err)
	}
	s.log.Printf("task %s attempt %d was refused a worker: %v", id, attempt, cause)
	event, err := s.store.AppendTaskEventAfter(ctx, id, anchor, isolationRefusedKind, string(reason))
	if err != nil {
		return fmt.Errorf("service: recording the refusal of task %s: %w", id, err)
	}
	return &isolationRefused{Task: id, Attempt: attempt, Reason: reason, Event: event, Cause: cause}
}

// isolationRefusedKind is the kind internal/taskstate gives the refusal. It is
// spelled here because that package exports the row and not the name; Lookup
// is what checks the spelling is one that package knows.
const isolationRefusedKind = "isolation-refused"

// copySnapshot reads what the proven copy holds: HEAD, the branch it is on,
// and every reference, through internal/vcs's read-only operations.
func (s *Service) copySnapshot(ctx context.Context, copyPath string) (store.CopySnapshot, error) {
	copied, err := vcs.OpenWorktree(ctx, copyPath, s.git()...)
	if err != nil {
		return store.CopySnapshot{}, err
	}
	head, err := copied.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return store.CopySnapshot{}, err
	}
	snapshot := store.CopySnapshot{Head: head, Refs: []store.SnapshotRef{}}
	switch branch, err := copied.HeadBranch(ctx); {
	case err == nil:
		snapshot.Branch = branch
	case !errors.Is(err, vcs.ErrDetachedHead):
		return store.CopySnapshot{}, err
	}
	refs, err := copied.ListRefs(ctx)
	if err != nil {
		return store.CopySnapshot{}, err
	}
	for _, r := range refs {
		snapshot.Refs = append(snapshot.Refs, store.SnapshotRef{Name: r.Name, Commit: r.Commit})
	}
	return snapshot, nil
}

// appendSecondCheck appends the second check to the instructions file under
// its own heading, unless the file already carries the line, so a retry does
// not tell the worker twice.
func appendSecondCheck(path, line string) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.Contains(string(existing), line) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	text := "\n\n" + secondCheckHeading + "\n\n" +
		"Before creating any branch or committing anything, run this line in the working copy you were started in " +
		"and stop if it exits non-zero: it checks that you are in the isolated copy the program cut for this task " +
		"and nowhere else.\n\n    " + line + "\n"
	if _, err := f.WriteString(text); err != nil {
		return err
	}
	return f.Sync()
}
