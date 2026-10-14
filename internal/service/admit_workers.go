package service

import (
	"context"
	"fmt"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The admission rule for a worker's push: a push whose peer is inside a
// worker group may update only refs/heads/<that task's branch>, forward
// only, and may delete nothing. It is #95 line 19's proposed rule, open
// there, built as #91's issue text states it.
//
// # Whose decision it is
//
// internal/safety owns whether a branch update the program makes may
// proceed, and on what anchor: a lease over an Observation a run made, an
// incorporation check, a force decision. This rule answers a different
// question. Nothing here updates a branch; a pusher is updating one, and
// admission decides whether that pusher may make that update. The fact the
// rule turns on is who the pusher is, which only this service holds, in the
// worker group set, and the only mechanism it needs is internal/vcs' ancestry
// read. A rule placed in internal/safety would take the peer's task from
// this service and hand back a yes over no anchor of its own, which is a
// per-ref policy over the pusher and not a branch update of the program's.
// So it is admission's, decided beside the other admission checks in
// admit, with the mechanism in internal/vcs and nothing of it written at the
// git call.
//
// # What is read, and where
//
// Which reference and whether it is a deletion are the update's own. Whether
// it is forward is read in two places. The reference's standing value is
// read from the gate repository, which is the fact admission is about, and
// an update whose stated old object is not what the gate holds is refused
// rather than decided against the caller's account. Whether that value is
// an ancestor of the pushed commit is read in the task's copy, because that
// is where the worker's commit is: admission runs before the gate accepts
// the push, and this service reads the gate's own store, so the gate is not
// asked about a commit it has not accepted. A pushed commit the copy does
// not hold is refused as a fact that could not be established, which is
// what a worker pushing from somewhere other than its copy meets. A
// reference the gate does not hold yet is created, and that needs no
// ancestry read.
//
// # What it does not cover
//
//   - An investigation task names no branch, so its worker's push is refused
//     whatever it carries.
//   - Nothing here reads whether the pushed commit is the copy's head, only
//     that it descends from what the gate holds.
//   - The gate's standing value and the hook's are two reads; a push that
//     lands between them is refused by the mismatch rather than raced.

// admitWorkerPush applies the rule to a push whose peer is a task's worker,
// and does nothing for every other peer. A refusal wraps ipc.ErrWorker and
// names the update; a fact that could not be established wraps
// ipc.ErrUnavailable.
func (s *Service) admitWorkerPush(ctx context.Context, from ipc.Request, req machine.GateRequest) error {
	m, ok, err := s.workerPeer(from)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	task, err := s.store.Task(ctx, m.Task)
	if err != nil {
		return fmt.Errorf("%w: the task %s whose worker is pushing could not be read: %w", ipc.ErrUnavailable, m.Task, err)
	}
	branch, named := task.Branch.Get()
	if !named {
		return fmt.Errorf("%w: task %s names no branch, so its worker may push nothing to gate %s",
			ipc.ErrWorker, m.Task, req.Gate)
	}
	repositoryPath, err := gate.RepositoryFor(s.home.Root(), req.Gate)
	if err != nil {
		return fmt.Errorf("%w: locating gate %s: %w", ipc.ErrUnavailable, req.Gate, err)
	}
	bare, err := vcs.OpenBare(ctx, repositoryPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return fmt.Errorf("%w: opening gate %s: %w", ipc.ErrUnavailable, req.Gate, err)
	}
	for _, update := range req.Updates {
		if err := s.admitWorkerUpdate(ctx, bare, task, m, branch, update); err != nil {
			return err
		}
	}
	return nil
}

// admitWorkerUpdate applies the rule to one update.
func (s *Service) admitWorkerUpdate(ctx context.Context, bare *vcs.Repository, task store.Task, m session.Membership,
	branch string, update gate.RefUpdate) error {
	refuse := func(why string) error {
		return fmt.Errorf("%w: the worker of task %s may update only refs/heads/%s, forward, and may delete nothing; "+
			"%s %s", ipc.ErrWorker, m.Task, branch, update, why)
	}
	if update.Branch() != branch {
		return refuse("is not that branch")
	}
	if update.Deleted() {
		return refuse("deletes it")
	}
	standing, err := bare.ListRefs(ctx, update.Ref)
	if err != nil {
		return fmt.Errorf("%w: reading %s in the gate: %w", ipc.ErrUnavailable, update.Ref, err)
	}
	var held string
	for _, ref := range standing {
		if ref.Name == update.Ref {
			held = ref.Object
		}
	}
	if held == "" {
		if !update.Created() {
			return refuse("names an old object for a reference the gate does not hold")
		}
		return nil
	}
	if update.Old != held {
		return refuse(fmt.Sprintf("names old object %s and the gate holds %s", update.Old, held))
	}
	copyPath, err := s.home.TaskCopy(task.ID)
	if err != nil {
		return fmt.Errorf("%w: locating the copy of task %s: %w", ipc.ErrUnavailable, task.ID, err)
	}
	working, err := vcs.OpenWorktree(ctx, copyPath, vcs.WithRedactor(redact.New()))
	if err != nil {
		return fmt.Errorf("%w: opening the copy of task %s: %w", ipc.ErrUnavailable, task.ID, err)
	}
	forward, err := working.IsAncestor(ctx, held, update.New)
	if err != nil {
		return fmt.Errorf("%w: whether %s is forward of %s could not be established in the copy of task %s: %w",
			ipc.ErrUnavailable, update.New, held, task.ID, err)
	}
	if !forward {
		return refuse("does not descend from what the gate holds")
	}
	return nil
}
