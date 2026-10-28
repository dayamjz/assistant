package tasks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The state of a copy's entries, and moving an entry aside only as it was
// read: PRD section 6's "Removing a discarded copy", step 2, "each item is
// held to the state read before the loss was, so an item changed since
// refuses". The state is the bytes: a file rewritten with the bytes it
// held, or its mode set to what it was, is not a change. Tests compare it
// before and after what they say changes nothing.
//
// A state is refused when an entry is seen to change while it is read:
// each entry's stamp (stampOf) is read before its bytes or, for a
// directory, its listing, and once all are read every stamp is read again
// and every directory listed again, and a difference is ErrLossChanged.
// That sees only a change the file system records in an entry's stamp or
// a directory's listing. What it does not cover: a change that leaves the
// stamp as it was goes unseen, and the state read can then be bytes the
// file never held whole. Two such changes are known. A write through a
// shared writable memory mapping of the file left its stamp as it was
// during the read (reproduced on macOS in the review of e2adb53), and no
// call this package makes can tell that another process holds a file open
// for writing or mapped, so it is neither refused nor seen. And a file
// rewritten in place with as many bytes in the same timestamp tick as its
// last change (a move's rename among the changes).

// ErrLossChanged is a discard's refusal of a copy that no longer holds
// exactly what was read before its loss was: an entry it would set aside
// changed, was added to or went missing since, or was seen to change
// while it was read (readCopyState names what is not seen). What the
// discard set aside is put back.
var ErrLossChanged = errors.New("tasks: the copy no longer holds exactly what was read when its loss was, so the discard went no further")

// copyState is the state of every entry under some directories, by
// absolute path: its type and permission bits, and a regular
// file's bytes by their hash or a link's target.
type copyState map[string]stateEntry

// stateReadForTest, when set, is called once a state's entries are read
// and before their stamps are read again: the seam a test changes an
// entry through while it is read.
var stateReadForTest func()

// stateEntry is one entry of a copyState.
type stateEntry struct {
	mode fs.FileMode
	held string
}

// readCopyState reads the state of every entry under each root, the roots
// included, without following a link, and refuses with ErrLossChanged
// when, once all are read, an entry's stamp differs or a directory lists
// other names than were read under it.
func readCopyState(roots ...string) (copyState, error) {
	state, stamps := copyState{}, map[string]stamp{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if stamps[p], err = stampOf(p); err != nil {
				return err
			}
			state[p], err = stateOf(p)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("tasks: reading the state of %s: %w", root, err)
		}
	}
	if stateReadForTest != nil {
		stateReadForTest()
	}
	// Each entry read counts once under its parent, whichever roots reached
	// it, and a parent's count is asked only of a directory read, so a root
	// is counted only where it is a directory's entry, as an overlapping
	// root is.
	children := map[string]int{}
	for p := range state {
		if parent := filepath.Dir(p); parent != p {
			children[parent]++
		}
	}
	for p, was := range stamps {
		if now, err := stampOf(p); err != nil || now != was || !listsAsRead(p, state, children[p]) {
			return nil, fmt.Errorf("%w: %s changed while it was read", ErrLossChanged, p)
		}
	}
	return state, nil
}

// listsAsRead reports whether the entry at p, when state read it as a
// directory, lists exactly the n names state holds under it.
func listsAsRead(p string, state copyState, n int) bool {
	if !state[p].mode.IsDir() {
		return true
	}
	names, err := os.ReadDir(p)
	if err != nil || len(names) != n {
		return false
	}
	for _, e := range names {
		if _, ok := state[filepath.Join(p, e.Name())]; !ok {
			return false
		}
	}
	return true
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

// moveAsRead moves the entry at from, whole, to to, a name that must not
// stand yet (renameNoReplace, so nothing standing at to is ever replaced;
// to's parent is made first), and then holds what it moved to before, the
// state read of from beforehand: every entry it moved must have been read
// there with the same state, and every entry read under from must have
// moved. what names from in a refusal. It reports whether anything moved:
// nothing at from, with nothing read there, is no change and moves
// nothing. A difference is ErrLossChanged, reported after the move with
// moved true, so the caller, which journals the move before making it,
// puts it back.
//
// What it does not cover: a process writing through a file it opened
// before the move, or through a memory mapping of it, changes what moved
// unseen, after the state is read and, where the write leaves the stamp
// as it was, during the read too (readCopyState's gap).
func moveAsRead(from, to string, before copyState, what string) (bool, error) {
	_, had := before[from]
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return false, err
	}
	if err := renameNoReplace(from, to); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if !had {
				return false, nil
			}
			return false, fmt.Errorf("%w: %s is gone from the copy", ErrLossChanged, what)
		}
		return false, err
	}
	now, err := readCopyState(to)
	if err != nil {
		return true, err
	}
	for p, s := range now {
		was, ok := before[filepath.Join(from, strings.TrimPrefix(p, to))]
		if !ok || was != s {
			return true, fmt.Errorf("%w: %s is not what the copy held when its loss was read", ErrLossChanged,
				filepath.Join(what, strings.TrimPrefix(p, to)))
		}
	}
	for p := range before {
		if p == from || strings.HasPrefix(p, from+string(filepath.Separator)) {
			if _, ok := now[filepath.Join(to, strings.TrimPrefix(p, from))]; !ok {
				return true, fmt.Errorf("%w: %s is gone from the copy", ErrLossChanged, filepath.Join(what, strings.TrimPrefix(p, from)))
			}
		}
	}
	return true, nil
}
