package pipeline

import (
	"context"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
)

// RoundOf reads a stage's fix rounds off the counters a real run kept: a
// review that never stops reporting a fixable finding spends both of its
// rounds, and the reading names those two and the limit off the edge that
// carries it. A stage that took no round, and one configured to take none,
// read zero rounds with their own limits, so the reading is per stage rather
// than the run's total. Counters kept against another build of the pipeline
// are refused rather than read.
func TestRoundOfReadsAStagesRoundsOffTheRunsCounters(t *testing.T) {
	const limit = 2
	c := newCalls()
	stages := recordingStages(c)
	alwaysFixable(c, StageReview, &stages)
	p := build(t, Options{
		Stages: stages,
		Fixer:  movingFixer(c),
		Rounds: config.FixRounds{Review: limit, Lint: 3},
		Budget: 500,
	})
	store := graph.NewMemoryStore()
	exec, err := p.Executor(store)
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	state, err := p.NewState(complete())
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if _, err := exec.Run(context.Background(), "run", state); err != nil {
		t.Fatalf("Run: %v", err)
	}
	cp, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	for stage, want := range map[Stage]Round{
		StageReview: {Taken: limit, Limit: limit},
		StageLint:   {Taken: 0, Limit: 3},
		StageTest:   {Taken: 0, Limit: 0},
	} {
		got, err := RoundOf(p.Graph(), cp.Counters, stage)
		if err != nil {
			t.Fatalf("RoundOf(%s): %v", stage, err)
		}
		if got != want {
			t.Errorf("RoundOf(%s) = %+v, want %+v", stage, got, want)
		}
	}
	if n := c.fixCount(StageReview); n != limit {
		t.Fatalf("the fixer ran %d times, so the reading above was compared against the wrong run", n)
	}

	other := build(t, Options{Stages: stages, Fixer: movingFixer(c), Rounds: config.FixRounds{Review: limit}, Budget: 500})
	if _, err := RoundOf(other.Graph(), cp.Counters, StageReview); err == nil {
		t.Fatal("counters kept under one set of round limits were read against another")
	}
	if _, err := RoundOf(p.Graph(), cp.Counters, StageInvalid); err == nil {
		t.Fatal("a value naming no stage was given a round")
	}
}
