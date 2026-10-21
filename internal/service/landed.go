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
// A reported investigation keeps its worker for promotion_window after its
// report is stored, PRD section 6's kept work, so that a promotion in the
// same sitting continues the worker that wrote the report. The window is
// counted from the report row's own time, ReportStoredAt in the snapshot the
// pass already read, and never from a timer or from when this service
// learned of the report, so a restart neither restarts nor forgets it (P8:
// the record, not the history's tail). At the first pass that reads the
// window passed, promotionWindowPassed, the latest attempt's worker is
// ended: through the session this service holds, or, for a worker an
// earlier service launched, by the verified identity its recorded session
// reference carries (endReportedWorker); nothing about the copy is decided: an
// investigation's copy is kept whether its worker was ended by the window,
// by a stop, or exited on its own, because a later promotion relaunches a
// worker in it (coordinator.go). Its cleanup is owed only once the task
// leaves the investigation lifecycle through its findings relayed record,
// below. Nothing here reads the report's text: whether it is stored is the
// row's to say, and what it says is the relaunch's starting point.
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
// log's tail. A relayed investigation is not promotable, so nothing here
// has to keep its worker or its copy for a promotion.
//
// # Cadence
//
// The landing sites run once per landing record. The stop path runs once
// per stop, which is a person's or a coordinator's request. The pass is the
// one recurring trigger, at the supervisor's beacon interval, so a window's
// end is acted on up to one interval late, and it bounds
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
//   - A reported investigation's worker that an earlier service launched is
//     ended by the window through its recorded identity, unlike a landed
//     task's (the first gap); one whose reference cannot be verified is
//     reported in the log once per reference per service life and left
//     running.
//   - A copy kept after the window has no kept-work entry: endReportedWorker
//     writes none, so tasks.KeptPromotionWindow has no producer yet and the
//     copy is not surfaced, and no open entry holds dispatch in this build
//     anyway (#411's second part); a reported investigation that is never
//     promoted or relayed keeps its copy indefinitely meanwhile.
//   - A reported investigation whose report opened a finding hold resolves
//     Held, which the supervisor watches the terminal of, so the window's
//     end raises a terminal-gone wake for it.

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

// reportedCleanupOwed reports whether o is an investigation whose copy the
// cleanup decision is worth asking for: still an investigation, its copy not
// recorded removed, resolved Complete from its findings relayed record
// (relayedEnds), and every hold open on the task one a report's ask finding
// registered (tasks.HoldKey's space). A stored report alone is not enough,
// as it was before promotion_window: the copy of a reported investigation
// is kept for a promotion, on this file's terms. A finding hold does not
// keep the copy, because PRD section 11 has the hold survive cleanup; any
// other open hold does, because a stop's or a relaunch bound's retry would
// relaunch in this copy. It reads what the resolver recorded and the holds
// the snapshot carries, never the log's tail (P8), and decides nothing
// about the copy: internal/tasks verifies the report inside the decision.
func reportedCleanupOwed(o taskstate.Observed) bool {
	if o.Snapshot.Task.Shape != store.TaskInvestigation || o.State.Basis.CleanupRemoved || !relayedEnds(o.State) {
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

// promotionWindowPassed reports whether o is a reported investigation whose
// promotion_window has passed at now: still an investigation, not in the
// terminal tier (a relayed one's worker is ended as a finished task's), and
// its report row's own time at least window before now. It reads the
// snapshot's ReportStoredAt and nothing remembered, which is what keeps the
// window across a restart.
func promotionWindowPassed(o taskstate.Observed, now time.Time, window time.Duration) bool {
	stored, ok := o.Snapshot.ReportStoredAt.Get()
	return ok && o.Snapshot.Task.Shape == store.TaskInvestigation && !landedEnds(o.State) && !now.Before(stored.Add(window))
}

// relayedEnds reports whether a resolution is an investigation's Complete
// from internal/taskstate's terminal tier, read from its findings relayed
// record (Basis.Relayed), rather than a delivery task's.
func relayedEnds(r taskstate.Resolution) bool {
	return landedEnds(r) && r.State == taskstate.Complete && r.Basis.Relayed
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
	done, err := s.cleaner.CleanUp(ctx, tasks.TaskCopy{Task: id, Terminal: o.State.Basis.Process})
	s.logCleanup(ctx, done, err)
}

// landedFromPass is the supervisor pass's trigger, over the resolutions the
// pass already read, on the file comment's terms: for each task in the
// terminal tier, and for each reported investigation whose promotion window
// has passed, it ends the worker this service still holds for the latest
// attempt, and for each one still owed a cleanup, landed or a relayed
// investigation, whose terminal the pass read Dead under a reference not
// yet tried, it asks the decision. The work goes to a goroutine registered
// through startWork under the task's slot, and an expired window is read
// again there, so a promotion that took the slot first is not undone; a
// slot already taken, or a service giving up its home, skips the task
// until the next pass. A service built by hand without a cleaner asks
// nothing; Open never builds one.
func (s *Service) landedFromPass(observed []taskstate.Observed) {
	if s.cleaner == nil {
		return
	}
	now := time.Now()
	for _, o := range observed {
		landed := landedEnds(o.State)
		expired := promotionWindowPassed(o, now, s.cfg.PromotionWindow)
		if !landed && !expired {
			continue
		}
		id := o.Snapshot.Task.ID
		ref := latestRef(o.Snapshot.Attempts)
		held := ref != "" && s.holdsSession(id, ref)
		survivor := expired && !held && ref != "" && o.State.Basis.Process == taskstate.Unobserved && !s.windowWasTried(id, ref)
		owed := copyCleanupOwed(o) && o.State.Basis.Process == taskstate.Dead && !s.landedWasTried(id, ref)
		if !held && !survivor && !owed {
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
			switch {
			case held && landed:
				s.endLandedWorker(s.stopCtx, id)
			case held || survivor:
				s.endReportedWorker(s.stopCtx, id)
			}
			s.cleanUpLandedTask(s.stopCtx, id)
		}()
	}
}

// endReportedWorker ends the worker of a reported investigation's latest
// attempt once its promotion window has passed, read again under the task's
// slot from a fresh read of the task, so a task the coordinator promoted
// since the pass read it keeps its worker. It ends the worker through
// steer.go's endWorker, never by a pattern: through the session this
// service holds, or, for a worker an earlier service launched that the
// pass read Unobserved, through agents.EndGroup on the identity the
// attempt's recorded session reference carries, verified before anything
// is signalled. A reference that cannot be verified (another home's, one
// with no start time, one that does not decode) or a group that does not
// end is reported in the log and left running, asked once per reference in
// this service's life (windowTried). The copy is kept either way.
func (s *Service) endReportedWorker(ctx context.Context, id string) {
	o, err := s.tasks().ObserveTask(ctx, id)
	if err != nil {
		s.log.Printf("task %s: its promotion window could not be read again, so its worker was not ended: %v", id, err)
		return
	}
	if !promotionWindowPassed(o, time.Now(), s.cfg.PromotionWindow) {
		return
	}
	ref := latestRef(o.Snapshot.Attempts)
	if ref == "" {
		return
	}
	held := s.holdsSession(id, ref)
	if !held {
		s.markWindowTried(id, ref)
	}
	ended, err := s.endWorker(id, ref)
	if err != nil {
		s.log.Printf("task %s is reported and its promotion window of %s has passed, and its worker (session %s) "+
			"was not ended and is left running: %v", id, s.cfg.PromotionWindow, ref, err)
		return
	}
	s.log.Printf("task %s is reported and its promotion window of %s has passed, so its worker (session %s) was ended: "+
		"%s; its copy is kept for a promotion", id, s.cfg.PromotionWindow, ref, ended)
}

// markWindowTried records that the end of an unheld reported worker under
// ref was asked for after the promotion window.
func (s *Service) markWindowTried(id, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.windowTried == nil {
		s.windowTried = make(map[string]string)
	}
	s.windowTried[id] = ref
}

// windowWasTried reports whether markWindowTried recorded ref for the task.
func (s *Service) windowWasTried(id, ref string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	tried, ok := s.windowTried[id]
	return ok && tried == ref
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
