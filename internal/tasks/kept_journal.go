package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
// Nothing here is recognized by its name. What proves a directory a
// discard's is a whole journal in it naming the copy, and nothing without
// one is ever removed. So the discard's directory never stands without its
// journal: the first journal is written, through writeWhole, this
// package's one way of writing a file whole, into a staging directory of a
// fresh name (home.Home.DiscardingStage), which is then renamed to the
// discard's directory whole, by a rename that refuses to replace anything
// standing there (renameNoReplace). A directory at the discard's name with
// no journal is therefore not one this program made, and is refused and
// left. A discard that stopped during its first write (the open finding
// a690cc38 began on feat/age-407-discard, a leftover partial journal that
// refused every later discard of the copy) leaves only its staging
// directory, which blocks no later discard, since each makes a staging
// directory of its own.
//
// What it does not cover: a staging directory a stop left is not removed,
// since nothing in it proves it this program's, and nothing here reports
// it; the report of what no claim names is a later link of AGE-486's
// stack.

// ErrAsideStands is returned for a copy beside which something stands at
// the discard's directory's name that this program cannot take up on its
// own: no journal, an unreadable journal or one naming no copy, a journal
// that is not a regular file, a file in place of the directory, or a
// directory where a new discard's would go. Nothing of it is removed, and
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
// with its first journal, naming the copy's identity, in it, before
// anything is set aside: the journal is written into a fresh staging
// directory, which is renamed to the discard's directory whole and never
// over anything standing there. Something already standing at that name
// is refused with ErrAsideStands and left as it is: standingDiscard is
// what reads it. A write or a rename that fails leaves the staging
// directory where it is, and nothing at the discard's name.
func (c *Cleaner) beginDiscard(copyPath string) (string, error) {
	dir, err := c.home.Discarding(copyPath)
	if err != nil {
		return "", err
	}
	parent, pattern, err := c.home.DiscardingStage(copyPath)
	if err != nil {
		return "", err
	}
	id, err := IdentifyCopy(copyPath)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(dir); err == nil {
		return "", fmt.Errorf("%w at %s: something already stands there", ErrAsideStands, dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", err
	}
	if err := writeJournal(stage, discardJournal{Copy: id.String()}); err != nil {
		return "", fmt.Errorf("tasks: writing the first journal of a discard at %s: %w", stage, err)
	}
	if err := renameNoReplace(stage, dir); err != nil {
		if _, lerr := os.Lstat(dir); lerr == nil {
			return "", fmt.Errorf("%w at %s: something came to stand there while the discard began", ErrAsideStands, dir)
		}
		return "", fmt.Errorf("tasks: moving a discard's first journal from %s to %s: %w", stage, dir, err)
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
// It reports false when nothing stands at the discard's directory's name,
// and the journal and true for a directory whose journal names a copy's
// identity, a partial file beside it ignored, since the journal is the
// last one written whole. Anything else (a directory with no journal, a
// journal that names no copy or cannot be read, a journal that is not a
// regular file, or a file in place of the directory) is refused with
// ErrAsideStands and left as it is: nothing is removed.
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
		return discardJournal{}, false, fmt.Errorf("%w at %s: it has no journal, so nothing in it is known to be a discard's", ErrAsideStands, dir)
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
