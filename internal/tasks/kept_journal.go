package tasks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// A confirmed discard's own directory and its journal, PRD section 6's
// "Removing a discarded copy", step 2: a discard sets what it removes aside
// into a directory beside the copy (home.Home.Discarding) and journals
// there, before each step, what it is about to do, so a refusal or a stop
// can put everything back or finish. This file opens one (beginDiscard),
// writes its journal whole (writeJournal) and reads one a discard left
// (standingDiscard). What a journal records of each item set aside is the
// set-aside's.
//
// Nothing here is recognized by its name, and nothing is removed, renamed
// or written over without proof that it is the discard's own:
//   - A directory is a discard's only by a whole journal in it naming the
//     copy, and one without is never removed. So it never stands without
//     one: the first journal is written into a staging directory of a fresh
//     name (home.Home.DiscardingStage), renamed into place whole and never
//     over anything (renameNoReplace). A first write that stopped leaves
//     only its staging directory, which blocks no later discard (the open
//     finding a690cc38 began on feat/age-407-discard).
//   - A journal is written over only while it reads exactly as the one the
//     discard last wrote, and the first only where none stands.
//   - A journal write removes only the partial its own call made
//     (replaceWhole), never a file beside it by its name.
//
// What it does not cover: a staging directory a stop left is not removed or
// reported here, since nothing in it proves it this program's (the report
// of what no claim names is a later link of AGE-486's stack); a stop during
// a later journal write leaves its partial beside the journal, never
// removed and read past; and a journal changed between writeJournal's read
// and its rename is replaced unseen.

// ErrAsideStands refuses a copy beside which something stands at the
// discard's directory's name that no journal proves this discard's, or a
// journal not as the discard last wrote it, or a directory where a new
// discard's would go. Nothing of it is removed; the error names where.
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

// beginDiscard makes the discard's directory beside the copy at copyPath,
// its first journal naming the copy's identity, by renaming a fresh
// staging directory holding that journal into place, never over anything.
// Something already standing there is refused with ErrAsideStands and
// left (standingDiscard reads it). A write or a rename that fails leaves
// the staging directory, and nothing at the discard's name.
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
	if err := writeJournal(stage, nil, discardJournal{Copy: id.String()}); err != nil {
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

// writeJournal replaces dir's journal with next through replaceWhole, and
// only the journal this discard last wrote: the one standing must read
// exactly as last was written, or, with last nil, none may stand. Anything
// else (changed, gone, standing where none should, not a regular file) is
// refused with ErrAsideStands and left as it is.
func writeJournal(dir string, last *discardJournal, next discardJournal) error {
	path := filepath.Join(dir, journalName)
	info, err := os.Lstat(path)
	switch {
	case last == nil && errors.Is(err, fs.ErrNotExist):
	case last == nil && err == nil:
		return fmt.Errorf("%w at %s: a journal already stands where the first goes", ErrAsideStands, dir)
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w at %s: its journal is gone", ErrAsideStands, dir)
	case err != nil:
		return err
	case !info.Mode().IsRegular():
		return fmt.Errorf("%w at %s: its journal is not a regular file", ErrAsideStands, dir)
	default:
		want, err := json.Marshal(*last)
		if err != nil {
			return err
		}
		if raw, err := os.ReadFile(path); err != nil {
			return err
		} else if !bytes.Equal(raw, want) {
			return fmt.Errorf("%w at %s: its journal is not the one this discard last wrote", ErrAsideStands, dir)
		}
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := replaceWhole(path, raw); errors.Is(err, ErrInstructionsLink) {
		return fmt.Errorf("%w at %s: its journal is a symbolic link", ErrAsideStands, dir)
	} else if err != nil {
		return err
	}
	return nil
}

// standingDiscard reads what a discard left beside the copy at copyPath:
// false when nothing stands at the discard's directory's name, and the
// journal and true for a directory whose journal names a copy's identity,
// whatever stands beside the journal. Anything else (a directory with no journal, a
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
