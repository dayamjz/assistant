package tasks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// ErrConfirmMismatch is returned by Discard when digest is not the one
// Loss seals for the copy now, so the confirmation is of another loss.
var ErrConfirmMismatch = errors.New("tasks: the confirmed digest is not what discarding the copy would lose now")

// ErrDiscardStateUnreadable is returned when a standing discard's journal
// has a ponr step but neither the copy's path nor the journalled move
// target stands, and finish was never journalled, so which side of the
// point of no return the discard is on cannot be verified (P6).
var ErrDiscardStateUnreadable = errors.New("tasks: which side of the discard's point of no return the copy is on cannot be read")

// Discard removes the entry's copy when digest is exactly what Loss seals
// for it now (PRD section 6's "Removing a discarded copy"): verify, re-read
// the loss, set working-tree loss aside, clean-read, then either move a
// task's clone or remove a run's worktree at the point of no return, and
// delete what remains. It does not record the discard's claim or outcome.
// On any refusal before the point of no return it puts everything back and
// clears the discard directory so a later confirmation can begin; past that
// point it never puts back and only finishes deletion (a worktree's remains
// by force). A stop is taken up on the next call from where the copy stands
// and from the journal. The discard directory remains after a finished
// removal for the outcome owner to delete. ErrLossChanged is a copy that
// moved under the discard after the confirmed read.
func (c *Cleaner) Discard(ctx context.Context, entry store.KeptWork, gatePath, digest string) error {
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
		// A run's worktree PONR is also the gate record and the step-3
		// tree (PRD step 4): path alone can still stand after a partial
		// remove, and an unreadable re-read leaves the claim owed.
		if !past && worktreePONR(j) {
			begun, readErr := c.worktreeRemovalBegun(ctx, gatePath, entry.CopyPath)
			if readErr != nil {
				return fmt.Errorf("%w: %w", ErrDiscardStateUnreadable, readErr)
			}
			past = begun
		}
		if past {
			return c.finishDiscard(ctx, gatePath, dir, &j)
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
			return fmt.Errorf("%w (and putting back: %w)", err, pb)
		}
		if rm := os.RemoveAll(dir); rm != nil {
			return fmt.Errorf("%w (and clearing the discard directory: %w)", err, rm)
		}
		return err
	}
	if err := c.setAsideLoss(ctx, dir, &last, entry.CopyPath, loss); err != nil {
		return refuse(err)
	}
	if err := c.discardClean(ctx, entry, gatePath, loss); err != nil {
		return refuse(err)
	}

	if !entry.TaskID.IsKnown() {
		return c.removeWorktreeAtPONR(ctx, entry, gatePath, dir, &last, refuse)
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
			return fmt.Errorf("%w (and striking ponr: %w)", moveErr, strikeErr)
		}
		return refuse(moveErr)
	}
	// Past the point of no return: never putBack.
	return c.finishDiscard(ctx, gatePath, dir, &last)
}

// removeWorktreeAtPONR journals ponr and removes the run's worktree through
// the non-force RemoveWorktree under the gate's copies lock. When git
// refuses, the worktree is read again (PRD step 4): path, gate record, and
// the step-3 tree. All still as step 3 left them is a refusal before PONR
// (strike and put back). Anything already gone is past PONR and finish
// removes remains by force. A reading that fails leaves the claim owed
// (no put-back, no finish).
func (c *Cleaner) removeWorktreeAtPONR(ctx context.Context, entry store.KeptWork, gatePath, dir string, last *discardJournal, refuse func(error) error) error {
	if gatePath == "" {
		return refuse(fmt.Errorf("tasks: Discard of a run's worktree needs the gate path"))
	}
	ponr := discardStep{Kind: discardStepPONR, From: entry.CopyPath}
	if err := appendDiscardStep(dir, last, ponr); err != nil {
		return refuse(err)
	}
	bare, err := vcs.OpenBare(ctx, gatePath, c.git...)
	if err != nil {
		if strikeErr := strikeUnreachedRemovalSteps(dir, last, entry.CopyPath); strikeErr != nil {
			return refuse(fmt.Errorf("tasks: opening the gate at %s: %w (and striking ponr: %w)", gatePath, err, strikeErr))
		}
		return refuse(fmt.Errorf("tasks: opening the gate at %s: %w", gatePath, err))
	}
	lock, err := gate.LockCopies(ctx, bare.Path())
	if err != nil {
		if strikeErr := strikeUnreachedRemovalSteps(dir, last, entry.CopyPath); strikeErr != nil {
			return refuse(fmt.Errorf("tasks: waiting to remove the worktree at %s: %w (and striking ponr: %w)", entry.CopyPath, err, strikeErr))
		}
		return refuse(fmt.Errorf("tasks: waiting to remove the worktree at %s: %w", entry.CopyPath, err))
	}
	defer func() { _ = lock.Release() }()
	if err := bare.RemoveWorktree(ctx, entry.CopyPath); err != nil {
		begun, readErr := c.worktreeRemovalBegunAt(ctx, bare, entry.CopyPath)
		if readErr != nil {
			// Leave the journal and discard directory: the claim stays owed.
			return fmt.Errorf("%w: after refusing to remove the worktree at %s: %w", ErrDiscardStateUnreadable, entry.CopyPath, readErr)
		}
		if !begun {
			if strikeErr := strikeUnreachedRemovalSteps(dir, last, entry.CopyPath); strikeErr != nil {
				return fmt.Errorf("tasks: removing the worktree at %s: %w (and striking ponr: %w)", entry.CopyPath, err, strikeErr)
			}
			return refuse(fmt.Errorf("tasks: removing the worktree at %s: %w", entry.CopyPath, err))
		}
		return c.finishDiscard(ctx, gatePath, dir, last)
	}
	return c.finishDiscard(ctx, gatePath, dir, last)
}

// worktreePONR reports whether j's ponr step is a run's worktree removal
// (empty To), not a clone rename.
func worktreePONR(j discardJournal) bool {
	for _, s := range j.Steps {
		if s.Kind == discardStepPONR {
			return s.To == ""
		}
	}
	return false
}

// worktreeRemovalBegun reports whether a run's worktree discard is past its
// point of no return after git's non-force remove refused or stopped, on
// PRD step 4's re-read: the path, the gate's worktree record, and the
// step-3 tree. gatePath is required; a read failure is returned as err so
// the claim stays owed.
func (c *Cleaner) worktreeRemovalBegun(ctx context.Context, gatePath, copyPath string) (bool, error) {
	if gatePath == "" {
		return false, fmt.Errorf("tasks: reading a worktree discard's point of no return needs the gate path")
	}
	bare, err := vcs.OpenBare(ctx, gatePath, c.git...)
	if err != nil {
		return false, err
	}
	return c.worktreeRemovalBegunAt(ctx, bare, copyPath)
}

// worktreeRemovalBegunAt is worktreeRemovalBegun with the gate already open.
func (c *Cleaner) worktreeRemovalBegunAt(ctx context.Context, bare *vcs.Repository, copyPath string) (bool, error) {
	info, err := os.Lstat(copyPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	case err != nil:
		return false, err
	case !info.IsDir():
		// Path no longer a worktree directory: removal had begun.
		return true, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(copyPath))
	if err != nil {
		return false, err
	}
	worktree := filepath.Join(parent, filepath.Base(copyPath))
	records, err := bare.WorktreeRecords(ctx)
	if err != nil {
		return false, err
	}
	hasRecord := false
	for _, r := range records {
		if r.Worktree == worktree {
			hasRecord = true
			break
		}
	}
	if !hasRecord {
		return true, nil
	}
	if _, err := copyRecord(ctx, bare, copyPath); err != nil {
		if errors.Is(err, ErrRecordNotTheCopys) {
			// Structure no longer matches the gate record: removal had begun.
			return true, nil
		}
		return false, err
	}
	copied, err := vcs.OpenWorktree(ctx, copyPath, c.git...)
	if err != nil {
		return false, err
	}
	inv, err := copied.Inventory(ctx)
	if err != nil {
		return false, err
	}
	if inv.Dirty || inv.Untracked {
		// Tree no longer as step 3 left it: removal had begun.
		return true, nil
	}
	return false, nil
}

// pathStands reports whether path names something that exists.
func pathStands(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// pastPointOfNoReturn reports whether the discard is past its point of no
// return from where the copy stands (PRD), not the journal alone. A task
// clone: at the journalled ponr target is past; a journalled finish is past
// even when something stands at copyPath; at copyPath without finish is
// not; ponr with neither path standing and no finish is
// ErrDiscardStateUnreadable. A run's worktree (ponr with empty To):
// finished or the path gone is past; the path still standing is not past
// on the path alone — Discard also asks worktreeRemovalBegun for the gate
// record and step-3 tree before treating a standing path as before PONR.
func pastPointOfNoReturn(copyPath string, j discardJournal) (bool, error) {
	var to string
	ponr := false
	finished := false
	for _, s := range j.Steps {
		switch s.Kind {
		case discardStepPONR:
			ponr = true
			to = s.To
		case discardStepFinish:
			finished = true
		}
	}
	if !ponr {
		return false, nil
	}
	if to == "" {
		if finished {
			return true, nil
		}
		_, fromErr := os.Lstat(copyPath)
		switch {
		case fromErr == nil:
			return false, nil
		case !errors.Is(fromErr, fs.ErrNotExist):
			return false, fromErr
		default:
			return true, nil
		}
	}
	_, toErr := os.Lstat(to)
	switch {
	case toErr == nil:
		// Clone stands in the discard directory: past, even if something
		// else now stands at the original path.
		return true, nil
	case !errors.Is(toErr, fs.ErrNotExist):
		return false, toErr
	}
	if finished {
		// Finish already journalled: past. Retry deletes even when an
		// impostor stands at the original path or both paths are gone.
		return true, nil
	}
	_, fromErr := os.Lstat(copyPath)
	switch {
	case fromErr == nil:
		// Still at its path, and never arrived at To: not past.
		return false, nil
	case !errors.Is(fromErr, fs.ErrNotExist):
		return false, fromErr
	default:
		// ponr journalled, rename never observed, both paths gone: refuse
		// rather than finish-delete set-asides or put back into nothing.
		return false, fmt.Errorf("%w: nothing stands at %s or %s", ErrDiscardStateUnreadable, copyPath, to)
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
// needed, delete the moved clone or force-remove a worktree that still
// stands, and clear the set-aside tree. The discard directory itself stays
// for the outcome owner. Never putBack.
func (c *Cleaner) finishDiscard(ctx context.Context, gatePath, dir string, last *discardJournal) error {
	if last == nil {
		return fmt.Errorf("tasks: finishDiscard with no journal")
	}
	var movedTo, from string
	for _, s := range last.Steps {
		if s.Kind == discardStepPONR {
			movedTo = s.To
			from = s.From
		}
	}
	if from == "" && movedTo == "" {
		return fmt.Errorf("tasks: finishDiscard with no ponr step in the journal")
	}
	if !journalHasStep(*last, discardStepFinish) {
		if err := appendDiscardStep(dir, last, discardStep{Kind: discardStepFinish}); err != nil {
			return err
		}
	}
	if movedTo != "" {
		if err := os.RemoveAll(movedTo); err != nil {
			return fmt.Errorf("tasks: removing the discarded clone at %s: %w", movedTo, err)
		}
	} else if pathStands(from) {
		if gatePath == "" {
			return fmt.Errorf("tasks: finishing a worktree discard at %s needs the gate path", from)
		}
		bare, err := vcs.OpenBare(ctx, gatePath, c.git...)
		if err != nil {
			return fmt.Errorf("tasks: opening the gate at %s to finish the worktree discard: %w", gatePath, err)
		}
		lock, err := gate.LockCopies(ctx, bare.Path())
		if err != nil {
			return fmt.Errorf("tasks: waiting to finish removing the worktree at %s: %w", from, err)
		}
		defer func() { _ = lock.Release() }()
		if err := bare.RemoveWorktreeDiscardingChanges(ctx, from); err != nil {
			// Gate record may already be gone (removal begun by pruning the
			// record); the path remains are still removed by force.
			if rm := os.RemoveAll(from); rm != nil {
				return fmt.Errorf("tasks: force-removing the worktree at %s past its point of no return: %w (and removing remains: %w)", from, err, rm)
			}
		}
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
