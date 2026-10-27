package service

import (
	"context"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// What this service does about a task whose work has landed: it ends the
// worker it holds for it, and it asks internal/tasks' cleanup decision
// again once the task's terminal reads Dead. Both are decided from the
// resolution, internal/taskstate's terminal tier, and never from a log line
// or an event (P8).
//
// # Ending the worker
//
// A worker started with stream-json on both streams waits on its standard
// input after its push, so nothing else ends it when its work lands. Once a
// task resolves in the terminal tier, SourceRecord, this service ends the worker of the
// task's latest attempt through the session it holds, the provider's Close,
// which is the held half of steer.go's endWorker and the way task.stop ends
// a worker this service launched. A worker this service does not hold, one
// an earlier service of this home launched, is not ended here: signalling a
// process by its recorded identity from a loop is a decision this file does
// not take on its own, and task.stop remains the way to end one. Whether
// this service holds the session decides only what there is to end; it is
// never read as the terminal's liveness, which stays the resolution's.
//
// # Retrying the cleanup
//
// cleanup.go's landing sites run once, when the merge record, the
// nothing-to-deliver record or the local landing record is written, and a terminal that did not read Dead then,
// because the worker was one this service could not end or had not yet
// exited, left the copy standing. The decision is therefore asked whenever
// the task's terminal is observed Dead, from three places, each handing internal/tasks the liveness a resolution read
// through the service's Terminals, on cleanup.go's terms (a retired
// housekeeping pass's, below, with one exception retiredTerminal states):
//
//   - the landing sites themselves, through settleLanded, which ends the
//     held worker first and then reads the task again, so a landing whose
//     worker this service held is cleaned up in the same call;
//   - the supervisor loop's pass, which already resolves every task and
//     probes every latest attempt's terminal, through landedFromPass, which
//     decides whether to act from that read and then, on its goroutine,
//     reads the task once more so the decision gets the terminal as of
//     after any ending;
//   - task.stop, which reads the task after its probe answered Dead and,
//     for a Landed one, asks the decision instead of registering a hold.
//
// None of them proves anything about the copy. A Dead terminal is only the
// precondition internal/tasks examines a copy under; the removal is still
// P12's proof, made by that package over the copy and the upstream, and a
// stop's Dead is never handed over in place of the resolution's.
//
// # A reported investigation
//
// An investigation's copy is owed the same decision once its report is
// stored, and cleanup.go's storeTaskReport asks it at once; but the worker
// that filed the report is usually still running then, so that ask keeps
// the copy with the live refusal. The pass therefore asks again, on the
// same once-per-reference terms, for an investigation reportedCleanupOwed
// accepts whose terminal it read Dead. It never ends such a worker: a
// reported investigation may still be promoted, which needs its worker and
// its copy, so the worker ends when the coordinator stops it or when it
// exits on its own, and the copy goes only after that. Nothing here reads
// the report: whether it is stored and its holds registered is
// internal/tasks' to verify inside the decision, and the predicate only
// decides that the decision is worth asking.
//
// # A relayed investigation
//
// Once the coordinator records an investigation's findings relayed, the task
// resolves Complete from internal/taskstate's terminal tier, read from the
// findings relayed record, and it is treated as every task in that tier is:
// owed no relaunch and keeping no worker, so the worker this service holds
// for it is ended, and the cleanup decision is owed for its copy while the
// record carries no removal. That is decided from the record the resolution
// read (Basis.Relayed beside the terminal tier's Complete), never from the
// log's tail: the relay appends its own entry after the report's, so the
// report-ready reading below would otherwise stop owing the copy the moment
// the findings were relayed. A relayed investigation is not promotable, so
// nothing here has to keep its worker for a promotion.
//
// # A retired housekeeping pass
//
// A retired pass is owed the decision too, on housekeeping.go's terms
// (retiredCleanupOwed, retiredTerminal, logRetiredCleanup): its retirement
// asks it once through cleanUpLandedTask, and the pass asks again on the
// same cadence while the copy carries no removal, so a service that stopped
// between the retirement record and that first ask, or a refusal that
// changes with the world, is asked again rather than left with work nothing
// surfaces (P15). Its worker is never ended here: a retirement is recorded
// only with its worker read dead or its latest attempt never having had a
// session.
//
// # Cadence
//
// The landing sites run once per landing record. The stop path runs once
// per stop, which is a person's or a coordinator's request. The pass is the
// one recurring trigger, at the supervisor's beacon interval, and it bounds
// itself: a worker is ended at most once, because ending it forgets the
// handle the next pass would find; and the cleanup is asked again for a
// task and session reference observed Dead only once landedAskSpacing has
// passed since it was last asked under that reference, and at most
// landedAsks times in this service's life, counted in landedTried after
// each answer to an ask made through askLandedCleanup: a landing site's,
// the pass's and a stop's. cleanup.go's ask when a report is stored is not
// counted, so it neither spends the bound nor delays the pass. Asking again is what lets a
// refusal that changes with the world, an upstream that could not be read
// or a pull request the code host had not yet reported merged, end in a
// removal without anybody stopping or restarting anything. The spacing is
// because a refusal the proof answered from what the upstream holds
// answers the same until something outside this service changes, and
// asking at every interval would fetch the upstream at every interval for
// nothing; the bound is because some refusals never change by themselves,
// work that reached no upstream above all, and nothing retries without a
// bound. Past it the kept work entry the first refusal raised is what
// stands, and a stop or a restart, which forgets the memory, asks again.
// The bound is on how often one task's cleanup is asked, the shape of
// relaunch_attempts, and not one of internal/graph's loop bounds: it stops
// no run and parks nothing. The work runs on a goroutine registered through
// startWork, never on the loop's own, because a fetch is not bounded by the
// beacon interval the loop's calls are held to, and it takes the task's
// slot through claimSteer, so it never interleaves with a stop, a relaunch
// or a dispatch of the same task; a slot already taken is skipped and the
// next pass looks again.
//
// # Residual gaps
//
//   - A worker an earlier service launched is not ended automatically; its
//     terminal reads Unobserved while it runs, so its copy stays until
//     task.stop ends it, after which the stop path asks the decision.
//   - A Close that fails has already forgotten the handle, on endWorker's
//     terms, so such a worker is not ended again here; the log says so.
//   - The terminal is as of the resolution's probe, and a worker that
//     starts writing in the copy between that probe and the removal is
//     caught only by internal/tasks' inventory re-read, as cleanup.go says.
//   - A refused cleanup is asked again at most landedAsks times, spaced by
//     landedAskSpacing, under one Dead terminal, and then only at a stop or
//     a restart; nothing watches the upstream for the change that would let
//     the proof pass, so a change that comes later than that waits for one.
//     The memory is the service's, so a restart begins the count afresh.
//   - landedAskSpacing and landedAsks are constants of this package, chosen
//     and not measured, and no configuration row sets them.
//   - What the decision answered goes to the log, as at every task-copy
//     site, and travels on no answer: task.stop's answer says no hold was
//     registered and not whether the copy was removed.
//   - A reported investigation's worker that neither exits nor is stopped
//     keeps its copy for as long as it runs; nothing here ends it.
//   - A removed investigation copy cannot be promoted, which internal/tasks
//     refuses as a missing copy, so a coordinator that means to promote a
//     reported investigation does so before stopping its worker.

// landedEnds reports whether a resolution is in internal/taskstate's terminal
// tier, the one that says the task's work is finished: Landed, Complete on
// top of it, or an investigation's Complete from its findings relayed
// record. Such a task is owed no relaunch and keeps no worker.
func landedEnds(r taskstate.Resolution) bool {
	return r.Source == taskstate.SourceRecord
}

// landedCleanupOwed reports whether a resolution is one whose copy the
// cleanup decision is still owed for: Landed from the terminal tier, and
// not yet Complete.
func landedCleanupOwed(r taskstate.Resolution) bool {
	return landedEnds(r) && r.State == taskstate.Landed
}

// reportedCleanupOwed reports whether o is an investigation whose filed
// report makes the cleanup decision worth asking for its copy: still an
// investigation, its copy not recorded removed, the report as the newest
// entry the resolver read (Basis.EventKind) or the task resolved Complete
// from its findings relayed record (relayedEnds), and every hold open on the
// task one a report's ask finding registered (tasks.HoldKey's space). The
// state is not read: internal/taskstate resolves such a task Reported from
// its report tier, but Held from the hold tier above it while a finding hold
// is open, and a finding hold is exactly what must not keep the copy, so the
// state cannot be the predicate. The rule is #259's and is kept as it stood:
// an entry the worker appended after its report makes the cleanup not owed
// here, where Basis.ReportStored would still say a report is stored; that
// is a narrower reading than the report tier's, stated rather than widened
// in the change that added the tier. A finding hold does not keep
// the copy, because PRD section 11 has the hold survive cleanup; any other
// open hold does, because a stop's or a relaunch bound's retry would
// relaunch in this copy. It reads what the resolver recorded and the holds
// the snapshot carries, never the log's tail (P8), and decides nothing
// about the copy: internal/tasks verifies the report inside the decision.
func reportedCleanupOwed(o taskstate.Observed) bool {
	if o.Snapshot.Task.Shape != store.TaskInvestigation || o.State.Basis.CleanupRemoved ||
		(o.State.Basis.EventKind != store.ReportReadyEventKind && !relayedEnds(o.State)) {
		return false
	}
	findings := tasks.HoldKey(o.Snapshot.Task.ID, "")
	for _, h := range o.Snapshot.Holds {
		if h.Open() && !strings.HasPrefix(h.Key, findings) {
			return false
		}
	}
	return true
}

// relayedEnds reports whether a resolution is an investigation's Complete
// from internal/taskstate's terminal tier, read from its findings relayed
// record (Basis.Relayed), rather than a delivery task's.
func relayedEnds(r taskstate.Resolution) bool {
	return landedEnds(r) && r.State == taskstate.Complete && r.Basis.Relayed
}

// copyCleanupOwed reports whether the cleanup decision is still owed for
// o's copy, as a landed task's, as a reported investigation's, or as a
// retired housekeeping pass's (housekeeping.go's retiredCleanupOwed).
func copyCleanupOwed(o taskstate.Observed) bool {
	return landedCleanupOwed(o.State) || reportedCleanupOwed(o) || retiredCleanupOwed(o)
}

// cleanupTerminal is the terminal the cleanup decision is asked with for
// o's copy: the liveness o's resolution read, except for a retired
// housekeeping pass, which housekeeping.go's retiredTerminal answers.
func cleanupTerminal(o taskstate.Observed) taskstate.Liveness {
	if retiredCleanupOwed(o) {
		return retiredTerminal(o)
	}
	return o.State.Basis.Process
}

// settleLanded is what a landing site does about one landed task: it ends
// the worker this service holds for the task's latest attempt, reads the
// task again through the service's reader, and asks the cleanup decision
// with the terminal that read resolved. A slot another stop, relaunch or
// dispatch holds is left alone and logged; the pass asks again.
func (s *Service) settleLanded(ctx context.Context, id string) {
	release, err := s.claimSteer(id)
	if err != nil {
		s.log.Printf("task %s landed and its worker and copy were left for the supervisor's next pass: %v", id, err)
		return
	}
	defer release()
	s.endLandedWorker(ctx, id)
	s.cleanUpLandedTask(ctx, id)
}

// endLandedWorker ends the worker this service holds for the task's latest
// attempt, when it holds one, and logs what came of it. It reads the attempt
// so a session this service holds for an attempt the record has moved past
// is not mistaken for the latest one.
func (s *Service) endLandedWorker(ctx context.Context, id string) {
	latest, ok, err := s.latestAttempt(ctx, id)
	if err != nil {
		s.log.Printf("task %s is finished and its latest attempt could not be read, so its worker was not ended: %v", id, err)
		return
	}
	ref, launched := latest.SessionRef.Get()
	if !ok || !launched {
		return
	}
	ended, err := s.endHeldWorker(id, ref)
	switch {
	case err != nil:
		s.log.Printf("task %s is finished and its worker (session %s) was not ended: %v", id, ref, err)
	case ended:
		s.log.Printf("task %s is finished, so this service ended its worker (session %s) through the session it holds", id, ref)
	}
}

// cleanUpLandedTask reads the task through the service's reader and, while
// it is still owed a cleanup, asks the decision with the terminal
// cleanupTerminal reads off that read, and counts the ask in landedTried
// when that terminal is Dead, so the pass asks again only on its spacing and
// bound. It logs what the decision answered.
func (s *Service) cleanUpLandedTask(ctx context.Context, id string) {
	o, err := s.tasks().ObserveTask(ctx, id)
	if err != nil {
		s.log.Printf("the isolated copy of task %s was not cleaned up: its terminal could not be read: %v", id, err)
		return
	}
	s.askLandedCleanup(ctx, o)
}

// askLandedCleanup asks the cleanup decision for a task read in o, when o
// says it is still owed one, landed, reported or a retired pass
// (copyCleanupOwed), with the terminal cleanupTerminal reads off o, and logs
// what the decision answered, a retired pass's on logRetiredCleanup's terms.
// An ask under a Dead terminal is counted in landedTried once the decision
// has answered, whatever it answered, so the pass's next ask of the same
// terminal waits out the spacing; a terminal not Dead is no ask the
// decision could have granted, and is not counted.
func (s *Service) askLandedCleanup(ctx context.Context, o taskstate.Observed) {
	if !copyCleanupOwed(o) {
		return
	}
	id := o.Snapshot.Task.ID
	if s.cleaner == nil {
		s.log.Printf("the isolated copy of task %s was not cleaned up: this service was built without a cleanup decision", id)
		return
	}
	terminal := cleanupTerminal(o)
	done, err := s.cleaner.CleanUp(ctx, tasks.TaskCopy{Task: id, Terminal: terminal})
	if terminal == taskstate.Dead {
		s.markLandedTried(id, latestRef(o.Snapshot.Attempts), time.Now())
	}
	if retiredCleanupOwed(o) {
		s.logRetiredCleanup(ctx, id, done, err)
		return
	}
	s.logCleanup(ctx, done, err)
}

// landedFromPass is the supervisor pass's trigger, over the resolutions the
// pass already read, on the file comment's terms: for each task in the
// terminal tier, it ends the worker this service still holds for the latest
// attempt, and for each one still owed a cleanup, landed, a reported
// investigation or a retired housekeeping pass, whose terminal reads Dead
// (cleanupTerminal) under a reference whose ask is due (landedAskDue), it
// asks the decision. Neither a reported investigation's worker nor a
// retired pass's is ended here. The work
// goes to a goroutine registered through startWork under the task's slot;
// a slot already taken, or a service giving up its home, skips the task
// until the next pass. A service built by hand without a cleaner asks
// nothing; Open never builds one.
func (s *Service) landedFromPass(observed []taskstate.Observed) {
	if s.cleaner == nil {
		return
	}
	for _, o := range observed {
		landed := landedEnds(o.State)
		if !landed && !copyCleanupOwed(o) {
			continue
		}
		id := o.Snapshot.Task.ID
		ref := latestRef(o.Snapshot.Attempts)
		held := landed && ref != "" && s.holdsSession(id, ref)
		owed := copyCleanupOwed(o) && cleanupTerminal(o) == taskstate.Dead && s.landedAskDue(id, ref, time.Now())
		if !held && !owed {
			continue
		}
		release, err := s.claimSteer(id)
		if err != nil {
			continue
		}
		if !s.startWork() {
			release()
			return
		}
		go func() {
			defer s.work.Done()
			defer release()
			if held {
				s.endLandedWorker(s.stopCtx, id)
			}
			s.cleanUpLandedTask(s.stopCtx, id)
		}()
	}
}

// holdsSession reports whether this service holds the session ref names
// for the task.
func (s *Service) holdsSession(id, ref string) bool {
	_, held, ok := s.sessionOf(id)
	return ok && held.Reference().String() == ref
}

// landedAskSpacing is how long the pass waits after the cleanup decision
// answered for a task under a Dead terminal before it asks again under the
// same terminal. Five minutes keeps a refused copy's cost to the upstream
// and the code host at one read of each per five minutes, the pace
// mergeReadInterval keeps a passed run's pull request read at, and it is
// short beside how long a person takes to notice a kept copy. It is chosen,
// not measured.
const landedAskSpacing = 5 * time.Minute

// landedAsks is the most times the cleanup decision is asked for one task
// under one Dead terminal in a service's life, counting every ask made
// through askLandedCleanup. Six, at landedAskSpacing, is about half an hour
// of asking, which outlasts a code host that is slow to report a merge or an
// upstream that was briefly unreachable, and then stops asking about work
// that reached no upstream. It is chosen, not measured.
const landedAsks = 6

// landedSpacingOf is the spacing a service asks again on: the one a test
// set, or landedAskSpacing.
func landedSpacingOf(set time.Duration) time.Duration {
	if set > 0 {
		return set
	}
	return landedAskSpacing
}

// landedAsk is what this service remembers about asking the cleanup
// decision for one task: the session reference whose terminal read Dead,
// how many times it was asked under it, and when it last answered.
type landedAsk struct {
	ref  string
	asks int
	last time.Time
}

// markLandedTried counts one ask of the cleanup decision for the task with
// its terminal read Dead under ref, answered at at. An ask under another
// reference than the one remembered begins the count again, because a new
// attempt's terminal is a terminal nothing has asked under yet.
func (s *Service) markLandedTried(id, ref string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.landedTried == nil {
		s.landedTried = make(map[string]landedAsk)
	}
	tried := s.landedTried[id]
	if tried.ref != ref {
		tried = landedAsk{ref: ref}
	}
	tried.asks++
	tried.last = at
	s.landedTried[id] = tried
}

// landedAskDue reports whether the pass may ask the cleanup decision for
// the task with its terminal read Dead under ref, at now: it was never asked
// under ref in this service's life, or it was asked fewer than landedAsks
// times and the last answer is landedSpacing old.
func (s *Service) landedAskDue(id, ref string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	tried, ok := s.landedTried[id]
	if !ok || tried.ref != ref {
		return true
	}
	return tried.asks < landedAsks && now.Sub(tried.last) >= s.landedSpacing
}

// latestRef is the session reference of the latest of attempts, or the
// empty string when there is none or it recorded no session.
func latestRef(attempts []store.TaskAttempt) string {
	if n := len(attempts); n > 0 {
		ref, _ := attempts[n-1].SessionRef.Get()
		return ref
	}
	return ""
}
