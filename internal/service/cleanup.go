package service

import (
	"context"
	"errors"
	"os"

	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// Where the cleanup decision is called from, and with what.
//
// PRD principle P12 is internal/tasks' decision and this service is its
// caller, at three sites and through no verb: the table PRD section 9
// fixes has none, and the journey harness records the consequence as
// package reach. The one protocol method that reaches it is the
// coordinator's task.stop, and only for a task already Landed, on
// landed.go's terms; it asks the decision and does not widen it.
//
// A run's copy is reclaimed when the run finishes, in settle and in the
// checks-hold closure, through reclaimCopy, after the move that finished it
// has swept it on runsweep.go's terms; copy.go says what that decides and
// what a refusal there becomes.
//
// A delivery task's copy is cleaned up when a merge record is written for a
// run attributed to it, which is the same two sites, after the run's own
// copy: a merge record is what makes the task Landed, and Landed is the
// state PRD section 6's lifecycle has cleanup move to Complete. The task is
// found the way the resolver finds it, through internal/taskstate's
// Basis.LandedRun, so the run that landed it and the task that is cleaned
// up are one attribution and not two. A landing site does not ask the
// decision directly: it settles the task through landed.go, which ends the
// worker this service holds for it first, and which also asks the decision
// again when a landed task's terminal is next observed Dead, from the
// supervisor's pass and from task.stop.
//
// An investigation task's copy is cleaned up once its report is stored. The
// report arrives from the task's worker over its route, as task.event's
// report kind, which workers.go hands to route.go's fileReport, and
// storeTaskReport stores it through internal/tasks and then asks for the
// cleanup. The worker that filed it is usually still running then, so that
// first ask is refused with the live terminal and keeps the copy; landed.go's
// pass asks again once the terminal is observed Dead, on the terms it states
// for a reported investigation, and that is the ask that removes the copy.
//
// Both task-copy sites hand internal/tasks the terminal's liveness as the
// service's task reader resolved it, Basis.Process of the resolution the
// site reads the task through, so the liveness a cleanup decides on is the
// one producer's answer, the provider's probe through the service's
// Terminals, and never a second read: a site that read the launched map or
// probed on its own would be a second owner of the fact a person sees on
// the task surface. internal/tasks examines a task copy only once that
// liveness is Dead, so a landed delivery task whose worker still reads
// running refuses with ReasonTerminalLive and keeps its copy, one whose
// terminal could not be observed, a worker an earlier service launched
// among them, refuses with ReasonTerminalUnobserved, and one whose worker
// is gone is examined and removed on P12's proof. What the probe does
// not close is the window after it: the liveness is as of the read, and a
// worker that starts writing in the copy between the read and the removal
// is caught only by internal/tasks' inventory re-read, which that package
// states the reach of.
//
// A refusal is a typed result internal/tasks returns, and these sites have
// no caller to return it to, so it goes to the log. For a run's copy the
// decision recorded it as well, on the run's cleanup record, and report
// composes that into the run's answer; a task copy's refusal is recorded
// only when it is kept work, as the entry below.
//
// # Kept work
//
// A refusal internal/tasks reports Unlanded is a copy kept because its work
// has not landed, and PRD principle P15 has that surface at once rather than
// stay on disk as a log line. Every site above hands such a refusal to
// keepWork, the one place a kept copy becomes a kept work entry
// (store.KeptWork, written by internal/tasks' Keep); recovery reaches it too,
// through reclaimAtRecovery, for a finished run whose copy still stands. What
// surfaces the entry is the supervisor loop, as a kept-work wake about the
// task the entry's owner is or is attributed to, and the status and catch-up
// reads, which list every open entry; supervise.go and catchup.go own
// those. An entry closes without an answer only when its copy is removed
// on a P12 proof: internal/store's MarkCleanupRemoved and
// MarkRunCleanupRemoved close the owner's open entries in the transaction
// that marks the removal, so a copy whose work later landed (recovery's
// reclaim among the paths) leaves no entry listed for a copy that is gone.
// This build records and surfaces kept work and does not yet hold
// dispatch: an open entry does not stop a new task in its project from
// dispatching, and nothing here acts on an answer. Both are #411's second
// part.

// keepWork records the kept work entry for a kept copy and logs what came of
// it. It is the one call a site makes for a copy kept because its work has
// not landed, and the call meant for an investigation's copy kept past
// promotion_window, with tasks.KeptPromotionWindow as the reason, which
// #438 adds. A write
// that failed is logged and refuses nothing, because the copy is kept either
// way and the next cleanup of it asks again; an entry already recorded for
// the copy and its commits is logged as such and written again nowhere.
func (s *Service) keepWork(ctx context.Context, kept tasks.Kept) {
	owner := "task " + kept.Task
	if kept.Run != "" {
		owner = "run " + kept.Run
	}
	got, err := s.cleaner.Keep(ctx, kept)
	switch {
	case err != nil:
		s.log.Printf("the isolated copy of %s is kept and its kept work entry could not be recorded: %v", owner, err)
	case got.Recorded:
		s.log.Printf("the isolated copy of %s is kept work entry %d (%s)", owner, got.Entry.ID, got.Entry.Reason)
	default:
		s.log.Printf("the isolated copy of %s is already kept work entry %d", owner, got.Entry.ID)
	}
}

// reclaimAtRecovery asks reclaimed of every run among records that the
// record shows finished and whose isolated copy still stands, on
// sweepFinishedAtRecovery's terms for which runs those are, on one goroutine
// registered under work, because each ask reads the upstream for its proofs
// and a home holding several kept copies should come back serving first. A
// run among records that was unfinished is passed over: the move that
// finished it during recovery reclaimed it then. A copy whose run has no
// record at all is not looked for, which is a gap this names.
func (s *Service) reclaimAtRecovery(records []store.Run) {
	var finished []store.Run
	for _, record := range records {
		if !record.Status.Recognized() || !runs.Finished(record.Status) {
			continue
		}
		if _, err := os.Lstat(s.home.Worktree(record.RepositoryID, record.ID)); errors.Is(err, os.ErrNotExist) {
			continue
		}
		finished = append(finished, record)
	}
	if len(finished) == 0 || !s.startWork() {
		return
	}
	go func() {
		defer s.work.Done()
		for _, record := range finished {
			if s.stopCtx.Err() != nil {
				return
			}
			s.reclaimed(s.stopCtx, record)
		}
	}()
}

// reclaimed calls reclaimCopy and logs what it could not do, on the terms
// copy.go states, and hands a copy kept for its work to keepWork.
func (s *Service) reclaimed(ctx context.Context, record store.Run) {
	err := s.reclaimCopy(ctx, record)
	var refused *tasks.CleanupRefusal
	switch {
	case err == nil:
	case errors.As(err, &refused):
		s.log.Printf("the isolated copy of run %s is left standing (%s): %s", record.ID, refused.Reason, refused.Detail)
		if kept, ok := tasks.KeptOf(refused); ok {
			s.keepWork(ctx, kept)
		}
	default:
		s.log.Printf("the isolated copy of run %s was not reclaimed: %v", record.ID, err)
	}
}

// cleanUpLanded settles every delivery task the run's merge record landed,
// on landed.go's terms: it ends the worker this service holds for it and
// then asks the cleanup decision, and logs what came of each. A run with no
// merge record reading merged landed nothing and this returns at once.
func (s *Service) cleanUpLanded(ctx context.Context, record store.Run) {
	merge, ended, err := s.store.MergeRecord(ctx, record.ID)
	if err != nil {
		s.log.Printf("cleaning up after run %s: its merge record could not be read: %v", record.ID, err)
		return
	}
	if !ended || merge.End != store.PullRequestMerged {
		return
	}
	resolved, err := s.tasks().All(ctx)
	if err != nil {
		s.log.Printf("cleaning up after run %s: the tasks could not be read: %v", record.ID, err)
		return
	}
	for _, r := range resolved {
		if r.State.Basis.LandedRun != record.ID || r.Record.Shape != store.TaskDelivery {
			continue
		}
		s.settleLanded(ctx, r.Record.ID)
	}
}

// storeTaskReport stores an investigation task's report through
// internal/tasks, publishes the holds it registered and the history entry
// that claims Reported at internal/taskstate's event tier, asks for its
// copy's cleanup, and then releases what is queued behind the task, through
// releaseInBackground. It returns what the store refused; the cleanup's
// answer goes to the log on this file's terms, and a task whose terminal
// could not be read after the report was stored is a cleanup not asked for
// rather than one asked with a stated liveness.
//
// The release follows the stored report and not the history entry or a log
// line (P8): it is asked only once StoreReport has returned the row, which
// is the record internal/taskstate's report tier resolves Reported from, and
// internal/tasks' Release decides from that resolution whether the blocker
// cleared. A report whose asks opened holds leaves the task Held, which does
// not clear, so nothing queued behind it is released by this call;
// releaseQueued names when such a successor is released instead.
func (s *Service) storeTaskReport(ctx context.Context, taskID, raw string) (tasks.StoredReport, error) {
	stored, err := s.cleaner.StoreReport(ctx, taskID, raw)
	if err != nil {
		return tasks.StoredReport{}, err
	}
	// The report's holds are registered in the store's transaction, and
	// each is published as it was returned: a hold already registered under
	// its key comes back as it stands, resolved or not, and that is what
	// travels.
	for _, h := range stored.Holds {
		s.publishHoldState(h)
	}
	s.publishTaskEvent(stored.Event)
	resolved, err := s.tasks().Resolved(ctx, taskID)
	if err != nil {
		s.log.Printf("the isolated copy of task %s was not cleaned up: its terminal could not be read: %v", taskID, err)
		s.releaseInBackground()
		return stored, nil
	}
	done, err := s.cleaner.CleanUp(ctx, tasks.TaskCopy{Task: taskID, Terminal: resolved.State.Basis.Process})
	s.logCleanup(ctx, done, err)
	s.releaseInBackground()
	return stored, nil
}

// logCleanup says what a task copy's cleanup did or why it did not, and
// hands a copy kept for its work to keepWork.
func (s *Service) logCleanup(ctx context.Context, done tasks.Cleanup, err error) {
	var refused *tasks.CleanupRefusal
	switch {
	case err == nil:
		s.log.Printf("the isolated copy of task %s: %s on %s", done.Task, done.Result, done.Proof)
	case errors.As(err, &refused):
		s.log.Printf("the isolated copy of task %s is left standing (%s): %s", refused.Task, refused.Reason, refused.Detail)
		if kept, ok := tasks.KeptOf(refused); ok {
			s.keepWork(ctx, kept)
		}
	default:
		s.log.Printf("the isolated copy of task %s was not cleaned up: %v", done.Task, err)
	}
}
