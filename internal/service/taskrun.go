package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// An answer given a task, reaching the run the task produced.
//
// PRD section 9 has answering a hold take a task as well as a run. A hold
// the task raised itself is taskholds.go's. This file is the other half: a
// task with no hold of its own open has its answer given to the hold on its
// current run, so a coordinator answers a task's run without a checkout of
// the task's branch. The task's own copy is not one: it is bound to no gate,
// and binding it would undo the isolation P11 asserts at launch.
//
// # Which run
//
// The task's current run is activeRun's answer for the task's repository and
// branch: the newest run of that branch that has not finished. That is the
// same lookup status makes for a working copy standing on the branch, so
// --task reaches exactly the run --answer reaches from a checkout of it, and
// a run a later push superseded is not it, because superseding ends a run
// in its record. It is not internal/store's attribution of runs to a task,
// which reads only runs created since the task's latest attempt began: that
// answers which runs speak for a task's state, and the question here is
// which run a person standing on the branch would be answering.
//
// # Which hold comes first
//
// A task with a hold of its own open is refused here, typed, rather than
// having its run answered past that hold. The hold naming the task is the
// one an answer given the task closes, as taskholds.go has always answered
// it, and PRD section 6 puts it above every run in the task's state, so a
// person reading the task is told about it first. The command surface sends
// an answer to the task's own hold when it reads one open, so this refusal
// is what a request meets when one opened after that read, or when it
// carries a fix round, which only a run takes. Either way nothing is
// answered, and the refusal says to answer the task's own decision first.
//
// # Who may ask
//
// A task's worker is refused, from the caller's credentials, on
// MethodTaskRespond's terms: no worker answers a hold, its own task's
// included. Selecting a run by naming it is unchanged by this file.
//
// # Residual gaps
//
//   - The run is selected and then answered in two reads, not one
//     transaction. A run that finished or was superseded between them is
//     refused by respond, which reads it again; a newer run that began in
//     between is not the one answered, and the answer reports which run it
//     moved.
//   - Two tasks on one branch at once select the same run, which is
//     internal/store's attribution gap restated: the intake's disposition is
//     what keeps two live tasks off one branch, and nothing here re-checks
//     it.

// respondTo answers a run's decision, the run named or selected through a
// task, on RespondRequest's terms. A request naming both is
// ipc.ErrInvalidRequest.
func (s *Service) respondTo(ctx context.Context, req ipc.Request, r machine.RespondRequest) (machine.Run, error) {
	if r.Task == "" {
		return s.respond(ctx, r)
	}
	if r.Run != "" {
		return machine.Run{}, fmt.Errorf("%w: an answer names run %s and task %s, and selects its run one way", ipc.ErrInvalidRequest, r.Run, r.Task)
	}
	if err := s.refuseWorker(req); err != nil {
		return machine.Run{}, err
	}
	run, err := s.taskRun(ctx, r.Task)
	if err != nil {
		return machine.Run{}, err
	}
	r.Run, r.Task = run.ID, ""
	return s.respond(ctx, r)
}

// taskRun is the task's current run, held at a decision, or a typed
// refusal. A task with a hold of its own open, a task naming no branch, a
// branch with no unfinished run, and a run not waiting on an answer are each
// ipc.ErrRefused, and an unknown task is ipc.ErrRefused too; each refusal
// carries the internal/machine row a person reads about it.
func (s *Service) taskRun(ctx context.Context, id string) (store.Run, error) {
	if strings.TrimSpace(id) == "" {
		return store.Run{}, fmt.Errorf("%w: an answer given a task names no task", ipc.ErrInvalidRequest)
	}
	task, err := s.store.Task(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Run{}, fmt.Errorf("%w: no task %s is recorded. %s", ipc.ErrRefused, id, machine.PhraseTaskNothingWaiting)
	}
	if err != nil {
		return store.Run{}, err
	}
	own, err := s.oldestTaskHold(ctx, id)
	switch {
	case err == nil:
		return store.Run{}, fmt.Errorf("%w: task %s waits on its own hold %s, which an answer given the task closes before its run's. %s",
			ipc.ErrRefused, id, own.Key, machine.PhraseTaskOwnHoldFirst)
	case !errors.Is(err, errNoTaskHold):
		return store.Run{}, err
	}
	repository, named := task.RepositoryID.Get()
	branch, onBranch := task.Branch.Get()
	if !named || !onBranch {
		return store.Run{}, fmt.Errorf("%w: task %s names no branch, so no run of it can be waiting. %s",
			ipc.ErrRefused, id, machine.PhraseTaskNothingWaiting)
	}
	active, found, err := s.activeRun(ctx, repository, branch)
	if err != nil {
		return store.Run{}, err
	}
	if !found {
		return store.Run{}, fmt.Errorf("%w: task %s has no hold open and its branch %s has no run under way. %s",
			ipc.ErrRefused, id, branch, machine.PhraseTaskNothingWaiting)
	}
	record, err := s.reconcile(ctx, active.ID)
	if err != nil {
		return store.Run{}, err
	}
	if record.Status != store.RunHeld {
		return store.Run{}, fmt.Errorf("%w: task %s has no hold open and its run %s is %s, not waiting on an answer. %s",
			ipc.ErrRefused, id, record.ID, record.Status, machine.PhraseTaskNothingWaiting)
	}
	return record, nil
}
