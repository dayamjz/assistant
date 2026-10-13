package service

import (
	"context"
	"errors"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Where the cleanup decision is called from, and with what.
//
// PRD principle P12 is internal/tasks' decision and this service is its
// caller, at three sites and through no verb or protocol method: the table
// PRD section 9 fixes has none, and the journey harness records the
// consequence as package reach.
//
// A run's copy is reclaimed when the run finishes, in settle and in the
// checks-hold closure, through reclaimCopy; copy.go says what that decides
// and what a refusal there becomes.
//
// A delivery task's copy is cleaned up when a merge record is written for a
// run attributed to it, which is the same two sites, after the run's own
// copy: a merge record is what makes the task Landed, and Landed is the
// state PRD section 6's lifecycle has cleanup move to Complete. The task is
// found the way the resolver finds it, through internal/taskstate's
// Basis.LandedRun, so the run that landed it and the task that is cleaned
// up are one attribution and not two.
//
// An investigation task's copy is cleaned up when its report is stored,
// through storeTaskReport, which stores it through internal/tasks and then
// asks for the cleanup. Nothing in this build calls storeTaskReport: the
// worker that would file a report is #91's, and the method is here so the
// site exists before the producer does, which its test drives in process.
//
// Both task-copy sites pass taskstate.Unobserved as the terminal, because
// the probe is not wired into this service's reader (#91's last PR), and
// internal/tasks examines a task copy only once its terminal probed Dead.
// Every task copy therefore refuses today with ReasonTerminalUnobserved,
// which is the refusal P12 asks for over a copy a worker may still be
// writing to, and is logged here as what it is; wiring the probe is what
// turns these calls into removals, and nothing here changes when it lands.
//
// A refusal is a typed result internal/tasks returns, and these sites have
// no caller to return it to, so it goes to the log. For a run's copy the
// decision recorded it as well, on the run's cleanup record, and report
// composes that into the run's answer; a task copy's refusal is recorded
// nowhere yet, because the surface that would show it is #110's.

// reclaimed calls reclaimCopy and logs what it could not do, on the terms
// copy.go states.
func (s *Service) reclaimed(ctx context.Context, record store.Run) {
	err := s.reclaimCopy(ctx, record)
	var refused *tasks.CleanupRefusal
	switch {
	case err == nil:
	case errors.As(err, &refused):
		s.log.Printf("the isolated copy of run %s is left standing (%s): %s", record.ID, refused.Reason, refused.Detail)
	default:
		s.log.Printf("the isolated copy of run %s was not reclaimed: %v", record.ID, err)
	}
}

// cleanUpLanded cleans up the copy of every delivery task the run's merge
// record landed, and logs what came of each. A run with no merge record
// reading merged landed nothing and this returns at once.
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
		s.logCleanup(s.cleaner.CleanUp(ctx, tasks.TaskCopy{Task: r.Record.ID, Terminal: taskstate.Unobserved}))
	}
}

// storeTaskReport stores an investigation task's report through
// internal/tasks and then asks for its copy's cleanup, which is the
// investigation half of the lifecycle's Complete edge. It returns what the
// store refused; the cleanup's answer goes to the log on this file's terms.
func (s *Service) storeTaskReport(ctx context.Context, taskID, raw string) (tasks.StoredReport, error) {
	stored, err := s.cleaner.StoreReport(ctx, taskID, raw)
	if err != nil {
		return tasks.StoredReport{}, err
	}
	s.logCleanup(s.cleaner.CleanUp(ctx, tasks.TaskCopy{Task: taskID, Terminal: taskstate.Unobserved}))
	return stored, nil
}

// logCleanup says what a task copy's cleanup did or why it did not.
func (s *Service) logCleanup(done tasks.Cleanup, err error) {
	var refused *tasks.CleanupRefusal
	switch {
	case err == nil:
		s.log.Printf("the isolated copy of task %s: %s on %s", done.Task, done.Result, done.Proof)
	case errors.As(err, &refused):
		s.log.Printf("the isolated copy of task %s is left standing (%s): %s", refused.Task, refused.Reason, refused.Detail)
	default:
		s.log.Printf("the isolated copy of task %s was not cleaned up: %v", done.Task, err)
	}
}
