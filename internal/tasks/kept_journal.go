package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// A confirmed discard's own directory and its journal, PRD section 6's
// "Removing a discarded copy", step 2: a discard sets what it removes aside
// into a directory beside the copy (home.Home.Discarding) and writes there,
// before each step, what it is about to do, so a refusal or a stop can put
// everything back or finish. This file is that directory's lifecycle:
// opening one (beginDiscard), writing the journal so a stop leaves either
// the journal as it was or the new one whole (writeJournal), and finding
// one a discard left behind (standingDiscard). What a journal records of
// each item set aside, and how it is put back, is the set-aside's.
//
// The journal is written through writeWhole, this package's one way of
// writing a file whole, so a stop leaves either the last journal or the new
// one and at most a partial file beside it, never part of a journal. It is
// written before anything is set aside, so a directory with no journal
// holds nothing of the copy. The one thing it can hold then is a partial
// file of the first journal, which a discard that stopped during its first
// write leaves (the open finding a690cc38 began on feat/age-407-discard);
// standingDiscard clears that and the directory. Anything else in a
// directory with no journal is no file this program writes there, so it is
// left and refused, never removed.

// ErrAsideStands is returned for a copy beside which a discard's directory
// stands that this program cannot take up on its own: one holding files no
// discard writes there without a journal, an unreadable journal, or a
// directory where one is about to be made. Nothing in it is removed, and
// the error names where it is.
var ErrAsideStands = errors.New("tasks: a discard that did not finish left a directory beside the copy, so the copy is not read until it is dealt with")

// journalName is the journal's file name in a discard's directory.
const journalName = "journal.json"

// discardJournal is what a discard writes before each step. Copy is the
// identity (CopyIdentity's written form) of the copy the discard began on,
// read when its directory was made, so the copy can be told from anything
// put at its path once the discard has moved it.
type discardJournal struct {
	Copy string `json:"copy"`
}

// beginDiscard makes the discard's directory beside the copy at copyPath
// and writes its first journal, naming the copy's identity, before
// anything is set aside. A directory already standing there is refused
// with ErrAsideStands and left as it is: standingDiscard is what reads it.
// A first write that fails leaves the directory with at most a partial
// file of the journal, which standingDiscard clears.
func (c *Cleaner) beginDiscard(copyPath string) (string, error) {
	dir, err := c.home.Discarding(copyPath)
	if err != nil {
		return "", err
	}
	id, err := IdentifyCopy(copyPath)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("%w at %s: a discard's directory already stands there", ErrAsideStands, dir)
	} else if err != nil {
		return "", err
	}
	if err := writeJournal(dir, discardJournal{Copy: id.String()}); err != nil {
		return "", fmt.Errorf("tasks: writing the first journal of a discard at %s: %w", dir, err)
	}
	return dir, nil
}

// writeJournal replaces dir's journal with j through writeWhole, so the
// journal read afterwards is either the last one or j. A journal standing
// as a symbolic link is refused with ErrAsideStands and not written through.
func writeJournal(dir string, j discardJournal) error {
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if err := writeWhole(filepath.Join(dir, journalName), raw); errors.Is(err, ErrInstructionsLink) {
		return fmt.Errorf("%w at %s: its journal is a symbolic link", ErrAsideStands, dir)
	} else if err != nil {
		return err
	}
	return nil
}

// standingDiscard reads what a discard left beside the copy at copyPath.
// It reports false when no discard's directory stands there, or when one
// stood with no journal and only the partial files a stopped first write
// leaves, which it removes with the directory. It reports the journal and
// true for a directory with a journal naming a copy's identity, a partial
// file beside it ignored, since the journal is the last one written whole.
// Anything else (a journal that names no copy, a
// directory with no journal holding other files, a journal or a directory
// that is not a regular file or a directory, or a journal that cannot be
// read) is refused with ErrAsideStands and left as it is.
func (c *Cleaner) standingDiscard(copyPath string) (discardJournal, bool, error) {
	dir, err := c.home.Discarding(copyPath)
	if err != nil {
		return discardJournal{}, false, err
	}
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return discardJournal{}, false, nil
	case err != nil:
		return discardJournal{}, false, fmt.Errorf("%w at %s: it could not be read: %w", ErrAsideStands, dir, err)
	case !info.IsDir():
		return discardJournal{}, false, fmt.Errorf("%w at %s: it is not a directory", ErrAsideStands, dir)
	}
	path := filepath.Join(dir, journalName)
	info, err = os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return discardJournal{}, false, clearUnjournalled(dir)
	case err != nil:
		return discardJournal{}, false, fmt.Errorf("%w at %s: its journal could not be read: %w", ErrAsideStands, dir, err)
	case !info.Mode().IsRegular():
		return discardJournal{}, false, fmt.Errorf("%w at %s: its journal is not a regular file", ErrAsideStands, dir)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return discardJournal{}, false, fmt.Errorf("%w at %s: its journal could not be read: %w", ErrAsideStands, dir, err)
	}
	var j discardJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		return discardJournal{}, false, fmt.Errorf("%w at %s: its journal could not be read: %w", ErrAsideStands, dir, err)
	}
	if _, err := ParseCopyIdentity(j.Copy); err != nil {
		return discardJournal{}, false, fmt.Errorf("%w at %s: its journal names no copy: %w", ErrAsideStands, dir, err)
	}
	return j, true, nil
}

// clearUnjournalled removes dir, a discard's directory with no journal, in
// which nothing was set aside. The files it removes are the journal's
// partial files, writeWhole's, by their exact names (isPartial), each one
// it read and checked removed by that name; any other entry, one whose name
// merely begins like a partial file's among them, is refused with
// ErrAsideStands naming it, and dir is left as it is. A file written into
// dir after the read is not removed, and the last removal, of dir itself,
// then fails rather than taking it.
func clearUnjournalled(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("%w at %s: it could not be read: %w", ErrAsideStands, dir, err)
	}
	journal := filepath.Join(dir, journalName)
	var others []string
	for _, e := range entries {
		if !isPartial(journal, e.Name()) {
			others = append(others, e.Name())
		}
	}
	if len(others) > 0 {
		return fmt.Errorf("%w at %s: it has no journal and holds %s, which no discard writes there without one",
			ErrAsideStands, dir, strings.Join(others, ", "))
	}
	for _, e := range entries {
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w at %s: %w", ErrAsideStands, dir, err)
		}
	}
	return os.Remove(dir)
}
