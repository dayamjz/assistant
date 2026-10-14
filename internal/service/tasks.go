package service

import (
	"context"
	"fmt"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/vcs"
)

// newQueue builds internal/tasks' queue over this service's store.
//
// The two things that package cannot answer for itself are this service's:
// the per-branch exclusion is holdBranch, the same gate a run start takes, so
// an intake deciding a branch's disposition and a push deciding its run do not
// interleave; and whether a branch is already in the gate is read off the
// gate repository the run's copies are cut from, and where that gate stands
// is gatePathOf, the same answer a dispatch cuts a copy against. The state
// reader is the one the task surface reads through, advancing fact included.
//
// The queue's intake and promotion are called from coordinator.go's
// task.create and task.promote, the coordinator-only service methods PRD
// section 9 puts beside its command table, and from nowhere else; no verb
// creates or promotes a task. This service also calls Release, from the
// sites releaseQueued names.
func (s *Service) newQueue() (*tasks.Queue, error) {
	return tasks.New(tasks.Deps{
		Home:   s.home,
		Store:  s.store,
		Reader: s.tasks(),
		Exclude: func(ctx context.Context, repository, branch string) (func(), error) {
			return s.holdBranch(ctx, branchKey{repository: repository, branch: branch})
		},
		BranchInGate: s.branchInGate,
		GatePath:     s.gatePathOf,
	})
}

// branchInGate reports whether the repository's gate already carries the
// branch, read from the gate repository itself. A repository bound to no gate
// is refused rather than read as having no branches: a task of it could not
// be cut a copy either, and the refusal names the command that binds one.
func (s *Service) branchInGate(ctx context.Context, repository store.Repository, branch string) (bool, error) {
	binding, err := s.store.GateBinding(ctx, repository.WorkingPath)
	if err != nil {
		return false, fmt.Errorf("service: %s is not bound to a gate, so a task cannot be given a branch of it; "+
			"run assistant init there: %w", repository.WorkingPath, err)
	}
	repositoryPath, err := gate.RepositoryFor(s.home.Root(), binding.GateID)
	if err != nil {
		return false, fmt.Errorf("service: locating the gate repository of %s: %w", repository.ID, err)
	}
	bare, err := vcs.OpenBare(ctx, repositoryPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return false, fmt.Errorf("service: opening the gate repository of %s: %w", repository.ID, err)
	}
	refs, err := bare.ListRefs(ctx, "refs/heads/"+branch)
	if err != nil {
		return false, fmt.Errorf("service: reading the branches of the gate repository of %s: %w", repository.ID, err)
	}
	for _, ref := range refs {
		if ref.Name == "refs/heads/"+branch {
			return true, nil
		}
	}
	return false, nil
}

// releaseQueued dispatches every queued task whose blocker has cleared, and
// logs what came of each. It is called when a run finishes and when the
// service opens, which are the moments this build knows a blocker can have
// cleared: a task resolves Failed from a run that failed or was ended,
// Reported from an entry a worker appended while no service watched, and
// Landed from the merge record the checks-hold closure writes. A
// run finishes at four sites in this package and three call it: settle, for
// a segment that ended the run; cancel, for an ending through the protocol;
// and the checks-hold closure, for a run its pull request's state ended.
// The merge record is written by internal/runs' PassOnMerge, which only
// settle and that closure reach, so every record is followed by a call here
// and a blocker Landed is observed by it. The
// fourth, claimPush terminating the run a new push supersedes, does not: it
// ends that run only to create the branch's next one under the same
// exclusion, so the branch stays live and the task the run was attributed to
// resolves from the replacement the moment it exists. A release between the
// two writes would read the gap as the blocker having failed and dispatch a
// successor onto a branch its worker is still pushing to. The one push that
// does call it is the one whose replacement run could not be created after
// the displaced run was terminated: the branch is then idle, and the call
// runs after the exclusion is given back. The create fails in driverFor as
// well as in the store, which is how stranded_internal_test.go reaches the
// path: a hand-built service whose driver does the terminate and whose
// catalog resolves no agent for the create. The cleanup record, which moves
// a Landed task to Complete, is written by internal/tasks' CleanUp, which
// cleanup.go calls at the same two finishes before this; no call here
// follows it, because a blocker Landed has already cleared and the record's
// writer owes this call nothing for the release.
//
// It reports to the log and not to a caller, on the terms reclaimCopy gives:
// the run has settled by the time this runs, so there is no answer a failure
// here could change, and a task left queued is released by the next call.
// Each dispatched entry it appends is a wake in its own right, which is how
// the coordinator learns of it.
func (s *Service) releaseQueued(ctx context.Context) {
	outcomes, err := s.queue.Release(ctx)
	for _, o := range outcomes {
		switch o.Result {
		case tasks.Released:
			s.log.Printf("task %s was released: its blocker %s resolved %s", o.Task, o.Blocker, o.BlockerState)
		case tasks.Refused:
			s.log.Printf("task %s was not released: %v", o.Task, o.Err)
		}
	}
	if err != nil {
		s.log.Printf("releasing queued tasks: %v", err)
	}
	// A released task resolves Dispatched, and a task intake recorded as
	// independent did from the start; dispatch.go cuts and proves each one's
	// copy, once per service, from here.
	s.dispatchDue(ctx)
}
