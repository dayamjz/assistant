package service

import (
	"context"
	"fmt"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Fleet reconciliation at service start: the table, the bound, and the two
// phases.
//
// PRD section 11 has the coordinator do a cheap presence check per task at
// session start and reconcile only the tasks recorded in this home: a
// terminal gone with the work not landed is relaunched in its own copy with
// the same instructions plus a note, ambiguous liveness is left alone, and
// queued wakes are drained before anything else. The drain is the
// coordinator's, and draining acknowledges, so the pass reads the queue with
// store.QueuedWakes and takes nothing: what it reads is logged as standing
// for the coordinator, whose own drain is what hands it over. This file is that pass as
// the service runs it, inside recover, after the runs are reconciled and
// before anything is served, and before supervise.go's loop begins. "Before
// serving anything" is a property of the claim and not of the launch: the
// synchronous half reads the records, probes each terminal under a bound and
// takes the anchored attempt claim for every relaunch it decides; the launch
// of each claimed attempt goes to a goroutine registered through startWork,
// as a continued run does, so a home with several dead terminals comes back
// serving after the claims and Close still waits for every launch it
// started.
//
// # The table
//
// reconcileRow is the whole decision about what a task's resolution calls
// for, keyed on the resolved State, the Source that decided it and the
// liveness the probe answered, and nothing else: no run is re-read and no
// proof is called. The relaunch rows are two. Unknown from SourceProcess with
// Dead is a terminal gone with no run attributed, which is a crashed worker;
// Failed with Dead is a terminal gone after its run failed or was ended, or
// after the worker reported failure itself. Every other state with Dead is
// nothing, because a dead terminal is the ordinary shape of a worker that
// handed off: Validating is the gate's, Ready is a pull request awaiting a
// merge, Reported is an investigation delivered, Held is a person's, and
// Landed and Complete are terminal records that outrank a run. Held is also
// how a task reads while a hold naming it is open, from internal/taskstate's
// hold tier, whatever its run or terminal says, so a task holding at the
// relaunch bound or on the no-sweep hold reaches this table as Held and is
// left alone until the answer closes the hold and it resolves Failed or
// Unknown again; the bound's hold is therefore registered once, by the pass
// that first finds the task past the bound, and a retry is acted on by the
// answer's own relaunch or, where that was refused, by the next pass, which
// reads the closed hold through taskholds.go. "Not landed"
// is therefore the state itself and not a question asked of the code host.
// Live and Unobserved are nothing in every row: a live terminal is a worker
// to leave alone, and an unobserved one is PRD section 11's ambiguous
// liveness, which relaunching on would risk two supervisors for one task.
// The action set is closed at relaunch, surface, hold and nothing, and the test
// derives the table's domain from taskstate.States, Sources and Livenesses,
// so a state or a source added later fails until a row decides it.
//
// A reported investigation reaches the table as Reported over whatever its
// terminal reads, from internal/taskstate's report tier, which reads the
// stored report row and outranks the process tier, so the table's Reported
// row is what leaves it alone and nothing here reads which entry was newest.
// Before that tier existed such a task resolved Unknown from SourceProcess
// once its worker ended, a relaunch row, and decideReconciliation turned it
// into nothing on Basis.EventKind; that rule is gone because the resolution
// it guarded against can no longer be produced for a task with a stored
// report, and keeping it would have left a second, weaker owner of the same
// fact.
//
// Surface and hold never come from the table. decideReconciliation turns a
// relaunch row into a surface for a Failed whose deciding event is the
// isolation assertion's own refusal, because the copy was already rejected
// and a relaunch would assert it again to the same end; and into a hold for
// a task the program has already relaunched relaunch_attempts times, counted
// from its attempt rows, because a worker that fails the same way every
// start is a retry nobody bounded, and PRD section 10's row has the task
// hold for a person there. A stop answered on the hold a relaunch with no
// process sweep raised turns it into nothing; the retry on that hold is
// relaunch.go's to read, because only the sweep says whether it is needed. The answer to that hold, read from its record by
// taskholds.go, is the third input: a retry relaunches once more past the
// bound and a stop leaves the task alone, and taskholds.go says why the
// anchor makes a retry good for one attempt. The bound is read on both
// relaunch rows and not on the Failed row alone: a worker that crashes with
// no run is the same unbounded retry otherwise. The surface case is the rule
// stated and no more: a refusal is recorded on an attempt that has no
// session, the reader probes the latest attempt only, and a dead terminal
// with no run outranks the log, so no record this build writes reaches the
// table as Failed from the event tier with a dead terminal. A refused copy's task reads Failed
// from the refusal with nothing to probe, the table leaves it, and the
// refusal reaches a person through the log and the task surface, as a
// dispatch's does.
//
// # A surface is the loop's wake, not a second one
//
// This file writes no wake and no marker; the bound's hold is a hold record,
// registered through taskholds.go, and not a row, as is the no-sweep hold
// relaunch.go registers. The wake queue and the
// detector markers are supervise.go's: every row and every marker is written
// by the supervisor loop under its incarnation fence, which this pass runs
// before and cannot hold, and the loop surfaces a terminal that is dead or
// that could not be observed as a terminal-gone wake, per reference and case,
// on its first pass after this one and again once per escalation_window while
// it stays silent. So a task this pass decides to
// surface rather than relaunch is told to the person by that row, and what
// this pass adds is the reason it was not relaunched, in the service log; a
// second row for the same dead terminal would be a second owner of one fact.
// The one row this pass's relaunch does write to is the acknowledgement: a
// terminal-gone wake about the session a relaunch replaced is handled, and
// relaunch.go acknowledges it as the program through the loop's own
// composition of the marker, terminalGoneAbout.
//
// For the same reason an unobserved terminal has no tolerance here: PRD
// section 11's ambiguous liveness is left alone by this pass, the loop
// surfaces it at once as unobservable and keeps the unobserved-since marker
// that says how long, and a tolerance counted here would be a second owner
// of that marker and a second surface for the same silence. The row the
// issue proposed for it, unobserved_tolerance, is therefore not a row.
//
// # Queued
//
// The Queued row is nothing here, and the task is handed to internal/tasks'
// Release by recover's next call, which is idempotent and is the one owner
// of whether a blocker has cleared; a Queued task has no terminal to probe
// and no copy to relaunch into.
//
// # Wake rows at start
//
// Draining is consuming the queued rows as input, not acting on them from
// here: the coordinator is the queue's consumer. What the service does
// acknowledge, as store.AcknowledgedByProgram, is exactly the rows its own
// relaunch superseded: a terminal-gone row for the session reference the
// relaunch replaced. Every other row stays queued for the coordinator,
// including a row composed some other way, which is the safe direction.
//
// # The bound
//
// Every probe is asked under reconcileProbeTimeout, on its own goroutine, and
// a whole read of the fleet under reconcileProbeBound; a probe that has not
// answered when either elapses reads Unobserved. That is what keeps Open from
// hanging with the home's lock held behind a provider that never answers,
// and it is what the blocking-provider test holds Open to. A probe that
// ignores its context is still bounded here, at the cost of a goroutine that
// returns when the probe does, which is stated rather than closed.
//
// # What the probe is
//
// The probe is the service's Terminals' Probe, the one seam a terminal is
// observed through, reached through the service's one reader constructor,
// which the task surface, the queue and the supervisor loop share, so what
// reconciliation sees of a terminal is what a person listing tasks sees and
// what the loop acts on. In production that is the service's own session
// provider, on terminals.go's terms: a service that restarts holds no
// session, so every reference it finds is answered by identity, Dead for a
// worker whose process is gone and Unobserved for one still present, which
// is exactly the reading the two relaunch rows and the ambiguous-liveness
// row are keyed on. A test may pass a double on Options.Terminals instead.
//
// # Residual gaps
//
//   - A dead terminal proves the terminal is gone, not that nothing is still
//     writing in the copy: a detached descendant can outlive its leader. The
//     table decides on the dead reading, and relaunch.go then asks the sweep
//     of the copy before it claims, on sweep.go's terms: on Linux and macOS
//     the copy is swept and a sweep that does not clear it refuses the
//     relaunch, and on every other platform agents.ErrSweepUnsupported is the
//     answer and the relaunch is refused and holds for a person, going into
//     the copy only on a retry answer, unproven and saying so. This file does
//     not say "prove" for that platform; a retry there is a person's decision
//     to go ahead without the proof.
//   - A relaunch the floor defers, because a live task now holds the
//     branch, is retried at the next reconciliation, which is the next
//     service start: no release site re-runs this pass, so such a task
//     waits for a restart.
//   - The read is one consistent snapshot of the records and the probes are
//     taken after it, which internal/taskstate states of every reader; a
//     terminal that dies between the read and the claim is relaunched on the
//     next start.
//   - The bound is per read, and a pass makes one read plus one per
//     relaunch, since each relaunch decides from a read of its own; at the
//     fleet sizes PRD section 12 contemplates that is a few seconds in the
//     worst case and nothing here has measured it past them.
//   - A task whose relaunch refused the copy resolves Failed from the
//     refusal with no terminal to probe, exactly as a dispatch's refusal
//     does today, and reaches a person through the log and the task
//     surface rather than the wake queue.
//   - A task this pass surfaces rather than relaunches, which is a task
//     whose copy the isolation assertion refused, is told to the person by
//     the loop's terminal-gone row and nothing else: that row says the
//     terminal is gone and not that the program declined to relaunch it,
//     which is in the service log only. A task past the bound is told by
//     the loop's hold-opened row for the hold this pass registered as well.

// reconcileProbeTimeout bounds one probe of one terminal. Chosen, not
// measured: a provider answers from a process table in far less, and a task
// surface that waits this long per task for a provider that is down is slow
// rather than hung.
const reconcileProbeTimeout = time.Second

// reconcileProbeBound bounds one read of the whole fleet at reconciliation,
// whatever its size. Chosen, not measured: enough for every task of a fleet
// PRD section 12 contemplates to be probed in turn, and short enough that a
// restart behind a provider that never answers is still a restart.
const reconcileProbeBound = 15 * time.Second

// reconcileDrainLimit bounds the read of the queue that is the pass's input,
// a store.QueuedWakes read that acknowledges nothing. A queue
// longer than this has a coordinator that stopped reading it; the pass reads
// the oldest rows and says so.
const reconcileDrainLimit = 4096

// reconcileAction is what the table calls for. The set is closed.
type reconcileAction string

const (
	// reconcileRelaunch is PRD section 11's terminal gone with the work not
	// landed: relaunch in the same copy.
	reconcileRelaunch reconcileAction = "relaunch"
	// reconcileSurface is a task the program will not relaunch on its own
	// and a person has not been told about: one terminal-gone wake.
	reconcileSurface reconcileAction = "surface"
	// reconcileHold is a task past relaunch_attempts that nobody has
	// answered: the hold taskholds.go registers, which a person answers.
	reconcileHold reconcileAction = "hold"
	// reconcileNothing is leave it alone.
	reconcileNothing reconcileAction = "nothing"
)

// reconcileRow is the reconciliation table, as this file's opening comment
// states it. It is a pure function of the three inputs.
func reconcileRow(state taskstate.State, source taskstate.Source, process taskstate.Liveness) reconcileAction {
	if process != taskstate.Dead {
		return reconcileNothing
	}
	switch {
	case state == taskstate.Unknown && source == taskstate.SourceProcess:
		return reconcileRelaunch
	case state == taskstate.Failed:
		return reconcileRelaunch
	}
	return reconcileNothing
}

// reconcileDecision is what decideReconciliation made of one task: the
// action, the words for the log and for the relaunched note, and for a
// surface which rule turned the relaunch into one.
type reconcileDecision struct {
	action reconcileAction
	reason string
	// surfaced names why the task surfaces rather than relaunches, and is
	// empty for every other action.
	surfaced string
}

// The two surface rules.
const (
	surfacedIsolationRefused = "isolation-refused"
	surfacedRelaunchBound    = "relaunch-bound"
)

// decideReconciliation applies the table to a resolution and then the rules
// that turn a relaunch into something else: a Failed decided by the
// isolation assertion's own refusal, read off Basis.EventKind and never off
// the log, surfaces; a task whose stop's hold (steer.go) or no-sweep hold
// for its latest attempt is answered stop is left alone; and a task already
// relaunched bound times,
// counted from attempts, holds for a person unless the bound's hold for its
// latest attempt is answered. A stop's hold answered retry is not read
// here either: it is what lets the task resolve from its dead terminal
// again, and the count applies to that relaunch like every other. answered
// carries the holds' answers, which
// taskholds.go reads: a retry on the bound's relaunches once more past the
// bound, and a stop on either leaves the task alone. A retry on the no-sweep
// hold is not read here, because whether the platform has a sweep is known
// only once relaunch.go asks it; relaunch.go reads that answer itself. The
// count is relaunches, so a task's first attempt is not one.
func decideReconciliation(r taskstate.Resolution, attempts []store.TaskAttempt, bound int, answered relaunchAnswers) reconcileDecision {
	action := reconcileRow(r.State, r.Source, r.Basis.Process)
	if action != reconcileRelaunch {
		return reconcileDecision{action: action}
	}
	if r.State == taskstate.Failed && r.Source == taskstate.SourceEvent && r.Basis.EventKind == isolationRefusedKind {
		return reconcileDecision{action: reconcileSurface, surfaced: surfacedIsolationRefused,
			reason: "its copy was refused by the isolation assertion, which a relaunch would assert again"}
	}
	if answered.stopped == boundStop {
		return reconcileDecision{action: reconcileNothing,
			reason: "its worker was stopped, and the stop's hold was answered not to begin it again"}
	}
	if answered.unswept == boundStop {
		return reconcileDecision{action: reconcileNothing,
			reason: "this platform has no process sweep, and its hold for a relaunch without one was answered not to retry it"}
	}
	if relaunches := max(len(attempts)-1, 0); relaunches >= bound {
		past := fmt.Sprintf("it has been relaunched %d times and relaunch_attempts is %d", relaunches, bound)
		switch answered.bound {
		case boundRetry:
			return reconcileDecision{action: reconcileRelaunch,
				reason: past + ", and its hold at the bound was answered " + machine.AnswerRetry}
		case boundStop:
			return reconcileDecision{action: reconcileNothing,
				reason: past + ", and its hold at the bound was answered not to retry it"}
		}
		return reconcileDecision{action: reconcileHold, surfaced: surfacedRelaunchBound, reason: past}
	}
	return reconcileDecision{action: reconcileRelaunch,
		reason: fmt.Sprintf("its terminal is gone and it resolves %s from its %s", r.State, r.Source)}
}

// boundedProbe wraps the Terminals' probe so that one answer costs at most
// reconcileProbeTimeout and none is asked once bound has ended, on the terms
// the opening comment's bound section states. It is nil for a Service with
// no Terminals, which Open never produces and this package's tests build by
// hand, so such a reader observes nothing rather than asking a bound of
// nothing.
func (s *Service) boundedProbe(bound context.Context) func(ctx context.Context, sessionRef string) taskstate.Liveness {
	terminals := s.terminals
	if terminals == nil {
		return nil
	}
	probe := func(ctx context.Context, ref string) taskstate.Liveness { return terminals.Probe(ctx, ref).Liveness }
	return func(_ context.Context, ref string) taskstate.Liveness {
		if bound.Err() != nil {
			return taskstate.Unobserved
		}
		ctx, cancel := context.WithTimeout(bound, reconcileProbeTimeout)
		defer cancel()
		answered := make(chan taskstate.Liveness, 1)
		go func() { answered <- probe(ctx, ref) }()
		select {
		case liveness := <-answered:
			return liveness
		case <-ctx.Done():
			return taskstate.Unobserved
		}
	}
}

// reconcileTasks is the pass, on the terms the opening comment states. It
// reports to the log and not to a caller: recover has nobody to answer, and
// every task it does not settle is read again at the next start.
func (s *Service) reconcileTasks(ctx context.Context) {
	if s.terminals == nil {
		s.log.Printf("reconciliation: this service has no terminal seam, so every terminal reads unobserved and nothing is relaunched")
	}
	queued, err := s.store.QueuedWakes(ctx, reconcileDrainLimit)
	switch {
	case err != nil:
		s.log.Printf("reconciliation: the queued wakes could not be read: %v", err)
	case len(queued) > 0:
		s.log.Printf("reconciliation: %d queued wake(s) stand for the coordinator%s", len(queued), drainNote(len(queued)))
	}
	bound, cancel := context.WithTimeout(ctx, reconcileProbeBound)
	defer cancel()
	fleet, err := s.reader(s.boundedProbe(bound)).All(ctx)
	if err != nil {
		s.log.Printf("reconciliation: the tasks could not be read: %v", err)
		return
	}
	for _, r := range fleet {
		attempts, err := s.store.TaskAttempts(ctx, r.Record.ID)
		if err != nil {
			s.log.Printf("reconciliation: the attempts of task %s could not be read: %v", r.Record.ID, err)
			continue
		}
		answered, err := s.relaunchAnswersOf(ctx, r.Record.ID, attempts)
		if err != nil {
			s.log.Printf("reconciliation: the relaunch holds of task %s could not be read: %v", r.Record.ID, err)
			continue
		}
		decision := decideReconciliation(r.State, attempts, s.cfg.RelaunchAttempts, answered)
		switch decision.action {
		case reconcileRelaunch:
			s.relaunchAtStart(ctx, r.Record.ID)
		case reconcileSurface:
			s.log.Printf("task %s is not relaunched (%s); the supervisor's terminal-gone wake is what tells the coordinator",
				r.Record.ID, decision.reason)
		case reconcileHold:
			if h, err := s.holdAtBound(ctx, r.Record.ID, attempts, decision); err != nil {
				s.log.Printf("task %s is not relaunched (%s), and its hold could not be registered: %v", r.Record.ID, decision.reason, err)
			} else {
				s.log.Printf("task %s is not relaunched (%s); it holds for a person on %s", r.Record.ID, decision.reason, h.Key)
			}
		case reconcileNothing:
			if decision.reason != "" {
				s.log.Printf("task %s is not relaunched: %s", r.Record.ID, decision.reason)
			}
		}
	}
}

// drainNote is the words for a read of the queue that hit its limit.
func drainNote(n int) string {
	if n < reconcileDrainLimit {
		return ""
	}
	return ", which is the read's limit, so more may stand"
}

// latestSession is the session reference of the latest attempt, and whether
// it has one. It is the reference the reader probes, on internal/taskstate's
// terms, so a decision about a terminal is about this reference.
func latestSession(attempts []store.TaskAttempt) (string, bool) {
	if len(attempts) == 0 {
		return "", false
	}
	return attempts[len(attempts)-1].SessionRef.Get()
}

// relaunchAtStart relaunches one task the pass decided for, and launches
// the attempt it claimed on a goroutine of this service's. What the relaunch
// refuses is logged and left for the next start.
func (s *Service) relaunchAtStart(ctx context.Context, id string) {
	out, err := s.relaunchTask(ctx, id)
	if err != nil {
		s.log.Printf("task %s was not relaunched: %v", id, err)
		return
	}
	s.log.Printf("task %s was relaunched in place as attempt %d, superseding session %s; %s",
		id, out.launch.attempt, out.superseded, out.swept)
	s.launchInBackground(out.launch)
}

// launchInBackground launches a claimed attempt on a goroutine registered
// through startWork, so recover returns after the claim and Close waits for
// the launch. The outcome goes to the log on dispatchDue's terms.
func (s *Service) launchInBackground(d dispatched) {
	if !s.startWork() {
		s.log.Printf("task %s attempt %d was claimed and not launched because this service is giving up the home; "+
			"the next start finds the attempt with no session and relaunches", d.id, d.attempt)
		return
	}
	go func() {
		defer s.work.Done()
		launched, err := s.launchTask(s.stopCtx, d)
		if err != nil {
			s.log.Printf("task %s attempt %d was not launched: %v", d.id, d.attempt, err)
			return
		}
		s.log.Printf("task %s attempt %d was launched: its worker runs in terminal session %s",
			d.id, d.attempt, launched.Reference())
	}()
}

// supersededBy reports whether a queued wake is about the session a relaunch
// replaced: a terminal-gone row of the task whose marker, as the loop
// composes it, names that reference. A row composed another way is not
// matched and stays queued.
func supersededBy(w store.Wake, id, ref string) bool {
	return w.TaskID == id && w.Kind == string(supervisor.KindTerminalGone) && terminalGoneAbout(w.Marker, ref)
}
