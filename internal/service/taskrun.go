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
// That precedence is for an answer that does not say which decision it
// means. One that names the run's, machine.RunHoldName with the run's
// identifier, reaches the run while the task's own holds stay open (#279),
// which is what a promoted investigation needs: its report's decisions
// stay open on the task while its run holds at a stage. The name is checked
// against the run selected, so a name for a run a later push superseded is
// refused rather than taken to mean the newer one.
//
// # Standing consent given a task
//
// Standing consent reaches the same run the same way, through run.start
// given a task, so a coordinator puts a task's run under consent without a
// checkout of its branch either. It selects the run on the terms above,
// refusal of a task with a hold of its own open included, and differs in one
// respect: the run need not be held, because consent given to a run that is
// executing answers its next decision. Whether the repository allows consent
// is read from its trusted copy first, as a start from a working copy reads
// it, and a refusal leaves the run as it was.
//
// # Who may ask
//
// A task's worker is refused, from the caller's credentials, on
// MethodTaskRespond's terms: no worker answers a hold, its own task's
// included, and no worker puts a run under consent to answer them either.
// Selecting a run by naming it is unchanged by this file.
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
		if r.Hold != "" {
			return machine.Run{}, fmt.Errorf("%w: an answer names decision %s and no task; a decision is named only given the task",
				ipc.ErrInvalidRequest, r.Hold)
		}
		return s.respond(ctx, r)
	}
	if r.Run != "" {
		return machine.Run{}, fmt.Errorf("%w: an answer names run %s and task %s, and selects its run one way", ipc.ErrInvalidRequest, r.Run, r.Task)
	}
	if err := s.refuseWorker(req); err != nil {
		return machine.Run{}, err
	}
	run, err := s.taskRun(ctx, r.Task, r.Hold)
	if err != nil {
		return machine.Run{}, err
	}
	r.Run, r.Task, r.Hold = run.ID, "", ""
	return s.respond(ctx, r)
}

// taskRun is the task's current run, held at a decision, or a typed
// refusal. A task with a hold of its own open and no run named, a name that
// is not the current run's, a task naming no branch, a branch with no
// unfinished run, and a run not waiting on an answer are each
// ipc.ErrRefused, and an unknown task is ipc.ErrRefused too; each refusal
// carries the internal/machine row a person reads about it. A name that is
// not a run's at all is ipc.ErrInvalidRequest, because a task's own hold is
// answered through task.respond.
func (s *Service) taskRun(ctx context.Context, id, name string) (store.Run, error) {
	var named string
	if name != "" {
		run, ok := machine.RunNamedBy(name)
		if !ok {
			return store.Run{}, fmt.Errorf("%w: an answer to task %s's run names %s, which is not a run's decision; "+
				"a run's is named %s", ipc.ErrInvalidRequest, id, name, machine.RunHoldName("<run>"))
		}
		named = run
	}
	record, err := s.taskCurrentRunNamed(ctx, id, named != "")
	if err != nil {
		return store.Run{}, err
	}
	if named != "" && record.ID != named {
		return store.Run{}, fmt.Errorf("%w: the answer names run %s, and task %s's current run is %s. %s",
			ipc.ErrRefused, named, id, record.ID, machine.PhraseTaskNothingWaiting)
	}
	if record.Status != store.RunHeld {
		return store.Run{}, fmt.Errorf("%w: task %s's run %s is %s, not waiting on an answer. %s",
			ipc.ErrRefused, id, record.ID, record.Status, machine.PhraseTaskNothingWaiting)
	}
	return record, nil
}

// startTo is run.start, the run selected through a working copy or, for
// standing consent, through a task, on StartRequest.Task's terms. The task's
// run is the one taskCurrentRun selects, refused on the same terms an answer
// given the task is, except that it need not be held: consent given to a run
// that is executing answers its next decision. Whether the repository allows
// consent is read before the record is touched, as start reads it.
func (s *Service) startTo(ctx context.Context, req ipc.Request, r machine.StartRequest) (machine.Run, error) {
	if r.Task == "" {
		return s.start(ctx, r)
	}
	switch {
	case r.WorkingPath != "":
		return machine.Run{}, fmt.Errorf("%w: a start names working copy %s and task %s, and selects its run one way",
			ipc.ErrInvalidRequest, r.WorkingPath, r.Task)
	case !r.StandingConsent:
		return machine.Run{}, fmt.Errorf("%w: a start given task %s asks for nothing; a task selects a run only to put it "+
			"under standing consent", ipc.ErrInvalidRequest, r.Task)
	case len(startInputs(r)) > 0:
		return machine.Run{}, fmt.Errorf("%w: a start given task %s carries %s, which start a run, and a task's run is "+
			"started by its push", ipc.ErrInvalidRequest, r.Task, strings.Join(startInputs(r), ", "))
	}
	if err := s.refuseWorker(req); err != nil {
		return machine.Run{}, err
	}
	record, err := s.taskCurrentRun(ctx, r.Task)
	if err != nil {
		return machine.Run{}, err
	}
	repository, err := s.store.Repository(ctx, record.RepositoryID)
	if err != nil {
		return machine.Run{}, err
	}
	allowed, err := s.standingConsentAllowed(ctx, repository)
	if err != nil {
		return machine.Run{}, err
	}
	if !allowed {
		return machine.Run{}, standingConsentRefused(repository)
	}
	if record, err = s.giveStandingConsent(ctx, record); err != nil {
		return machine.Run{}, err
	}
	return s.attach(ctx, record.ID)
}

// taskCurrentRun is the task's current run, reconciled against its
// checkpoint, whatever it is doing, or a typed refusal on taskRun's terms for
// every case but a run that is not held.
func (s *Service) taskCurrentRun(ctx context.Context, id string) (store.Run, error) {
	return s.taskCurrentRunNamed(ctx, id, false)
}

// taskCurrentRunNamed is taskCurrentRun, and with named it reaches the run
// past the task's own open holds rather than refusing for them: the answer
// named the run's decision, so no hold of the task's own is the one it
// meant (#279). Whether the run named is this one is taskRun's to check.
func (s *Service) taskCurrentRunNamed(ctx context.Context, id string, named bool) (store.Run, error) {
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
	case named:
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
		return store.Run{}, fmt.Errorf("%w: task %s's branch %s has no run under way. %s",
			ipc.ErrRefused, id, branch, machine.PhraseTaskNothingWaiting)
	}
	return s.reconcile(ctx, active.ID)
}
