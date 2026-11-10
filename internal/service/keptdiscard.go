package service

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// Discarding kept work: PRD section 6's "you name the copy, the program
// shows exactly what will be lost, and it removes the copy only on that
// answer". A discard without Confirm shows the loss through
// internal/tasks' Loss and records nothing. A confirmation from a
// terminal's foreground claims the entry, removes a task's clone or a
// run's worktree through Cleaner.Discard, records the outcome, clears the
// discard directory, and redispatches. remove-anyway is
// keptremoveanyway.go.

// discardKeptWork is the discard answer, on this file's terms.
func (s *Service) discardKeptWork(ctx context.Context, req ipc.Request, entry store.KeptWork, confirm string) (machine.KeptWorkAnswered, error) {
	if !entry.Open() && entry.Answer.Or("") != store.KeptDiscard {
		return machine.KeptWorkAnswered{}, refusedAnswered(entry)
	}
	if confirm == "" {
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
	if err := s.refuseOffTerminal(req); err != nil {
		if errors.Is(err, ipc.ErrNotAtTerminal) {
			return machine.KeptWorkAnswered{}, discardOffTerminal{entry: entry.ID, cause: err}
		}
		return machine.KeptWorkAnswered{}, err
	}
	if entry.Open() && entry.DiscardClaimed() {
		return s.completeDiscard(ctx, entry)
	}
	if !entry.Open() {
		return machine.KeptWorkAnswered{}, refusedAnswered(entry)
	}
	return s.confirmDiscard(ctx, req, entry, confirm)
}

// confirmDiscard claims and removes a task's clone or a run's worktree on
// the confirmed digest.
func (s *Service) confirmDiscard(ctx context.Context, req ipc.Request, entry store.KeptWork, confirm string) (machine.KeptWorkAnswered, error) {
	gatePath, err := s.keptGatePath(ctx, entry)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	loss, err := s.cleaner.Loss(ctx, entry, gatePath)
	if err != nil {
		return machine.KeptWorkAnswered{}, refusedLoss(entry, err)
	}
	if loss.Digest != confirm {
		return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d was not discarded: %w",
			ipc.ErrRefused, entry.ID, tasks.ErrConfirmMismatch)
	}
	siblings, err := s.store.OpenKeptWorkOfCopy(ctx, entry.CopyPath)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	verified := make([]int64, 0, len(siblings))
	for _, k := range siblings {
		if k.ID != entry.ID {
			verified = append(verified, k.ID)
		}
	}
	claimed, err := s.store.ClaimKeptWorkDiscard(ctx, entry.ID, confirm, loss.Lacks, req.HoldResolver())
	switch {
	case errors.Is(err, store.ErrKeptWorkAnswered) || errors.Is(err, store.ErrKeptWorkClosed) ||
		errors.Is(err, store.ErrKeptWorkDiscardClaimed):
		again, readErr := s.store.KeptWorkEntry(ctx, entry.ID)
		if readErr != nil {
			return machine.KeptWorkAnswered{}, readErr
		}
		if again.Answer.Or("") == store.KeptDiscard {
			return machine.KeptWorkAnswered{Entry: again, Effect: machine.KeptWorkDiscarded}, nil
		}
		if again.DiscardClaimed() {
			return s.completeDiscard(ctx, again)
		}
		return machine.KeptWorkAnswered{}, refusedAnswered(again)
	case errors.Is(err, store.ErrKeptWorkCopyOwed):
		return machine.KeptWorkAnswered{}, refusedOwed(entry, err)
	case err != nil:
		return machine.KeptWorkAnswered{}, err
	}
	return s.finishDiscardClaim(ctx, claimed, gatePath, confirm, verified)
}

// completeDiscard finishes a standing discard claim (resume after a stop).
func (s *Service) completeDiscard(ctx context.Context, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	digest, ok := entry.DiscardDigest.Get()
	if !ok || digest == "" {
		return machine.KeptWorkAnswered{}, fmt.Errorf("service: kept work entry %d holds a discard claim with no digest", entry.ID)
	}
	siblings, err := s.store.OpenKeptWorkOfCopy(ctx, entry.CopyPath)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	verified := make([]int64, 0, len(siblings))
	for _, k := range siblings {
		if k.ID != entry.ID {
			verified = append(verified, k.ID)
		}
	}
	gatePath, err := s.keptGatePath(ctx, entry)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	return s.finishDiscardClaim(ctx, entry, gatePath, digest, verified)
}

// finishDiscardClaim removes the copy for a claimed discard, records the
// outcome, clears the discard directory, and redispatches.
func (s *Service) finishDiscardClaim(ctx context.Context, entry store.KeptWork, gatePath, digest string, verified []int64) (machine.KeptWorkAnswered, error) {
	if err := s.cleaner.Discard(ctx, entry, gatePath, digest); err != nil {
		// A pre-PONR refusal puts everything back and clears the discard
		// directory (PRD #discard-removal): take the claim back so the
		// person can show and confirm again. A directory that still
		// stands means put-back failed or the removal is past PONR, so
		// the claim stays for completeDiscard.
		if !discardDirectoryStands(s, entry.CopyPath) {
			if _, werr := s.store.WithdrawKeptWorkDiscard(ctx, entry.ID); werr != nil && !errors.Is(werr, store.ErrKeptWorkNotDiscard) {
				return machine.KeptWorkAnswered{}, fmt.Errorf("%w (and withdrawing the claim: %w)", err, werr)
			}
		}
		return machine.KeptWorkAnswered{}, refusedDiscard(entry, err)
	}
	done, err := s.store.RecordKeptWorkDiscard(ctx, entry.ID, verified)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	if dir, err := s.home.Discarding(entry.CopyPath); err == nil {
		_ = os.RemoveAll(dir)
	}
	s.redispatchAfterRemoval()
	return machine.KeptWorkAnswered{Entry: done, Effect: machine.KeptWorkDiscarded}, nil
}

// discardDirectoryStands reports whether the discard directory beside the
// copy still exists, which means a put-back failed or the removal is past
// its point of no return and the claim must stay owed.
func discardDirectoryStands(s *Service, copyPath string) bool {
	dir, err := s.home.Discarding(copyPath)
	if err != nil {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// completeDiscardsAtOpen finishes every discard claim a stopped service left.
func (s *Service) completeDiscardsAtOpen(ctx context.Context) {
	owed, err := s.store.KeptWorkDiscardsOwed(ctx)
	if err != nil {
		s.log.Printf("reading discard claims owed: %v", err)
		return
	}
	for _, entry := range owed {
		if _, err := s.completeDiscard(ctx, entry); err != nil {
			s.log.Printf("completing discard of kept work entry %d: %v", entry.ID, err)
		}
	}
}

// discardOffTerminal is the refusal of a discard confirmation from a peer
// foreground.go's relation does not place in a terminal's foreground.
type discardOffTerminal struct {
	entry int64
	cause error
}

func (e discardOffTerminal) Error() string {
	return fmt.Sprintf("kept work entry %d was not discarded: %s (%v)", e.entry, machine.PhraseDiscardNeedsTerminal, e.cause)
}

func (e discardOffTerminal) Unwrap() error { return e.cause }

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

// refusedDiscard is the refusal of a confirmed discard whose removal failed.
func refusedDiscard(entry store.KeptWork, err error) error {
	switch {
	case errors.Is(err, tasks.ErrConfirmMismatch), errors.Is(err, tasks.ErrLossChanged):
		return fmt.Errorf("%w: kept work entry %d was not discarded: %w", ipc.ErrRefused, entry.ID, err)
	case errors.Is(err, tasks.ErrCopyGone), errors.Is(err, tasks.ErrLayoutUnsupported),
		errors.Is(err, tasks.ErrRecordNotTheCopys), errors.Is(err, tasks.ErrDiscardStateUnreadable):
		return fmt.Errorf("%w: kept work entry %d was not discarded: %w", ipc.ErrRefused, entry.ID, err)
	}
	return err
}

// lossOf is tasks.Loss as the answer carries it.
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
// empty for a task's clone.
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
