package tasks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// putBack reverses the journal's move and writeBack steps, newest first,
// striking each once undone so a stop takes up where it stopped (PRD
// section 6's refusal put-back, for the kinds this link journals). head,
// refDelete, ponr and finish steps are refused: later removal links that
// write them must reverse them here before a discard journals them. A
// move whose original path and aside both stand is ErrAsideStands naming
// both; if the aside is already gone the move is treated as done (a stop
// after the rename and before the strike). writeBack: exact content is
// taken out, a missing path is struck, anything else stays.
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

// putBackMove puts a moved path back only when nothing stands at From. If
// the aside is already gone, the move is treated as done so a stop between
// the rename and the journal strike can resume.
func putBackMove(step discardStep) error {
	if step.From == "" || step.To == "" {
		return fmt.Errorf("tasks: putBack: a move step names no from or to")
	}
	_, fromErr := os.Lstat(step.From)
	_, toErr := os.Lstat(step.To)
	switch {
	case errors.Is(toErr, fs.ErrNotExist):
		// Already restored, or the aside never stood: strike.
		return nil
	case toErr != nil:
		return fmt.Errorf("tasks: putBack: the set-aside at %s: %w", step.To, toErr)
	case fromErr == nil:
		return fmt.Errorf("%w at %s: something stands where the set-aside at %s goes back", ErrAsideStands, step.From, step.To)
	case !errors.Is(fromErr, fs.ErrNotExist):
		return fromErr
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
