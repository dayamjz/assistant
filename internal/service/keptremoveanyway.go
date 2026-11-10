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
// the discard refuses and what of the loss could be read. With Force,
// from a terminal's foreground and naming the same task or run, it claims
// the entry, records the release, and redispatches. It never removes the
// copy (decision 31).

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

// showRemoveAnyway reports why a discard refuses and what of the loss
// could be read, recording nothing.
func (s *Service) showRemoveAnyway(ctx context.Context, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	if err := s.refuseRemoveAnywayWhileBusy(entry); err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	refusal, shown, err := s.removeAnywayProbe(ctx, entry)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	out := machine.KeptWorkAnswered{
		Entry:   entry,
		Effect:  machine.KeptWorkRemoveAnywayShown,
		Refusal: refusal,
	}
	if shown != nil {
		out.Loss = shown
	}
	return out, nil
}

// confirmRemoveAnyway claims and releases the entry on a Force
// confirmation, keeping the copy.
func (s *Service) confirmRemoveAnyway(ctx context.Context, req ipc.Request, entry store.KeptWork) (machine.KeptWorkAnswered, error) {
	if err := s.refuseRemoveAnywayWhileBusy(entry); err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	refusal, shown, err := s.removeAnywayProbe(ctx, entry)
	if err != nil {
		return machine.KeptWorkAnswered{}, err
	}
	commits := entry.Holds
	if shown != nil && len(shown.Lacks) > 0 {
		commits = shown.Lacks
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
	claimed, err := s.store.ClaimKeptWorkRemoveAnyway(ctx, entry.ID, refusal, commits, req.HoldResolver())
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
	// Re-probe: a refusal that no longer holds takes the claim back.
	if _, _, err := s.removeAnywayProbe(ctx, entry); err != nil {
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

// refuseRemoveAnywayWhileBusy refuses while a discard claim or a discard
// set-aside stands beside the copy (PRD section 6). A standing
// remove-anyway claim is completed on Force, not refused here.
func (s *Service) refuseRemoveAnywayWhileBusy(entry store.KeptWork) error {
	if entry.DiscardClaimed() {
		return fmt.Errorf("%w: kept work entry %d was not released: a discard claim stands on it; finish or withdraw that discard first",
			ipc.ErrRefused, entry.ID)
	}
	if discardDirectoryStands(s, entry.CopyPath) {
		return fmt.Errorf("%w: kept work entry %d was not released: a discard set aside still stands beside the copy",
			ipc.ErrRefused, entry.ID)
	}
	return nil
}

// removeAnywayProbe decides whether remove-anyway is offered: a discard
// that can read and remove the copy refuses naming discard; otherwise the
// discard refusal and any partial shown loss are returned.
func (s *Service) removeAnywayProbe(ctx context.Context, entry store.KeptWork) (refusal string, shown *machine.KeptWorkLoss, err error) {
	gatePath, err := s.keptGatePath(ctx, entry)
	if err != nil {
		return "", nil, err
	}
	if verifyErr := s.cleaner.VerifyKeptCopy(ctx, entry, gatePath); verifyErr != nil {
		switch {
		case errors.Is(verifyErr, tasks.ErrCopyGone),
			errors.Is(verifyErr, tasks.ErrRecordNotTheCopys),
			errors.Is(verifyErr, tasks.ErrLayoutUnsupported):
			return fmt.Sprintf("discard refused: %v", verifyErr), nil, nil
		default:
			return "", nil, verifyErr
		}
	}
	_, lossErr := s.cleaner.Loss(ctx, entry, gatePath)
	if lossErr == nil {
		return "", nil, fmt.Errorf("%w: kept work entry %d was not released: a discard can remove the copy; confirm that discard instead of remove-anyway",
			ipc.ErrRefused, entry.ID)
	}
	switch {
	case errors.Is(lossErr, tasks.ErrLossUnestablished),
		errors.Is(lossErr, tasks.ErrLayoutUnsupported),
		errors.Is(lossErr, tasks.ErrRecordNotTheCopys),
		errors.Is(lossErr, tasks.ErrCopyGone):
		partial := machine.KeptWorkLoss{Lacks: append([]string(nil), entry.Holds...)}
		return fmt.Sprintf("discard refused: %v", lossErr), &partial, nil
	default:
		return "", nil, lossErr
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
