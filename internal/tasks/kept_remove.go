package tasks

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// A discard's removal deletes the copy only as it was read: Discard reads
// the state of everything the removal destroys (copyState) before its last
// read of the loss and again once every check has passed, refuses with
// ErrLossChanged when the two differ in anything but what the loss read
// itself may write (objectsAdded), and then removes each entry only while
// it is the entry the state recorded (removeAsRead). So a file written,
// changed, linked or removed after the read the digest was checked on,
// by any process, refuses the discard rather than being destroyed unshown.
//
// What it does not cover: a process that opened a file in the copy before
// that file is moved aside for its removal can still write to it through
// that descriptor after it is checked and before it is unlinked, and what
// it writes then is lost with the file; nothing a path names reaches it
// from the moment it is moved aside. A refusal while entries are being
// removed leaves the ones already removed gone, each of them checked as
// read and so shown, and everything from the entry that differed on as
// it stands. A directory named .git is removed after everything beside
// it, so a refusal before the removal reaches it leaves a copy that still
// reads as a repository; one inside it, or at the copy's own directory
// once it is empty but for what was made in it, leaves one that does not.
// And the state is the bytes: a file rewritten with the bytes it held, or
// its mode set to what it was, is not a change.

// copyState is the state of every entry under the directories a removal
// destroys, by absolute path: its type and permission bits, and a regular
// file's bytes by their hash or a link's target.
type copyState map[string]stateEntry

// stateEntry is one entry of a copyState.
type stateEntry struct {
	mode fs.FileMode
	held string
}

// readCopyState reads the state of every entry under each root, the roots
// included, without following a link.
func readCopyState(roots ...string) (copyState, error) {
	state := copyState{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			entry, err := stateOf(p)
			if err != nil {
				return err
			}
			state[p] = entry
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("tasks: reading the state of %s: %w", root, err)
		}
	}
	return state, nil
}

// stateOf reads one entry's state, without following a link.
func stateOf(p string) (stateEntry, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return stateEntry{}, err
	}
	entry := stateEntry{mode: info.Mode().Type() | info.Mode().Perm()}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		entry.held, err = os.Readlink(p)
	case info.Mode().IsRegular():
		entry.held, err = hashFile(p)
	}
	return entry, err
}

// under is the part of the state at root and below it.
func (was copyState) under(root string) copyState {
	out := copyState{}
	prefix := root + string(filepath.Separator)
	for p, entry := range was {
		if p == root || strings.HasPrefix(p, prefix) {
			out[p] = entry
		}
	}
	return out
}

// changedSince names every way now differs from was, an entry added,
// removed or changed, leaving out an entry added that appendOnly says the
// loss read itself may add; an empty result is no change.
func (was copyState) changedSince(now copyState, appendOnly func(string) bool) []string {
	var out []string
	for p, entry := range now {
		before, ok := was[p]
		switch {
		case !ok && appendOnly(p):
		case !ok:
			out = append(out, p+" was added")
		case before != entry:
			out = append(out, p+" changed")
		}
	}
	for p := range was {
		if _, ok := now[p]; !ok {
			out = append(out, p+" was removed")
		}
	}
	slices.Sort(out)
	return out
}

// objectsAdded is appendOnly for a repository directory: what the loss
// read may add to it, through its fetch by identifier, is new files and
// directories under objects/, the objects it fetches among them, none of
// which names anything; a change to or a removal of one already there is
// still a change, so git's own maintenance, should the fetch start it and
// repack, refuses the discard, which fails toward keeping. An object nothing names is outside what a
// discard shows (kept_discard.go's header), so one another process adds
// meanwhile changes nothing here either; one a reference comes to name
// changes that reference, which is a change.
func objectsAdded(gitDir string) func(string) bool {
	objects := filepath.Join(gitDir, "objects") + string(filepath.Separator)
	return func(p string) bool { return strings.HasPrefix(p, objects) }
}

// removeAsRead removes root and everything under it, each entry only while
// it is what state recorded, children before their directory and a
// directory named .git after everything beside it. A file or link is moved
// aside within its directory first, so a writer opening its name from then
// on makes a new entry, and then checked and unlinked; an entry that is
// not as recorded is put back under its name, unless something has taken
// the name since, and refuses with ErrLossChanged. A directory is removed
// only once empty, so an entry made in it since the state was read
// refuses too. An entry already gone is not refused: it destroys nothing.
func removeAsRead(root string, state copyState) error {
	return remover{}.removeEntry(root, state)
}

// remover is removeAsRead's walk. afterListing, when not nil, runs once a
// directory's entries are listed and before they are removed; it is nil
// but in this package's tests, which use it to make an entry the listing
// did not see.
type remover struct {
	afterListing func(dir string)
}

func (r remover) removeEntry(p string, state copyState) error {
	info, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	want, ok := state[p]
	if !ok {
		return fmt.Errorf("%w: %s was not there when the copy was read", ErrLossChanged, p)
	}
	if !info.IsDir() {
		return removeFileAsRead(p, want)
	}
	if want.mode.Type() != fs.ModeDir {
		return fmt.Errorf("%w: %s is a directory now", ErrLossChanged, p)
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return err
	}
	slices.SortStableFunc(entries, func(a, b fs.DirEntry) int {
		switch {
		case a.Name() == ".git" && b.Name() != ".git":
			return 1
		case b.Name() == ".git" && a.Name() != ".git":
			return -1
		}
		return 0
	})
	if r.afterListing != nil {
		r.afterListing(p)
	}
	for _, e := range entries {
		if err := r.removeEntry(filepath.Join(p, e.Name()), state); err != nil {
			return err
		}
	}
	if err := os.Remove(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%w: %s could not be removed, since something was put in it after the copy was read: %w", ErrLossChanged, p, err)
	}
	return nil
}

// removeFileAsRead is removeEntry for anything but a directory.
func removeFileAsRead(p string, want stateEntry) error {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	aside := filepath.Join(filepath.Dir(p), ".assistant-discard-"+hex.EncodeToString(suffix[:]))
	if err := os.Rename(p, aside); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	now, err := stateOf(aside)
	if err == nil && now == want {
		return os.Remove(aside)
	}
	why := fmt.Sprintf("%s changed after the copy was read", p)
	if err != nil {
		why = fmt.Sprintf("%s could not be read as it was moved aside: %v", p, err)
	}
	if restoreErr := renameNoReplace(aside, p); restoreErr != nil {
		return fmt.Errorf("%w: %s, and it was left at %s, since it could not be put back: %w", ErrLossChanged, why, aside, restoreErr)
	}
	return fmt.Errorf("%w: %s", ErrLossChanged, why)
}
