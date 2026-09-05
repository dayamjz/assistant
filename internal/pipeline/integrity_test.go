package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// swapStages are the nine stages the counter-misattribution trace needs:
// review takes a round and then passes, document holds for a person, and lint
// fails its first execution so a segment ends with the run standing at it.
// Every pipeline in this test is built from these same implementations, so the
// only thing that differs between them is the fix round limits.
func swapStages(c *calls) Stages {
	stages := recordingStages(c)
	set(&stages, StageReview, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		if call == 1 {
			return Output{Report: reportWith(findings.ActionFix, "fix me")}, nil
		}
		return Output{Report: passing()}, nil
	}))
	set(&stages, StageDocument, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: reportWith(findings.ActionAsk, "your call")}, nil
	}))
	set(&stages, StageLint, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		if call == 1 {
			return Output{}, errors.New("interrupted")
		}
		return Output{Report: reportWith(findings.ActionFix, "still wrong")}, nil
	}))
	return stages
}

// TestAResumeUnderCompensatingFixRoundLimitsIsRefused walks the trace doc.go
// describes. This package adds one edge per stage whose fix round limit is
// above zero, so lowering review's limit while raising lint's leaves the edge
// count alone and shifts every edge between them by one index. A checkpoint's
// per-edge counters would then be read against edges that did not produce
// them, and before internal/graph carried an edge digest this resume was
// admitted: lint took its round, and the back edge parked the run
// rounds-exhausted on review's inherited count before the re-run that would
// have verified the fix.
//
// The two controls matter as much as the refusal. The same resume under the
// limits the run recorded must still be admitted and go on, or the check would
// be refusing every resume rather than this one, and the two pipelines must
// have the same number of edges, or the length check internal/graph already
// had would be what caught it.
func TestAResumeUnderCompensatingFixRoundLimitsIsRefused(t *testing.T) {
	ctx := context.Background()
	store := graph.NewMemoryStore()
	c := newCalls()

	// The limits the run starts under: review takes one round, lint takes none.
	before := build(t, Options{
		Stages: swapStages(c),
		Fixer:  movingFixer(c),
		Rounds: config.FixRounds{Review: 1},
		Budget: 100,
	})
	exec, err := before.Executor(store)
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	state, err := before.NewState(complete())
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	held, err := exec.Run(ctx, "run", state)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if held.Status != graph.StatusHalted || held.Position != StageDocument.HoldNode() {
		t.Fatalf("the run is %s at %q, want it holding at %q", held.Status, held.Position, StageDocument.HoldNode())
	}
	// Answering document counts the traversal out of its hold and into lint,
	// which is the count the swap hands to lint's back edge.
	if _, err := exec.Answer(ctx, "run", string(OutcomeApproved)); !errors.Is(err, graph.ErrNodeFailed) {
		t.Fatalf("Answer = %v, want the segment to end on lint's failure", err)
	}
	cp, err := store.Latest(ctx, "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if cp.Position != StageLint.Node() {
		t.Fatalf("the run stands at %q, want it at %q with lint not yet done", cp.Position, StageLint.Node())
	}

	// The compensating swap: review's round goes to lint's.
	after := build(t, Options{
		Stages: swapStages(c),
		Fixer:  movingFixer(c),
		Rounds: config.FixRounds{Lint: 1},
		Budget: 100,
	})
	if len(after.Graph().Edges()) != len(before.Graph().Edges()) {
		t.Fatalf("the two configurations declare %d and %d edges, so a length check would already refuse "+
			"this resume and the case proves nothing",
			len(before.Graph().Edges()), len(after.Graph().Edges()))
	}
	swapped, err := after.Executor(store)
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	history, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	_, err = swapped.Resume(ctx, "run")
	var ce *graph.CheckpointError
	if !errors.As(err, &ce) {
		t.Fatalf("resuming under the swapped limits = %v, want a *graph.CheckpointError", err)
	}
	if ce.Field != "counters.edge_digest" {
		t.Fatalf("the refusal blames %q (%s), want it to name the counters' edge digest", ce.Field, ce.Detail)
	}
	if n := c.fixCount(StageLint); n != 0 {
		t.Errorf("the refused resume ran lint's fixer %d times, want none", n)
	}
	unchanged, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(unchanged) != len(history) {
		t.Errorf("the refused resume wrote %d checkpoints, want none", len(unchanged)-len(history))
	}

	// The control: the same run, resumed by a pipeline built again from the
	// limits it recorded. Lint runs, reports what it found, and holds, because
	// under these limits lint takes no fix round.
	same := build(t, Options{
		Stages: swapStages(c),
		Fixer:  movingFixer(c),
		Rounds: config.FixRounds{Review: 1},
		Budget: 100,
	})
	rebuilt, err := same.Executor(store)
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	got, err := rebuilt.Resume(ctx, "run")
	if err != nil {
		t.Fatalf("resuming under the limits the run recorded: %v", err)
	}
	if got.Status != graph.StatusHalted || got.Position != StageLint.HoldNode() {
		t.Fatalf("the resumed run is %s at %q, want it holding at %q", got.Status, got.Position, StageLint.HoldNode())
	}
}
