package service

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// The coordinator's task methods: task.create, task.promote and
// task.relaunch, PRD section 9's three service methods beside the command
// table (#95 lines A, E and J). Its paragraph's two steering methods,
// task.message and task.stop, and the refused task.interrupt are steer.go's,
// on this file's terms for who may ask and how a refusal crosses the wire.
//
// # None is a second implementation
//
// Each method is a caller of the library entry that already owns the act:
// task.create calls internal/tasks' Queue.Create, task.promote calls its
// Queue.Promote, and task.relaunch calls relaunchTask, the one anchored
// relaunch reconcile.go's start-time pass calls, and then hands the launch
// to launchInBackground exactly as that pass does. What this file adds is
// who may ask and how an answer or a refusal crosses the wire.
//
// # Who may ask
//
// Each is coordinator-only, on two checks over the caller's credentials and
// nothing the caller says about itself. internal/ipc declares all three
// restricted, so a caller it cannot identify is refused with
// ipc.ErrUnidentifiedPeer and a caller contained by a validation stage with
// ipc.ErrContained before this file runs. Then refuseWorker, the placement
// workers.go derives for the wakes methods and supervision.assert, refuses
// with ipc.ErrWorker a caller the worker group set places in any task's
// worker group, before anything is read or written. That second check is
// what keeps an investigation from promoting itself: its worker's own
// process group is in the set while it runs, so the request is refused
// whatever task it names. No second placement check is built here.
//
// # How a refusal crosses the wire
//
// A refusal stays a typed result, on refusalOf's terms: a request intake or
// promotion cannot read is ipc.ErrInvalidRequest, a request the records do
// not allow is ipc.ErrRefused, and a fact the service could not establish is
// ipc.ErrUnavailable. The typed error the library returned is wrapped beside
// the code, so this package's own callers still match it with errors.As;
// over the wire the code and the message travel, which is internal/ipc's
// rule for every failure.
//
// # Residual gaps
//
//   - The placement is workers.go's and carries its gaps: a descendant that
//     leaves its worker's process group is outside it, a worker an earlier
//     service of this home launched is in no set here, and with no worker
//     launched the group is not read at all.
//   - Where internal/ipc reads no local socket peer credentials, every
//     restricted method is refused, these three included, so on such a
//     platform no coordinator can call them either: that is the platform
//     guard internal/ipc states, failing closed, and not parity.
//   - A caller outside every worker group is taken as the coordinator. The
//     service has no relation that names the coordinator positively, so a
//     person's own client on the socket, which is the same user, is
//     admitted on the same terms; PRD section 9's rule is that no worker can
//     call these, and that is what is enforced.
//   - task.create and task.relaunch answer once their records are written:
//     the copy a created task is cut and the worker a relaunch starts are
//     the service's work afterwards, so their outcome is read from the
//     task and its attempts rather than from this answer.

// createTask records a new task through internal/tasks' intake, for the
// coordinator, and then dispatches what is due in the background, as
// releaseQueued does after a release: a task intake recorded independent
// resolves Dispatched and is cut its copy by dispatch.go.
func (s *Service) createTask(ctx context.Context, req ipc.Request, r machine.TaskCreateRequest) (machine.TaskCreated, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.TaskCreated{}, err
	}
	created, err := s.queue.Create(ctx, tasks.Request{
		Shape: r.Shape, Project: r.Project, Instructions: r.Instructions, Mode: r.Mode,
		Repository: r.Repository, Branch: r.Branch, Overlaps: r.Overlaps, Delivery: r.Delivery,
	})
	if err != nil {
		return machine.TaskCreated{}, refusalOf(err)
	}
	s.log.Printf("task %s was created by %s (%s)", created.Task.ID, peerOf(req), created.Cause)
	s.dispatchInBackground()
	return machine.TaskCreated{
		Record: created.Task, Cause: string(created.Cause), BlockerState: created.BlockerState,
		Instructions: created.Instructions,
	}, nil
}

// promoteTask flips an investigation task to delivery through internal/tasks'
// promotion, for the coordinator. The posture the promoted task is recorded
// under is read by that promotion through projectPosture, the reader intake
// uses, so a trusted copy this service cannot read refuses it as
// ipc.ErrUnavailable on refusalOf's terms. The environment the copy is
// asserted under is the one every worker of this service is started with,
// the same agents.WorkerEnvironment(os.Environ(), nil) assertCopy uses, and
// never one the caller states.
func (s *Service) promoteTask(ctx context.Context, req ipc.Request, r machine.TaskPromoteRequest) (machine.TaskPromoted, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.TaskPromoted{}, err
	}
	promoted, err := s.queue.Promote(ctx, tasks.Promotion{
		Task: r.Task, Repository: r.Repository, Branch: r.Branch, Intent: r.Intent, Delivery: r.Delivery,
		Environment: agents.WorkerEnvironment(os.Environ(), nil),
	})
	if err != nil {
		return machine.TaskPromoted{}, refusalOf(err)
	}
	s.log.Printf("task %s was promoted to delivery on %s by %s", r.Task, r.Branch, peerOf(req))
	out := machine.TaskPromoted{
		Record: promoted.Task, Event: promoted.Event, Head: promoted.Head, Instructions: promoted.Instructions,
	}
	if report, ok := promoted.Report.Get(); ok {
		at := report.RecordedAt
		out.ReportRecordedAt = &at
	}
	return out, nil
}

// relaunchTaskFor relaunches a task whose terminal is gone, for the
// coordinator, through relaunchTask and launchInBackground: the claim, the
// records and the launch reconciliation makes, and nothing else. A task the
// reconciliation table does not relaunch, a live terminal among them, is
// refused there and not here.
func (s *Service) relaunchTaskFor(ctx context.Context, req ipc.Request, r machine.TaskRequest) (machine.TaskRelaunched, error) {
	if err := s.refuseWorker(req); err != nil {
		return machine.TaskRelaunched{}, err
	}
	if r.Task == "" {
		return machine.TaskRelaunched{}, fmt.Errorf("%w: a relaunch names no task", ipc.ErrInvalidRequest)
	}
	out, err := s.relaunchTask(ctx, r.Task)
	if err != nil {
		return machine.TaskRelaunched{}, refusalOf(err)
	}
	s.log.Printf("task %s was relaunched in place as attempt %d by %s, superseding session %s; %s",
		r.Task, out.launch.attempt, peerOf(req), out.superseded, out.swept)
	s.launchInBackground(out.launch)
	return machine.TaskRelaunched{
		Task: r.Task, Attempt: out.launch.attempt, Superseded: out.superseded, Swept: out.swept,
	}, nil
}

// dispatchInBackground runs dispatchDue on a goroutine registered through
// startWork, so the answer that recorded a task does not wait on cutting its
// copy and Close still waits for the dispatch.
func (s *Service) dispatchInBackground() {
	if !s.startWork() {
		s.log.Printf("dispatching was not started because this service is giving up the home; the next start dispatches")
		return
	}
	go func() {
		defer s.work.Done()
		s.dispatchDue(s.stopCtx)
	}()
}

// refusalOf classifies what a library entry returned for the wire, wrapping
// the code beside the error so errors.As still finds the typed refusal: a
// request intake or promotion could not read is ipc.ErrInvalidRequest, a
// fact the service could not establish is ipc.ErrUnavailable, and every
// typed refusal the records decided is ipc.ErrRefused. An error that is
// none of these is a failure of a read or a write and is returned as it is,
// which the wire reports as internal.
//
// It names the promotion and store refusals itself because tasks.IsRefusal is
// intake-shaped and answers for none of them.
func refusalOf(err error) error {
	var held *relaunchHeld
	var unswept *copyUnswept
	var unproven *relaunchUnswept
	switch {
	case errors.Is(err, tasks.ErrRequest):
		return fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
	case errors.Is(err, errRelaunchUnprobed), errors.Is(err, tasks.ErrPostureUnreadable):
		return fmt.Errorf("%w: %w", ipc.ErrUnavailable, err)
	case tasks.IsRefusal(err),
		errors.Is(err, tasks.ErrNotPromotable),
		errors.Is(err, tasks.ErrBranchHeld),
		errors.Is(err, tasks.ErrPromotionRefused),
		errors.Is(err, tasks.ErrNotIsolated),
		errors.Is(err, store.ErrTaskMoved),
		errors.Is(err, store.ErrTaskShapeMoved),
		errors.Is(err, store.ErrCleanupRecorded),
		errors.Is(err, errRelaunchNotDue),
		errors.Is(err, errRelaunchNotDead),
		errors.Is(err, errRelaunchInFlight),
		errors.As(err, &held),
		errors.As(err, &unswept),
		errors.As(err, &unproven):
		return fmt.Errorf("%w: %w", ipc.ErrRefused, err)
	}
	return err
}
