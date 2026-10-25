package machine_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// PRD section 6's stall hold ends nothing: its worker carries on and is
// watched while it waits. So a person told about one, by the notice of its
// hold-opened wake or by the line for the open hold itself, is never told
// the work does not move, which would read a live worker as paused. Each
// row carries none of the owners' words, the open line included, which is
// built per hold and so is in no table the vocabulary test reads. The control is a stop's
// hold, which does stop the work and keeps the rows that say so, so the
// stall rows are chosen by the hold and not by every hold.
func TestAStallHoldIsNeverPhrasedAsWorkThatDoesNotMove(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	stall := supervisor.StallHold{Task: "abcdefghijkl", Attempt: 1, Epoch: 2, Surface: 3}.Key()
	stopped := "task/abcdefghijkl/stopped/1"
	notice := func(key string) machine.Phrase {
		return machine.QueuedWakePhrase(machine.QueuedWake{Wake: store.Wake{
			Kind: string(supervisor.KindHoldOpened), Marker: supervisor.HoldOpenedMarker(key),
		}})
	}
	line := func(key string) machine.Phrase {
		return machine.HoldPhrase(store.Hold{Key: key, TaskID: store.Known("abcdefghijkl"), Subject: "whether to end it"})
	}
	stalled := []machine.Phrase{notice(stall), line(stall)}
	others := []machine.Phrase{notice(stopped), line(stopped)}
	vocabtest.Control(t)
	for i, p := range stalled {
		vocabtest.Assert(t, p.String())
		if strings.Contains(p.String(), "does not move") || strings.Contains(p.String(), "does not go on") {
			t.Errorf("stall row %d says the work stops: %q", i, p)
		}
		if !strings.Contains(p.String(), "Nothing has been ended") {
			t.Errorf("stall row %d does not say nothing was ended: %q", i, p)
		}
		if p == others[i] {
			t.Errorf("stall row %d reads as the stop's row %q", i, p)
		}
	}
	if !strings.Contains(others[0].String(), "does not go on") || !strings.Contains(others[1].String(), "does not move") {
		t.Fatalf("the stop's rows %q and %q no longer say the work stops, so this test compares nothing", others[0], others[1])
	}
	if !slices.Contains(machine.TablePhrases(), stalled[0]) {
		t.Errorf("the stall notice %q is not in TablePhrases", stalled[0])
	}
}
