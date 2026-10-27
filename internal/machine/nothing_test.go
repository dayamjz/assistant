package machine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// PRD section 6's "nothing left to deliver" (P13): a task its run's
// nothing-to-deliver record landed is phrased as already on the shared branch
// with nothing delivered, never as merged, on its task line and on every
// catch-up line about it, and the catch-up line names the record by its run.
// The controls are a task a merge record landed, which reads the merged row,
// and that task's cleanup line, which reads removed after it was merged, so
// the choice is made from the basis and the record and not from the state.
func TestATaskWithNothingToDeliverIsNeverPhrasedAsMerged(t *testing.T) {
	t.Parallel()
	nothing := taskstate.Resolution{State: taskstate.Landed, Source: taskstate.SourceRecord,
		Basis: taskstate.Basis{LandedRun: "r1", NothingToDeliver: true}}
	viaMerge := taskstate.Resolution{State: taskstate.Landed, Source: taskstate.SourceRecord,
		Basis: taskstate.Basis{LandedRun: "r1"}}

	line := machine.TaskPhrase(machine.Task{State: nothing})
	if strings.Contains(line.Outcome, "merged") || !strings.Contains(line.Outcome, "already on the shared branch") {
		t.Fatalf("the task line reads %q", line)
	}
	if control := machine.TaskPhrase(machine.Task{State: viaMerge}); !strings.Contains(control.Outcome, "merged") {
		t.Fatalf("the control reads %q, so the merged row is not what it was", control)
	}

	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	record := store.NothingToDeliver{Run: "r1", RecordedAt: at}
	cleaned := store.TaskCleanup{TaskID: "t", RemovedAt: store.Known(at)}
	shown := machine.Landed{Task: machine.Task{State: nothing}, NothingToDeliver: &record}
	if got := shown.Record(); got != (store.SeenRecord{Kind: store.SeenNothingToDeliver, Key: "r1"}) {
		t.Fatalf("the catch-up line names %+v", got)
	}
	if !shown.At().Equal(record.RecordedAt) {
		t.Fatalf("the line is dated %v, want the record's %v", shown.At(), record.RecordedAt)
	}
	for _, l := range []machine.Landed{shown, {Task: machine.Task{State: nothing}, Cleanup: &cleaned}} {
		if phrase := machine.LandedPhrase(l); strings.Contains(phrase.Outcome, "merged") {
			t.Fatalf("the catch-up line about %+v reads %q", l.Record(), phrase)
		}
	}
	if control := machine.LandedPhrase(machine.Landed{Task: machine.Task{State: viaMerge}, Cleanup: &cleaned}); !strings.Contains(control.Outcome, "merged") {
		t.Fatalf("the control's cleanup line reads %q", control)
	}

	listed := map[machine.Phrase]bool{}
	for _, row := range machine.TablePhrases() {
		listed[row] = true
	}
	for _, row := range []machine.Phrase{line, machine.LandedPhrase(shown),
		machine.LandedPhrase(machine.Landed{Task: machine.Task{State: nothing}, Cleanup: &cleaned})} {
		if !listed[row] {
			t.Fatalf("%q is not in TablePhrases, so the vocabulary check does not reach it", row)
		}
	}
}
