package tasks

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The state of a copy's entries, which a discard holds what it sets aside
// to (kept_aside.go): an entry set aside must hold what the copy held when
// the loss was read, and a path the discard wrote is taken out again only
// while it holds what was written. The state is the bytes: a file
// rewritten with the bytes it held, or its mode set to what it was, is not
// a change.

// copyState is the state of every entry under some directories, by
// absolute path: its type and permission bits, and a regular
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
