package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// Render and FormatFailure print a refused carry-on the way a person reads
// it. The production refusals from completeCarryOn, with the task id the
// test created, are asserted through FormatFailure in internal/service
// (TestCarryOnSlotRefusalsPrintThroughTheCLIRendererWithoutTaskIdentifiers).
// This file holds the render half and the positive control that each guard
// fails when the error interpolates a task id (#731 round 3, P13).
func TestCarryOnSlotRefusalRenderOmitsInterpolatedTaskIdentifiers(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)

	taskID := "72vguqpau776"
	key := store.KeptWorkKey(1)
	for _, row := range []struct {
		name   string
		phrase machine.Phrase
	}{
		{"busy", machine.PhraseCarryOnSlotBusy},
		{"already started", machine.PhraseCarryOnAlreadyStarted},
	} {
		t.Run(row.name, func(t *testing.T) {
			vocabtest.Assert(t, row.phrase.String())
			bad := fmt.Errorf("%w: %s: task %s: %s", ipc.ErrRefused, key, taskID, row.phrase)
			var badOut, badErrs bytes.Buffer
			if code := render(&invocation{env: Environment{Stdout: &badOut, Stderr: &badErrs}}, nil, bad); code != machine.ExitFailure {
				t.Fatalf("render of the positive-control error exited %s, want failure", code)
			}
			if !strings.Contains(badErrs.String(), taskID) {
				t.Fatalf("positive control: render of an error that interpolates task %s did not print it:\n%s", taskID, badErrs.String())
			}
			if !strings.Contains(FormatFailure(bad), taskID) {
				t.Fatalf("positive control: FormatFailure of the same error omitted task %s", taskID)
			}

			err := fmt.Errorf("%w: %s: %s", ipc.ErrRefused, key, row.phrase)
			var out, errs bytes.Buffer
			if code := render(&invocation{env: Environment{Stdout: &out, Stderr: &errs}}, nil, err); code != machine.ExitFailure {
				t.Fatalf("render exited %s, want failure", code)
			}
			printed := strings.TrimSuffix(errs.String(), "\n")
			if printed != FormatFailure(err) {
				t.Fatalf("render printed %q, want FormatFailure %q", printed, FormatFailure(err))
			}
			if strings.Contains(printed, taskID) {
				t.Fatalf("printed the task identifier %s:\n%s", taskID, printed)
			}
		})
	}
}
