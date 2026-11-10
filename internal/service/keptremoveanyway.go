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

// Removing anyway: PRD section 6's release of a kept-work entry after a
// discard refuses the copy, keeping the copy. Without Force it shows why
// the discard refuses. With Force, from a terminal's foreground and naming
// the same task or run, it claims the entry, records the release, and
// redispatches. It never removes the copy (decision 31).
//
// Whether a discard refuses is read afresh from the copy each time
// (removeAnywayProbe); nothing reads a refusal a confirmed discard met
// earlier, since a pre-PONR refusal takes its claim back and leaves
// nothing on the entry. So a copy whose preview reads but whose removal
// keeps being refused is refused here naming only that the preview reads
// (not that a discard can remove it), and neither answer releases it
// (#880). Nothing of the loss is shown either: Loss returns its refusal
// and no loss, even where it read the copy before refusing (#880).

// removeAnywayKeptWork is the remove-anyway answer.
func (s *Service) removeAnywayKeptWork(ctx context.Context, req ipc.Request, entry store.KeptWork, force bool) (machine.KeptWorkAnswered, error) {
	if !entry.Open() && entry.Answer.Or("") != store.KeptRemoveAnyway {
		return machine.KeptWorkAnswered{}, refusedAnswered(entry)
	}
	if !force {
		if !entry.Open() {
			return machine.KeptWorkAnswered{}, refusedAnswered(entry)
		}
		if entry.RemoveAnywayClaimed() {
			return machine.KeptWorkAnswered{}, fmt.Errorf("%w: kept work entry %d was not shown: a remove-anyway claim already stands; confirm with --force to finish it",
				ipc.ErrRefused, entry.ID)
		}
		return s.showRemoveAnyway(ctx, entry)
	}
	if err := s.refuseOffTerminal(req); err != nil {
		if errors.Is(err, ipc.ErrNotAtTerminal) {
			return machine.KeptWorkAnswered{}, removeAnywayOffTerminal{entry: entry.ID, cause: err}
		}
		return machine.KeptWorkAnswered{}, err
	}
	if entry.Open() && entry.RemoveAnywayClaimed() {
		return s.completeRemoveAnyway(ctx, entry)
	}
	if !entry.Open() {
		return machine.KeptWorkAnswered{}, refusedAnswered(entry)
	}
	return s.confirmRemoveAnyway(ctx, req, entry)
}

// showRemoveAnyway reports why a discard refuses, recording nothing.
func (s *Service) showRemoveAnyway(ctx context.Context, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	if err := s.refuseRemoveAnywayWhileBusy(ctx, entry); err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	refusal, err := s.removeAnywayProbe(ctx, entry)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	return machine.KeptWorkAnswered{Entry: entry, Effect: machine.KeptWorkRemoveAnywayShown, Refusal: refusal}, nil
}

// confirmRemoveAnyway claims and releases the entry on a Force
// confirmation, keeping the copy.
func (s *Service) confirmRemoveAnyway(ctx context.Context, req ipc.Request, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	if err := s.refuseRemoveAnywayWhileBusy(ctx, entry); err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	refusal, err := s.removeAnywayProbe(ctx, entry)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
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
	// The preview showed nothing of the loss, so the claim records that.
	claimed, err := s.store.ClaimKeptWorkRemoveAnyway(ctx, entry.ID, refusal, nil, req.HoldResolver())
	switch {
	case errors.Is(err, store.ErrKeptWorkAnswered) || errors.Is(err, store.ErrKeptWorkClosed) ||
		errors.Is(err, store.ErrKeptWorkRemoveAnywayClaimed):
		again, readErr := s.store.KeptWorkEntry(ctx, entry.ID)
		if readErr != nil {
			return machine.KeptWorkAnswered{}, readErr
		}
		if again.Answer.Or("") == store.KeptRemoveAnyway {
			return machine.KeptWorkAnswered{Entry: again, Effect: machine.KeptWorkRemovedAnyway}, nil
		}
		if again.RemoveAnywayClaimed() {
			return s.completeRemoveAnyway(ctx, again)
		}
		return machine.KeptWorkAnswered{}, refusedAnswered(again)
	case errors.Is(err, store.ErrKeptWorkCopyOwed):
		return machine.KeptWorkAnswered{}, refusedOwed(entry, err)
	case err != nil:
		return machine.KeptWorkAnswered{}, err
	}
	return s.finishRemoveAnywayClaim(ctx, claimed, verified)
}

// completeRemoveAnyway finishes a standing remove-anyway claim.
func (s *Service) completeRemoveAnyway(ctx context.Context, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
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
	return s.finishRemoveAnywayClaim(ctx, entry, verified)
}

// finishRemoveAnywayClaim records the release and redispatches. The copy
// is not touched.
func (s *Service) finishRemoveAnywayClaim(ctx context.Context, entry store.KeptWork, verified []int64) (machine.KeptWorkAnswered, error) {
	// Re-probe: only a refusal that no longer holds takes the claim back;
	// any other failure leaves it owed.
	if _, err := s.removeAnywayProbe(ctx, entry); err != nil {
		if !errors.Is(err, ipc.ErrRefused) {
			return machine.KeptWorkAnswered{}, err
		}
		if _, werr := s.store.WithdrawKeptWorkRemoveAnyway(ctx, entry.ID); werr != nil && !errors.Is(werr, store.ErrKeptWorkNotRemoveAnyway) {
			return machine.KeptWorkAnswered{}, fmt.Errorf("%w (and withdrawing the claim: %w)", err, werr)
		}
		return machine.KeptWorkAnswered{}, err
	}
	done, err := s.store.RecordKeptWorkRemoveAnyway(ctx, entry.ID, verified)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	s.redispatchAfterRemoval()
	return machine.KeptWorkAnswered{Entry: done, Effect: machine.KeptWorkRemovedAnyway}, nil
}

// completeRemoveAnywaysAtOpen finishes every remove-anyway claim a stopped
// service left.
func (s *Service) completeRemoveAnywaysAtOpen(ctx context.Context) {
	owed, err := s.store.KeptWorkRemoveAnywaysOwed(ctx)
	if err != nil {
		s.log.Printf("reading remove-anyway claims owed: %v", err)
		return
	}
	for _, entry := range owed {
		if _, err := s.completeRemoveAnyway(ctx, entry); err != nil {
			s.log.Printf("completing remove-anyway of kept work entry %d: %v", entry.ID, err)
		}
	}
}

// refuseRemoveAnywayWhileBusy refuses while a land, carry-on or discard
// claim stands on the entry, any claim on another open entry of its copy,
// or a discard set-aside beside the copy (PRD section 6).
func (s *Service) refuseRemoveAnywayWhileBusy(ctx context.Context, entry store.KeptWork) error {
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
	ofCopy, err := s.store.OpenKeptWorkOfCopy(ctx, entry.CopyPath)
	if err != nil {
		return err
	}
	for _, k := range ofCopy {
		if k.ID != entry.ID && (k.LandingClaimed() || k.CarryOnClaimed() || k.DiscardClaimed() || k.RemoveAnywayClaimed()) {
			return fmt.Errorf("%w: kept work entry %d was not released: a claim stands on kept work entry %d of its copy",
				ipc.ErrRefused, entry.ID, k.ID)
		}
	}
	if discardDirectoryStands(s, entry.CopyPath) {
		return fmt.Errorf("%w: kept work entry %d was not released: a discard set aside still stands beside the copy",
			ipc.ErrRefused, entry.ID)
	}
	return nil
}

// removeAnywayProbe decides whether remove-anyway is offered, from the
// copy as it reads now: a copy whose loss a discard's preview reads is
// refused naming only that read, since whether a discard can remove the
// copy is not known until confirmation and may still refuse (#880);
// otherwise it returns the discard's refusal.
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
		return "", fmt.Errorf("%w: kept work entry %d was not released: a discard's preview reads the copy's loss; whether a discard can remove the copy is not known yet; show that discard to see the loss",
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

// removeAnywayOffTerminal is the refusal of a remove-anyway confirmation
// from a peer foreground.go's relation does not place in a terminal's
// foreground.
type removeAnywayOffTerminal struct {
	entry int64
	cause error
}

func (e removeAnywayOffTerminal) Error() string {
	return fmt.Sprintf("kept work entry %d was not released: %s (%v)", e.entry, machine.PhraseRemoveAnywayNeedsTerminal, e.cause)
}

func (e removeAnywayOffTerminal) Unwrap() error { return e.cause }
