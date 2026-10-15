package service

import (
	"context"
	"strings"

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
// cleanup.go's landing sites run once, when the merge record or the local
// landing record is written, and a terminal that did not read Dead then,
// because the worker was one this service could not end or had not yet
// exited, left the copy standing. The decision is therefore asked whenever
// the task's terminal is observed Dead, from three places, each handing internal/tasks the liveness a resolution read
// through the service's Terminals, on cleanup.go's terms:
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
// # Cadence
//
// The landing sites run once per landing record. The stop path runs once
// per stop, which is a person's or a coordinator's request. The pass is the
// one recurring trigger, at the supervisor's beacon interval, and it bounds
// itself: a worker is ended at most once, because ending it forgets the
// handle the next pass would find; and the cleanup is asked at most once per
// task and session reference observed Dead in this service's life, recorded
// in landedTried, because a refusal the proof answered (work that reached
// no upstream, an unreadable remote) answers the same until something
// outside this service changes, and asking at every interval would fetch
// the upstream at every interval for nothing. A restart forgets the memory,
// so each service start asks once more; that and a stop are how a refused
// cleanup is asked again. The work runs on a goroutine registered through
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
//   - A refused cleanup is asked again only at a stop or a restart; nothing
//     watches the upstream for the change that would let the proof pass.
//   - What the decision answered goes to the log, as at every task-copy
//     site, and travels on no answer: task.stop's answer says no hold was
//     registered and not whether the copy was removed.
//   - A reported investigation's worker that neither exits nor is stopped
//     keeps its copy for as long as it runs; nothing here ends it.
//   - A removed investigation copy cannot be promoted, which internal/tasks
//     refuses as a missing copy, so a coordinator that means to promote a
//     reported investigation does so before stopping its worker.

// landedEnds reports whether a resolution is in internal/taskstate's terminal
// tier, the one that says the task's work is finished: Landed, or Complete
// on top of it. Such a task is owed no relaunch and keeps no worker.
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
// entry the resolver read (Basis.EventKind), and every hold open on the
// task one a report's ask finding registered (tasks.HoldKey's space). The
// state is not read: internal/taskstate resolves such a task Running while
// its worker runs, Unknown once its terminal is Dead, and Reported only
// while the terminal is unobserved, so the entry the resolver recorded is
// the reading that says the report was filed. A finding hold does not keep
// the copy, because PRD section 11 has the hold survive cleanup; any other
// open hold does, because a stop's or a relaunch bound's retry would
// relaunch in this copy. It reads what the resolver recorded and the holds
// the snapshot carries, never the log's tail (P8), and decides nothing
// about the copy: internal/tasks verifies the report inside the decision.
func reportedCleanupOwed(o taskstate.Observed) bool {
	if o.Snapshot.Task.Shape != store.TaskInvestigation || o.State.Basis.CleanupRemoved ||
		o.State.Basis.EventKind != store.ReportReadyEventKind {
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

// copyCleanupOwed reports whether the cleanup decision is still owed for
// o's copy, as a landed task's or as a reported investigation's.
func copyCleanupOwed(o taskstate.Observed) bool {
	return landedCleanupOwed(o.State) || reportedCleanupOwed(o)
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
		s.log.Printf("task %s landed and its latest attempt could not be read, so its worker was not ended: %v", id, err)
		return
	}
	ref, launched := latest.SessionRef.Get()
	if !ok || !launched {
		return
	}
	ended, err := s.endHeldWorker(id, ref)
	switch {
	case err != nil:
		s.log.Printf("task %s landed and its worker (session %s) was not ended: %v", id, ref, err)
	case ended:
		s.log.Printf("task %s landed, so this service ended its worker (session %s) through the session it holds", id, ref)
	}
}

// cleanUpLandedTask reads the task through the service's reader and, while
// it is still owed a cleanup, asks the decision with the terminal that read
// resolved, remembering a Dead terminal's reference in landedTried so the
// pass does not ask again for it. It logs what the decision answered.
func (s *Service) cleanUpLandedTask(ctx context.Context, id string) {
	o, err := s.tasks().ObserveTask(ctx, id)
	if err != nil {
		s.log.Printf("the isolated copy of task %s was not cleaned up: its terminal could not be read: %v", id, err)
		return
	}
	s.askLandedCleanup(ctx, o)
}

// askLandedCleanup asks the cleanup decision for a task read in o, when o
// says it is still owed one, landed or reported (copyCleanupOwed), with the
// terminal o resolved, and logs what the decision answered. A Dead terminal's reference is remembered in
// landedTried first.
func (s *Service) askLandedCleanup(ctx context.Context, o taskstate.Observed) {
	if !copyCleanupOwed(o) {
		return
	}
	id := o.Snapshot.Task.ID
	if s.cleaner == nil {
		s.log.Printf("the isolated copy of task %s was not cleaned up: this service was built without a cleanup decision", id)
		return
	}
	if o.State.Basis.Process == taskstate.Dead {
		s.markLandedTried(id, latestRef(o.Snapshot.Attempts))
	}
	s.logCleanup(s.cleaner.CleanUp(ctx, tasks.TaskCopy{Task: id, Terminal: o.State.Basis.Process}))
}

// landedFromPass is the supervisor pass's trigger, over the resolutions the
// pass already read, on the file comment's terms: for each task in the
// terminal tier, it ends the worker this service still holds for the latest
// attempt, and for each one still owed a cleanup, landed or a reported
// investigation, whose terminal the pass read Dead under a reference not
// yet tried, it asks the decision. A reported investigation's worker is
// never ended here. The work
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
		if !landed && !reportedCleanupOwed(o) {
			continue
		}
		id := o.Snapshot.Task.ID
		ref := latestRef(o.Snapshot.Attempts)
		held := landed && ref != "" && s.holdsSession(id, ref)
		owed := copyCleanupOwed(o) && o.State.Basis.Process == taskstate.Dead && !s.landedWasTried(id, ref)
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

// markLandedTried records that the cleanup decision was asked for the task
// with its terminal read Dead under ref.
func (s *Service) markLandedTried(id, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.landedTried == nil {
		s.landedTried = make(map[string]string)
	}
	s.landedTried[id] = ref
}

// landedWasTried reports whether the cleanup decision was already asked for
// the task with its terminal read Dead under ref, in this service's life.
func (s *Service) landedWasTried(id, ref string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	tried, ok := s.landedTried[id]
	return ok && tried == ref
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
