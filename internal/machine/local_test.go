package machine_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// A task its local landing record landed is phrased as a change made on this
// machine without validation, and never with the row a merge record's Landed
// reads: PRD section 6 has the local posture worded apart from a run's words.
// The control is a task the merge record landed, which reads the merged row,
// so the choice is made from the basis and not from the state alone.
func TestALocallyLandedTaskIsPhrasedAsUnvalidated(t *testing.T) {
	t.Parallel()
	local := machine.TaskPhrase(machine.Task{State: taskstate.Resolution{State: taskstate.Landed, Source: taskstate.SourceRecord,
		Basis: taskstate.Basis{LandedLocally: "c2"}}})
	merged := machine.TaskPhrase(machine.Task{State: taskstate.Resolution{State: taskstate.Landed, Source: taskstate.SourceRecord,
		Basis: taskstate.Basis{LandedRun: "r1"}}})
	if local == merged {
		t.Fatalf("a locally landed task reads as a merged one: %q", local)
	}
	if !strings.Contains(local.String(), "without validation") || strings.Contains(local.String(), "merged") {
		t.Fatalf("a locally landed task reads %q", local)
	}
	if !strings.Contains(merged.String(), "merged") {
		t.Fatalf("the control reads %q, so the merged row is not what it was", merged)
	}
	// Listed is what puts the row under the table test's vocabulary check.
	listed := false
	for _, row := range machine.TablePhrases() {
		if row == local {
			listed = true
		}
	}
	if !listed {
		t.Fatalf("%q is not in TablePhrases", local)
	}
}
