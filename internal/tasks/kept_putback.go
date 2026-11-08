package tasks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// putBack reverses every step the journal records, newest first, striking
// each from the journal once it is undone, so a stop takes up where it
// stopped (PRD section 6, "A refusal puts everything back"). A move whose
// original path now holds something else is refused with ErrAsideStands
// naming both; the aside and the step stay. writeBack steps undo first in
// reverse order: a file still holding exactly what the discard wrote is
// taken out, a missing path is struck, and anything else stays.
//
// It does not take the discard's claim back: that is the caller's
// (WithdrawKeptWorkDiscard). It does not remove the discard's directory.
func putBack(dir string, j discardJournal) error {
	for len(j.Steps) > 0 {
		i := len(j.Steps) - 1
		step := j.Steps[i]
		switch step.Kind {
		case discardStepWriteBack:
			if err := putBackWriteBack(step); err != nil {
				return err
			}
		case discardStepMove:
			if err := putBackMove(step); err != nil {
				return err
			}
		case discardStepHead, discardStepRefDelete, discardStepPONR, discardStepFinish:
			return fmt.Errorf("tasks: putBack does not yet reverse a %s step", step.Kind)
		default:
			return fmt.Errorf("tasks: putBack: unrecognized discard step %q", step.Kind)
		}
		next := j
		next.Steps = append([]discardStep(nil), j.Steps[:i]...)
		if err := writeJournal(dir, &j, next); err != nil {
			return err
		}
		j = next
	}
	return nil
}

// putBackMove puts a moved path back only when nothing stands at From.
func putBackMove(step discardStep) error {
	if step.From == "" || step.To == "" {
		return fmt.Errorf("tasks: putBack: a move step names no from or to")
	}
	if _, err := os.Lstat(step.From); err == nil {
		return fmt.Errorf("%w at %s: something stands where the set-aside at %s goes back", ErrAsideStands, step.From, step.To)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(step.To); err != nil {
		return fmt.Errorf("tasks: putBack: the set-aside at %s: %w", step.To, err)
	}
	if err := os.MkdirAll(filepath.Dir(step.From), 0o700); err != nil {
		return err
	}
	if err := renameNoReplace(step.To, step.From); err != nil {
		return fmt.Errorf("tasks: putting %s back to %s: %w", step.To, step.From, err)
	}
	return nil
}

// putBackWriteBack takes out a file the discard wrote from the index, on
// PRD section 6's three states.
func putBackWriteBack(step discardStep) error {
	if step.Path == "" {
		return fmt.Errorf("tasks: putBack: a writeBack step names no path")
	}
	raw, err := os.ReadFile(step.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case string(raw) == step.Held:
		if err := os.Remove(step.Path); err != nil {
			return fmt.Errorf("tasks: taking out the discard's write at %s: %w", step.Path, err)
		}
		return nil
	default:
		// Written meanwhile: leave it.
		return nil
	}
}
