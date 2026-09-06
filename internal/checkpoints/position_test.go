package checkpoints_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/store"
)

// TestARunsPositionHasOneRecordAndTheOneRowCheckpointIsNotIt walks a whole run
// through the durable store and shows that the only record it left its position
// in is the checkpoint history.
//
// This is the claim PRD section 8 now makes: the history is where a run's
// position is written and read, and no other record holds a copy of it. The
// one-row checkpoint the store still ships is the record that would be that
// copy, and the run below is driven under a run the store has a row for, so
// that record is writable for this run and is left empty anyway.
//
// An assertion that a read comes back empty is worth nothing on its own, so
// what the empty read is about is pinned down around it: the identifier names a
// run the store answers for, the same handle answers that run's whole position
// history, and internal/store's own tests hold the other side, that this record
// reads back when something writes it. What is missing is the record, not the
// run, the handle, or the read.
func TestARunsPositionHasOneRecordAndTheOneRowCheckpointIsNotIt(t *testing.T) {
	ctx := context.Background()
	records := openRecords(t)
	run := seedRunRow(t, records)

	rec := &recorder{}
	g := fixLoopGraph(t, rec)
	durable := checkpoints.New(records)
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

	// Nothing the run did wrote the one-row record, and the run, the handle
	// and the history it is being compared against are all right here.
	if _, err := records.Run(ctx, run); err != nil {
		t.Fatalf("the run the graph was driven under is not in the store, so an empty"+
			" checkpoint read below would say nothing: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("the store answered no history for the run it just walked")
	}
	if _, err := records.Checkpoint(ctx, run); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the one-row checkpoint of %s reads %v, want ErrNotFound: a run's position"+
			" must be in the history and nowhere else", run, err)
	}
	t.Logf("run %s: %d history entries, standing at %s; the one-row checkpoint record: %v",
		run, len(history), stood.ID(), store.ErrNotFound)
}

// seedRunRow creates a repository and a run on it, and returns the run's
// identifier. The one-row checkpoint references run(id), so a run driven under
// a name with no row could not be given that record at all, and finding it
// empty would say nothing.
func seedRunRow(t *testing.T, records *store.Store) string {
	t.Helper()
	ctx := context.Background()
	if _, err := records.UpsertRepository(ctx, store.Repository{
		ID:            "repo-1",
		WorkingPath:   "/checkouts/one",
		UpstreamURL:   "https://example.test/one.git",
		DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	run, err := records.CreateRun(ctx, store.Run{
		ID:            "run-1",
		RepositoryID:  "repo-1",
		Branch:        "topic",
		SubmittedHead: "aaaa",
		Base:          "bbbb",
		Intent:        "validate",
		IntentSource:  "push",
		Build: store.Build{
			Version: "v0.1.0", Revision: "0123456789abcdef", Modified: false, Go: "go1.25.0",
		},
		ConfigDigest: "cfg-1",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run.ID
}
