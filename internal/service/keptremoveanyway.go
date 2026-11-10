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

// Removing anyway: PRD section 6's answer for a kept-work entry whose
// copy a discard refuses, which releases the entry and keeps the copy
// (decision 31). This build has its preview alone: it shows why the
// discard refuses, recording and releasing nothing. Its --force
// confirmation, the release, is not built yet.
//
// Whether a discard refuses is read afresh from the copy each time
// (removeAnywayProbe); nothing reads a refusal a confirmed discard met
// earlier, since a pre-PONR refusal takes its claim back and leaves
// nothing on the entry. So a copy whose preview reads but whose removal
// keeps being refused is refused here naming the discard (#880). Nothing
// of the loss is shown either: a discard that refuses has read none of it
// (#880).

// removeAnywayKeptWork is the remove-anyway answer: its preview.
func (s *Service) removeAnywayKeptWork(ctx context.Context, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	if !entry.Open() {
		return machine.KeptWorkAnswered{}, refusedAnswered(entry)
	}
	return s.showRemoveAnyway(ctx, entry)
}

// showRemoveAnyway reports why a discard refuses, recording nothing.
func (s *Service) showRemoveAnyway(ctx context.Context, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	if err := s.refuseRemoveAnywayWhileBusy(entry); err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	refusal, err := s.removeAnywayProbe(ctx, entry)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	return machine.KeptWorkAnswered{Entry: entry, Effect: machine.KeptWorkRemoveAnywayShown, Refusal: refusal}, nil
}

// refuseRemoveAnywayWhileBusy refuses while a land, carry-on or discard
// claim or a discard set-aside stands on the copy (PRD section 6).
func (s *Service) refuseRemoveAnywayWhileBusy(entry store.KeptWork) error {
	switch {
	case entry.LandingClaimed():
		return fmt.Errorf("%w: kept work entry %d was not released: a land claim stands on it",
			ipc.ErrRefused, entry.ID)
	case entry.CarryOnClaimed():
		return fmt.Errorf("%w: kept work entry %d was not released: a carry-on claim stands on it",
			ipc.ErrRefused, entry.ID)
	case entry.DiscardClaimed():
		return fmt.Errorf("%w: kept work entry %d was not released: a discard claim stands on it; finish or withdraw that discard first",
			ipc.ErrRefused, entry.ID)
	}
	if discardDirectoryStands(s, entry.CopyPath) {
		return fmt.Errorf("%w: kept work entry %d was not released: a discard set aside still stands beside the copy",
			ipc.ErrRefused, entry.ID)
	}
	return nil
}

// removeAnywayProbe decides whether remove-anyway is offered, from the
// copy as it reads now: a discard that can read and remove the copy
// refuses naming discard; otherwise it returns the discard's refusal.
func (s *Service) removeAnywayProbe(ctx context.Context, entry store.KeptWork) (refusal string, err error) {
	gatePath, err := s.keptGatePath(ctx, entry)
	if err != nil {
		return "", err
	}
	if verifyErr := s.cleaner.VerifyKeptCopy(ctx, entry, gatePath); verifyErr != nil {
		switch {
		case errors.Is(verifyErr, tasks.ErrCopyGone),
			errors.Is(verifyErr, tasks.ErrRecordNotTheCopys),
			errors.Is(verifyErr, tasks.ErrLayoutUnsupported):
			return fmt.Sprintf("discard refused: %v", verifyErr), nil
		default:
			return "", verifyErr
		}
	}
	_, lossErr := s.cleaner.Loss(ctx, entry, gatePath)
	if lossErr == nil {
		return "", fmt.Errorf("%w: kept work entry %d was not released: a discard can remove the copy; confirm that discard instead of remove-anyway",
			ipc.ErrRefused, entry.ID)
	}
	switch {
	case errors.Is(lossErr, tasks.ErrLossUnestablished),
		errors.Is(lossErr, tasks.ErrLayoutUnsupported),
		errors.Is(lossErr, tasks.ErrRecordNotTheCopys),
		errors.Is(lossErr, tasks.ErrCopyGone):
		return fmt.Sprintf("discard refused: %v", lossErr), nil
	default:
		return "", lossErr
	}
}
