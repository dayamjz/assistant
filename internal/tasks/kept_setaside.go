package tasks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/vcs"
)

// setAsideLoss sets aside the working-tree paths a Loss shows into the
// discard's directory, journalled before each step (PRD section 6,
// "Removing a discarded copy", step 1's path half). LostChanged and
// LostNew are moved as read; LostDeleted needs no move. Then each
// LostChanged or LostDeleted path that is missing from the working tree
// is written back from its stage-0 index blob, journalled with the exact
// bytes first, never replacing anything that stands. A missing tracked
// path the loss did not name as changed or deleted is refused
// (ErrLossChanged): a deletion nobody was shown. LostRepository paths
// are left in place: a task's clone removes its repository directory with
// the clone at the point of no return, and a run's worktree leaves the
// gate's. LostStaged and HEAD/ref set-aside are refused here until later
// removal links handle them. A path that changed since the loss was read
// is ErrLossChanged; putBack undoes what was journalled.
//
// last must be the journal beginDiscard or standingDiscard last wrote;
// on success it holds every step this call appended.
func (c *Cleaner) setAsideLoss(ctx context.Context, dir string, last *discardJournal, copyPath string, loss Loss) error {
	if last == nil {
		return fmt.Errorf("tasks: setAsideLoss with no journal last written")
	}
	heldOf := map[LostPath]string{}
	for _, f := range loss.files {
		heldOf[f.LostPath] = f.held
	}
	restore := map[string]bool{}
	for _, f := range loss.Files {
		switch f.Kind {
		case LostRepository:
			continue
		case LostStaged:
			return fmt.Errorf("tasks: setAsideLoss does not yet set aside a staged path (%s)", f.Path)
		case LostDeleted:
			restore[f.Path] = true
			continue
		case LostChanged, LostNew:
			held, ok := heldOf[f]
			if !ok {
				return fmt.Errorf("tasks: setAsideLoss: loss names %s (%s) with no sealed content", f.Kind, f.Path)
			}
			if err := setAsidePath(dir, last, copyPath, f, held); err != nil {
				return err
			}
			if f.Kind == LostChanged {
				restore[f.Path] = true
			}
		default:
			return fmt.Errorf("tasks: setAsideLoss: unrecognized loss kind %q", f.Kind)
		}
	}
	copied, err := vcs.OpenWorktree(ctx, copyPath, c.git...)
	if err != nil {
		return fmt.Errorf("tasks: opening the kept copy at %s for write-back: %w", copyPath, err)
	}
	return writeBackMissingIndex(ctx, copied, dir, last, copyPath, restore)
}

// setAsidePath journals a move of the loss path under copyPath into
// dir/paths and moves it only when the one state read for the move still
// holds what the loss sealed (so a change between a seal check and the
// move's before cannot smuggle unconfirmed bytes into the aside).
func setAsidePath(dir string, last *discardJournal, copyPath string, f LostPath, sealed string) error {
	if strings.HasSuffix(f.Path, "/") {
		return fmt.Errorf("tasks: setAsideLoss does not yet set aside a directory path (%s)", f.Path)
	}
	if f.Path == "" || strings.Contains(f.Path, "..") {
		return fmt.Errorf("tasks: setAsideLoss: refused path %q", f.Path)
	}
	from := filepath.Join(copyPath, filepath.FromSlash(f.Path))
	to := filepath.Join(dir, "paths", filepath.FromSlash(f.Path))
	before, err := readCopyState(from)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s is gone from the copy", ErrLossChanged, f.Path)
		}
		return err
	}
	if err := stateHoldsSealed(before, from, f.Path, sealed); err != nil {
		return err
	}
	step := discardStep{Kind: discardStepMove, From: from, To: to}
	if err := appendDiscardStep(dir, last, step); err != nil {
		return err
	}
	moved, err := moveAsRead(from, to, before, f.Path)
	if err != nil {
		return err
	}
	if !moved {
		return fmt.Errorf("%w: %s is gone from the copy", ErrLossChanged, f.Path)
	}
	return nil
}

// stateHoldsSealed reports whether the state already read for path still
// holds what the loss sealed for rel, on lostPaths' terms for a single
// regular or link entry.
func stateHoldsSealed(before copyState, path, rel, sealed string) error {
	entry, ok := before[path]
	if !ok {
		return fmt.Errorf("%w: %s is gone from the copy", ErrLossChanged, rel)
	}
	switch {
	case entry.mode&fs.ModeSymlink != 0, entry.mode.IsRegular():
	default:
		return fmt.Errorf("tasks: setAsideLoss: %s is not a file or link", rel)
	}
	d := newDigest()
	d.record(rel, entryKindFromMode(entry.mode), gitModeFromMode(entry.mode), entry.held)
	if d.sum() != sealed {
		return fmt.Errorf("%w: %s is not what the copy held when its loss was read", ErrLossChanged, rel)
	}
	return nil
}

func entryKindFromMode(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeSymlink != 0:
		return "link"
	case mode.IsDir():
		return "dir"
	case mode.IsRegular():
		return "file"
	}
	return "other"
}

func gitModeFromMode(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeSymlink != 0:
		return "120000"
	case mode.IsRegular() && mode&0o100 != 0:
		return "100755"
	case mode.IsRegular():
		return "100644"
	}
	return ""
}

// shownForRestore reports whether path is a LostChanged or LostDeleted
// the set-aside named, including a file under a LostDeleted directory
// (loss names the directory once with a trailing slash).
func shownForRestore(restore map[string]bool, path string) bool {
	if restore[path] {
		return true
	}
	for shown := range restore {
		if strings.HasSuffix(shown, "/") && strings.HasPrefix(path, shown) {
			return true
		}
	}
	return false
}

// writeBackMissingIndex writes back each stage-0 index blob whose path is
// in restore (a LostChanged or LostDeleted the set-aside named, a file
// under a deleted directory among them) and whose working-tree path is
// missing, journalled first. A missing tracked path not in restore is
// ErrLossChanged. A path that stands meanwhile is ErrAsideStands.
// Symlinks and gitlinks are refused: this link writes regular files only.
func writeBackMissingIndex(ctx context.Context, copied *vcs.Repository, dir string, last *discardJournal, copyPath string, restore map[string]bool) error {
	entries, err := copied.IndexEntries(ctx)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Stage != 0 {
			continue
		}
		path := filepath.Join(copyPath, filepath.FromSlash(e.Path))
		_, err := os.Lstat(path)
		switch {
		case err == nil:
			continue
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
		if !shownForRestore(restore, e.Path) {
			return fmt.Errorf("%w: %s is missing and was not shown as a changed or deleted path to restore", ErrLossChanged, e.Path)
		}
		mode, err := regularIndexMode(e.Mode)
		if err != nil {
			return fmt.Errorf("tasks: writeBack of %s: %w", e.Path, err)
		}
		held, err := copied.Blob(ctx, e.Object)
		if err != nil {
			return fmt.Errorf("tasks: reading the index blob for %s: %w", e.Path, err)
		}
		step := discardStep{Kind: discardStepWriteBack, Path: path, Held: held}
		if err := appendDiscardStep(dir, last, step); err != nil {
			return err
		}
		if err := writeNewFile(path, held, mode); err != nil {
			return err
		}
	}
	return nil
}

// regularIndexMode is the file mode a regular index entry writes with.
// Symlinks (120000) and gitlinks (160000) are refused here.
func regularIndexMode(mode string) (os.FileMode, error) {
	switch mode {
	case "100644":
		return 0o644, nil
	case "100755":
		return 0o755, nil
	case "120000":
		return 0, fmt.Errorf("setAsideLoss does not yet write a symbolic link back from the index")
	case "160000":
		return 0, fmt.Errorf("setAsideLoss does not yet write a gitlink back from the index")
	default:
		return 0, fmt.Errorf("unrecognized index mode %s", mode)
	}
}

// writeNewFile creates path holding content with mode, never replacing
// anything that stands (PRD section 6: written without replacing).
func writeNewFile(path string, content []byte, mode os.FileMode) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%w at %s: something stands where the writeBack goes", ErrAsideStands, path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".discard-write-*")
	if err != nil {
		return err
	}
	partial := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(partial)
		return err
	}
	if _, err := f.Write(content); err != nil {
		return fail(err)
	}
	if err := f.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(partial)
		return err
	}
	if err := renameNoReplace(partial, path); err != nil {
		_ = os.Remove(partial)
		if errors.Is(err, fs.ErrExist) || existsAt(path) {
			return fmt.Errorf("%w at %s: something stands where the writeBack goes", ErrAsideStands, path)
		}
		return fmt.Errorf("tasks: writing back %s: %w", path, err)
	}
	return nil
}

func existsAt(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
