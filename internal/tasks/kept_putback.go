package tasks

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// putBack reverses the journal's move and writeBack steps, newest first,
// striking each once undone so a stop takes up where it stopped (PRD
// section 6's refusal put-back). head and refDelete are refused until
// those set-asides land. ponr and finish are refused because they are past
// the point of no return: Discard finishes deletion instead of putting
// back, and strikes an unreached ponr (rename never happened) before
// calling putBack. A
// move whose original path and aside both stand is ErrAsideStands naming
// both; if the aside is gone and the original stands, the move is treated
// as done (a stop after the rename and before the strike); if both are
// gone the move is refused, because the restore cannot be verified (P6).
// writeBack: exact Held bytes are taken out, a missing path is struck,
// anything else stays.
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
// the aside is gone and From stands, the move is treated as done so a stop
// between the rename and the journal strike can resume. If the aside is
// gone and From is gone too, the restore cannot be verified and is refused.
func putBackMove(step discardStep) error {
	if step.From == "" || step.To == "" {
		return fmt.Errorf("tasks: putBack: a move step names no from or to")
	}
	_, fromErr := os.Lstat(step.From)
	_, toErr := os.Lstat(step.To)
	switch {
	case errors.Is(toErr, fs.ErrNotExist) && fromErr == nil:
		// Already restored: strike.
		return nil
	case errors.Is(toErr, fs.ErrNotExist) && errors.Is(fromErr, fs.ErrNotExist):
		return fmt.Errorf("tasks: putBack: the set-aside at %s is gone and nothing stands at %s", step.To, step.From)
	case errors.Is(toErr, fs.ErrNotExist):
		return fmt.Errorf("tasks: putBack: the original at %s: %w", step.From, fromErr)
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
// PRD section 6's three states. Held is compared as bytes, so a journalled
// write of non-UTF-8 content still matches what stands on disk.
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
	case bytes.Equal(raw, step.Held):
		if err := os.Remove(step.Path); err != nil {
			return fmt.Errorf("tasks: taking out the discard's write at %s: %w", step.Path, err)
		}
		return nil
	default:
		// Written meanwhile: leave it.
		return nil
	}
}
