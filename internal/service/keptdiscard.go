package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// Discarding kept work, as far as this build takes it: PRD section 6's
// "you name the copy, the program shows exactly what will be lost", and
// section 9's first half, "--answer discard alone shows exactly what would
// be lost, with a short digest of it, and removes nothing". The removal on
// a confirmed discard is deferred to AGE-486 (docs/prd.md, sections 6 and
// 9, the owner's decision of 2026-10-25); its work is on the branch
// feat/age-407-discard.
//
// A discard answer given no confirmation reads what would be lost through
// internal/tasks' Loss, the same read a land decides from, and returns it
// with its digest. Nothing is recorded and nothing is removed, and the
// entry stays open and listed, holding its project's new tasks as before.
//
// A discard answer given a confirmation is refused with discardDeferred
// before anything is read or written, so the copy, its entry and the store
// are exactly as they were. What closes the entry instead, besides a land
// or a carry on, is P12 cleanup removing the copy on a proof, which needs
// everything the copy holds to be safe elsewhere, anything uncommitted,
// untracked or stashed included, and happens only when the cleanup
// decision is next asked of the copy at one of cleanup.go's sites; the
// removal closes the entry. The refusal's row says what that needs of the
// person (machine.PhraseDiscardRemovalDeferred).

// discardKeptWork is the discard answer, on this file's terms.
func (s *Service) discardKeptWork(ctx context.Context, entry store.KeptWork, confirm string) (machine.KeptWorkAnswered, error) {
	if confirm != "" {
		return machine.KeptWorkAnswered{}, discardDeferred{entry: entry.ID}
	}
	if !entry.Open() {
		return machine.KeptWorkAnswered{}, refusedAnswered(entry)
	}
	gatePath, err := s.keptGatePath(ctx, entry)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	loss, err := s.cleaner.Loss(ctx, entry, gatePath)
	if err != nil {
		return machine.KeptWorkAnswered{}, refusedLoss(entry, err)
	}
	shown := lossOf(loss)
	return machine.KeptWorkAnswered{Entry: entry, Effect: machine.KeptWorkDiscardShown, Loss: &shown}, nil
}

// discardDeferred is the refusal of a discard given a confirmation: the
// removal it would make is deferred to AGE-486, so nothing was read or
// written. Its text is the row a person reads,
// machine.PhraseDiscardRemovalDeferred, and it matches ipc.ErrRefused.
type discardDeferred struct{ entry int64 }

// Error names the entry and is the row.
func (e discardDeferred) Error() string {
	return fmt.Sprintf("kept work entry %d was not discarded: %s", e.entry, machine.PhraseDiscardRemovalDeferred)
}

// Unwrap makes the refusal match ipc.ErrRefused.
func (e discardDeferred) Unwrap() error { return ipc.ErrRefused }

// refusedLoss is the refusal of a discard whose loss could not be read.
func refusedLoss(entry store.KeptWork, err error) error {
	switch {
	case errors.Is(err, tasks.ErrLossUnestablished):
		return fmt.Errorf("%w: kept work entry %d was not shown: %v; the next cleanup of the unchanged copy establishes it, "+
			"and until then what a discard would lose cannot be shown exactly", ipc.ErrRefused, entry.ID, err)
	case errors.Is(err, tasks.ErrCopyGone), errors.Is(err, tasks.ErrRecordNotTheCopys), errors.Is(err, tasks.ErrLayoutUnsupported):
		return fmt.Errorf("%w: kept work entry %d was not shown: %w", ipc.ErrRefused, entry.ID, err)
	}
	return err
}

// lossOf is tasks.Loss as the answer carries it, each path and name with
// its exact bytes beside it (machine.LostPath, machine.LostObject), since
// the JSON the answer travels as does not carry a byte that is not UTF-8.
func lossOf(l tasks.Loss) machine.KeptWorkLoss {
	files := make([]machine.KeptWorkLostPath, len(l.Files))
	for i, f := range l.Files {
		files[i] = machine.LostPath(string(f.Kind), f.Path)
	}
	objects := make([]machine.KeptWorkLostObject, len(l.Objects))
	for i, o := range l.Objects {
		objects[i] = machine.LostObject(o.Object, o.Type, o.Names)
	}
	return machine.KeptWorkLoss{Head: l.Head, Lacks: l.Lacks, Objects: objects, Files: files, Dirty: l.Dirty, Untracked: l.Untracked,
		Stash: l.Stash, Digest: l.Digest}
}

// keptGatePath is the gate repository a run's copy is a worktree of, and
// empty for a task's clone, which tasks.Cleaner's Loss and VerifyKeptCopy
// take.
func (s *Service) keptGatePath(ctx context.Context, entry store.KeptWork) (string, error) {
	runID, ok := entry.RunID.Get()
	if !ok {
		return "", nil
	}
	owner, err := s.store.Run(ctx, runID)
	if err != nil {
		return "", err
	}
	repository, err := s.store.Repository(ctx, owner.RepositoryID)
	if err != nil {
		return "", err
	}
	return s.gatePathOf(ctx, repository)
}
