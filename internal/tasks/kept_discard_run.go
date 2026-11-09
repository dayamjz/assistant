package tasks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// ErrConfirmMismatch is returned by Discard when digest is not the one
// Loss seals for the copy now, so the confirmation is of another loss.
var ErrConfirmMismatch = errors.New("tasks: the confirmed digest is not what discarding the copy would lose now")

// Discard removes the entry's copy when digest is exactly what Loss seals
// for it now (PRD section 6's "Removing a discarded copy" for a task's
// clone): verify, re-read the loss, set working-tree loss aside, clean-read,
// move the clone at the point of no return, and delete what remains. It
// does not record the discard's claim or outcome. A run's worktree is
// refused until a later link. On any refusal before the point of no return
// it puts everything back and clears the discard directory so a later
// confirmation can begin; past that point it never puts back and only
// finishes deletion. A stop past the point of no return is taken up on the
// next call by reading the copy's path, never the journal alone. The
// discard directory remains after a finished removal for the outcome owner
// to delete. ErrLossChanged is a copy that moved under the discard after
// the confirmed read.
func (c *Cleaner) Discard(ctx context.Context, entry store.KeptWork, gatePath, digest string) error {
	if !entry.TaskID.IsKnown() {
		return fmt.Errorf("tasks: Discard of a run's worktree is not yet implemented")
	}
	dir, err := c.home.Discarding(entry.CopyPath)
	if err != nil {
		return err
	}
	j, standing, err := c.standingDiscard(entry.CopyPath)
	if err != nil {
		return err
	}
	if standing {
		past, err := pastPointOfNoReturn(entry.CopyPath, j)
		if err != nil {
			return err
		}
		if past {
			return c.finishDiscard(dir, &j)
		}
		if err := strikeUnreachedRemovalSteps(dir, &j, entry.CopyPath); err != nil {
			return err
		}
		if err := putBack(dir, j); err != nil {
			return err
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("tasks: clearing a discard directory after put-back at %s: %w", dir, err)
		}
	}

	if err := c.VerifyKeptCopy(ctx, entry, gatePath); err != nil {
		return err
	}
	if err := c.supportedLayout(ctx, entry, gatePath); err != nil {
		return err
	}
	loss, err := c.Loss(ctx, entry, gatePath)
	if err != nil {
		return err
	}
	if loss.Digest != digest {
		return fmt.Errorf("%w: confirmed %s, copy seals %s", ErrConfirmMismatch, digest, loss.Digest)
	}

	dir, err = c.beginDiscard(entry.CopyPath)
	if err != nil {
		return err
	}
	last, found, err := c.standingDiscard(entry.CopyPath)
	if err != nil || !found {
		return fmt.Errorf("tasks: reading the journal just begun: found %v: %w", found, err)
	}
	refuse := func(err error) error {
		if err == nil {
			return nil
		}
		if pb := putBack(dir, last); pb != nil {
			return fmt.Errorf("%w (and putting back: %v)", err, pb)
		}
		if rm := os.RemoveAll(dir); rm != nil {
			return fmt.Errorf("%w (and clearing the discard directory: %v)", err, rm)
		}
		return err
	}
	if err := c.setAsideLoss(ctx, dir, &last, entry.CopyPath, loss); err != nil {
		return refuse(err)
	}
	if err := c.discardClean(ctx, entry, gatePath, loss); err != nil {
		return refuse(err)
	}

	to := filepath.Join(dir, "clone")
	ponr := discardStep{Kind: discardStepPONR, From: entry.CopyPath, To: to}
	if err := appendDiscardStep(dir, &last, ponr); err != nil {
		return refuse(err)
	}
	if err := renameNoReplace(entry.CopyPath, to); err != nil {
		// Rename failed: still before the point of no return; strike ponr and put back.
		moveErr := fmt.Errorf("tasks: moving the clone to %s: %w", to, err)
		if strikeErr := strikeUnreachedRemovalSteps(dir, &last, entry.CopyPath); strikeErr != nil {
			return fmt.Errorf("%v (and striking ponr: %v)", moveErr, strikeErr)
		}
		return refuse(moveErr)
	}
	// Past the point of no return: never putBack.
	return c.finishDiscard(dir, &last)
}

// pastPointOfNoReturn reports whether the copy has left its path after a
// journalled ponr, reading the path and not the journal alone (PRD).
func pastPointOfNoReturn(copyPath string, j discardJournal) (bool, error) {
	var sawPONR bool
	for _, s := range j.Steps {
		if s.Kind == discardStepPONR {
			sawPONR = true
		}
	}
	if !sawPONR {
		return false, nil
	}
	_, err := os.Lstat(copyPath)
	switch {
	case err == nil:
		return false, nil
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	default:
		return false, err
	}
}

// strikeUnreachedRemovalSteps drops trailing ponr/finish steps while the
// copy still stands at copyPath, so putBack can reverse the set-aside
// (those kinds are not reversed, and a journalled ponr before a failed
// rename never moved anything).
func strikeUnreachedRemovalSteps(dir string, j *discardJournal, copyPath string) error {
	if _, err := os.Lstat(copyPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for len(j.Steps) > 0 {
		last := j.Steps[len(j.Steps)-1]
		if last.Kind != discardStepPONR && last.Kind != discardStepFinish {
			return nil
		}
		next := *j
		next.Steps = append([]discardStep(nil), j.Steps[:len(j.Steps)-1]...)
		if err := writeJournal(dir, j, next); err != nil {
			return err
		}
		*j = next
	}
	return nil
}

// discardClean is step 3 after set-aside: the working tree reads clean
// (tracked and untracked, ignored included via a fresh Loss), and the copy
// loses nothing the confirmed loss did not name. Confirmed Lacks may still
// be named by HEAD until the whole clone moves (HEAD/ref set-aside is a
// later link).
func (c *Cleaner) discardClean(ctx context.Context, entry store.KeptWork, gatePath string, confirmed Loss) error {
	copied, err := vcs.OpenWorktree(ctx, entry.CopyPath, c.git...)
	if err != nil {
		return fmt.Errorf("tasks: opening the kept copy at %s after set-aside: %w", entry.CopyPath, err)
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return fmt.Errorf("tasks: reading the kept copy at %s after set-aside: %w", entry.CopyPath, err)
	}
	if inv.Dirty || inv.Untracked {
		return fmt.Errorf("%w: the copy still holds a tracked change or untracked file after set-aside", ErrLossChanged)
	}
	again, err := c.Loss(ctx, entry, gatePath)
	if err != nil {
		return err
	}
	for _, lack := range again.Lacks {
		if !slices.Contains(confirmed.Lacks, lack) {
			return fmt.Errorf("%w: the copy now loses commit %s, which the confirmation did not name", ErrLossChanged, lack)
		}
	}
	for _, o := range again.Objects {
		if !slices.ContainsFunc(confirmed.Objects, func(c LostObject) bool { return c.Object == o.Object }) {
			return fmt.Errorf("%w: the copy now loses object %s, which the confirmation did not name", ErrLossChanged, o.Object)
		}
	}
	for _, f := range again.Files {
		switch f.Kind {
		case LostRepository:
			if !slices.Contains(confirmed.Files, f) {
				return fmt.Errorf("%w: the copy now loses repository path %s, which the confirmation did not name", ErrLossChanged, f.Path)
			}
		default:
			// Working-tree and index paths should have been set aside; any
			// still lost (an ignored file included) is unconfirmed.
			return fmt.Errorf("%w: the copy still loses %s %s after set-aside", ErrLossChanged, f.Kind, f.Path)
		}
	}
	return nil
}

// finishDiscard is step 5 past the point of no return: journal finish if
// needed, delete the moved clone and the set-aside tree. The discard
// directory itself stays for the outcome owner. Never putBack.
func (c *Cleaner) finishDiscard(dir string, last *discardJournal) error {
	if last == nil {
		return fmt.Errorf("tasks: finishDiscard with no journal")
	}
	var movedTo string
	for _, s := range last.Steps {
		if s.Kind == discardStepPONR {
			movedTo = s.To
		}
	}
	if movedTo == "" {
		return fmt.Errorf("tasks: finishDiscard with no ponr step in the journal")
	}
	if !journalHasStep(*last, discardStepFinish) {
		if err := appendDiscardStep(dir, last, discardStep{Kind: discardStepFinish}); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(movedTo); err != nil {
		return fmt.Errorf("tasks: removing the discarded clone at %s: %w", movedTo, err)
	}
	paths := filepath.Join(dir, "paths")
	if err := os.RemoveAll(paths); err != nil {
		return fmt.Errorf("tasks: removing the discard's set-aside paths at %s: %w", paths, err)
	}
	return nil
}

// journalHasStep reports whether j records a step of kind.
func journalHasStep(j discardJournal, kind discardStepKind) bool {
	for _, s := range j.Steps {
		if s.Kind == kind {
			return true
		}
	}
	return false
}
