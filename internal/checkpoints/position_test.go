package checkpoints_test

import (
	"context"
	"testing"

	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/graph"
)

// TestARunsPositionHasOneRecordAndItIsTheHistory walks a whole run through the
// durable store and shows that the record it left its position in is the
// checkpoint history: every position it stood in, in the order it stood in
// them, with the highest sequence being where it ended up.
//
// This is the claim PRD section 8 makes. Its other half, that no second record
// holds a copy of the same position, used to be shown here by reading the
// one-row checkpoint of a run the store had a row for and finding it empty.
// That read is gone with the accessor it went through, and the half it covered
// is now held in two places that can still fail. This package has no way to
// address a second record, because internal/store offers none. And the table
// that used to be one refuses a row, which is checked in internal/store by
// TestTheOneRowCheckpointTableTakesNoRows, against the database rather than
// against the absence of a caller.
func TestARunsPositionHasOneRecordAndItIsTheHistory(t *testing.T) {
	ctx := context.Background()
	durable := checkpoints.New(openRecords(t))
	const run = "run-1"

	rec := &recorder{}
	g := fixLoopGraph(t, rec)
	exec := mustExecutor(t, g, durable, 20)

	halted, err := exec.Run(ctx, run, mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if halted.Status != graph.StatusHalted || halted.Position != "gate" {
		t.Fatalf("the run ended %s at %q, want halted at gate", halted.Status, halted.Position)
	}
	done, err := exec.Answer(ctx, run, "fix")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("the answered run ended %s, want completed", done.Status)
	}

	// The history is a position per node the run stood before, appended and
	// never revised, and its highest sequence is where the run ended up. The
	// gate appears twice because the run stood there twice: once halted before
	// the node, and once running at it with the answer recorded.
	history, err := durable.History(ctx, run)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	wantStops := []string{"review", "gate", "gate", "fix", "review", "done", ""}
	if got := positions(history); !equalStrings(got, wantStops) {
		t.Errorf("the run's history stands at %v, want %v", got, wantStops)
	}
	for i, cp := range history {
		if cp.Seq != i+1 {
			t.Errorf("history entry %d carries sequence %d, want %d", i, cp.Seq, i+1)
		}
		t.Logf("history %s: position=%q status=%s steps=%d/%d decision=%v",
			cp.ID(), cp.Position, cp.Status, cp.Counters.Steps, cp.Counters.Budget, cp.Decision)
	}
	stood, err := durable.Latest(ctx, run)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if stood.ID() != done.Checkpoint || stood.Seq != len(history) {
		t.Errorf("the run stands at %s of %d entries, want %s at the highest sequence",
			stood.ID(), len(history), done.Checkpoint)
	}

	t.Logf("run %s: %d history entries, standing at %s", run, len(history), stood.ID())
}
