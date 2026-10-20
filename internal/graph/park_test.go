package graph_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

// parkedRun runs g to wherever it stops under budget and returns the result
// and the checkpoint the store kept for it, which went through the store's
// encoding on the way in and out.
func parkedRun(t *testing.T, g *graph.Graph, budget int) (graph.Result, graph.Checkpoint) {
	t.Helper()
	store := graph.NewMemoryStore()
	got, err := mustExecutor(t, g, store, budget).Run(context.Background(), "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	latest, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	return got, latest
}

// Each of the three loop bounds parks a run and the park says which, from the
// checkpoint that recorded it: the status, the node the run stands in front
// of, the edge whose bound it was for the two that live on an edge, and none
// for the budget, which is no edge's. The result a segment returns and the
// checkpoint a later reader loads say the same, so a caller that read the
// store rather than holding the segment's answer is told the same park. The
// usage budget's park, the fourth, is covered in usage_test.go.
func TestAParkNamesTheBoundAndTheEdgeItWasCountedOn(t *testing.T) {
	rec := &recorder{}
	rounds := mustBuild(t, fixLoopBuilder(rec, 2, func(_ context.Context, _ graph.Reader, w graph.Writer) error {
		return w.Set("log", graph.ListValue("round"))
	}))
	converges := mustBuild(t, fixLoopBuilder(rec, 10, func(context.Context, graph.Reader, graph.Writer) error {
		return nil
	}))
	spends := mustBuild(t, fixLoopBuilder(rec, 10, func(_ context.Context, _ graph.Reader, w graph.Writer) error {
		return w.Set("log", graph.ListValue("round"))
	}))

	for _, c := range []struct {
		name   string
		g      *graph.Graph
		budget int
		status graph.Status
		edge   *graph.EdgeRef
	}{
		{"round limit", rounds, 50, graph.StatusRoundsExhausted, &roundLimitEdge},
		{"convergence", converges, 50, graph.StatusConverged, &graph.EdgeRef{From: "fix", To: "check", Rounds: 10}},
		{"step budget", spends, 5, graph.StatusBudgetExhausted, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, latest := parkedRun(t, c.g, c.budget)
			if got.Status != c.status {
				t.Fatalf("Status = %s (%s), want %s", got.Status, got.Reason, c.status)
			}
			fromResult, ok := got.Park()
			if !ok {
				t.Fatalf("a %s result reports no park", got.Status)
			}
			fromCheckpoint, ok := latest.Park()
			if !ok {
				t.Fatalf("a %s checkpoint reports no park", latest.Status)
			}
			for source, park := range map[string]graph.Park{"result": fromResult, "checkpoint": fromCheckpoint} {
				if park.Status != c.status || park.Node != got.Position || park.Budget != c.budget {
					t.Errorf("the %s's park is %+v, want %s in front of %q under a budget of %d",
						source, park, c.status, got.Position, c.budget)
				}
				if !sameEdge(park.Edge, c.edge) {
					t.Errorf("the %s's park names edge %+v, want %+v", source, park.Edge, c.edge)
				}
			}
		})
	}
}

// roundLimitEdge is the edge fixLoopBuilder declares a round bound on, with
// the bound the tests here build it with: the back edge, which is the only
// bounded edge that loop has.
var roundLimitEdge = graph.EdgeRef{From: "fix", To: "check", Rounds: 2}

// sameEdge reports whether two optional edge references name the same edge.
func sameEdge(a, b *graph.EdgeRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// A status that is not a park reports none, a hold included: a halted run is
// stopped and is not parked, which is the line PRD section 5 draws between
// the two words.
func TestOnlyTheThreeParksAreParked(t *testing.T) {
	for _, s := range []graph.Status{
		graph.StatusInvalid, graph.StatusRunning, graph.StatusCompleted, graph.StatusHalted,
	} {
		if s.Parked() {
			t.Errorf("%s reads as parked", s)
		}
		if _, ok := (graph.Result{Status: s}).Park(); ok {
			t.Errorf("a %s result reports a park", s)
		}
	}
	for _, s := range []graph.Status{graph.StatusRoundsExhausted, graph.StatusBudgetExhausted, graph.StatusConverged} {
		if !s.Parked() || !s.Stopped() {
			t.Errorf("%s does not read as a park that stops the run", s)
		}
	}
}

// A recorded park edge is held to what the executor writes, and refused as a
// checkpoint error naming it when it could not have been. One that is absent
// on an edge park passes, because that is what a checkpoint written before the
// field existed holds.
func TestValidateRefusesAParkEdgeTheRunCouldNotHaveParkedOn(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, fixLoopBuilder(rec, 2, func(_ context.Context, _ graph.Reader, w graph.Writer) error {
		return w.Set("log", graph.ListValue("round"))
	}))
	_, parked := parkedRun(t, g, 50)
	if parked.ParkedAt == nil || *parked.ParkedAt != roundLimitEdge {
		t.Fatalf("the round limit parked on %+v, want %+v", parked.ParkedAt, roundLimitEdge)
	}
	if err := g.Validate(parked); err != nil {
		t.Fatalf("the checkpoint the executor wrote is refused: %v", err)
	}

	legacy := parked
	legacy.ParkedAt = nil
	if err := g.Validate(legacy); err != nil {
		t.Fatalf("an edge park with no edge recorded is refused: %v", err)
	}
	if park, ok := legacy.Park(); !ok || park.Edge != nil {
		t.Fatalf("an edge park with no edge recorded reports %+v (%v), want the park with no edge", park, ok)
	}

	for _, c := range []struct {
		name   string
		mutate func(graph.Checkpoint) graph.Checkpoint
	}{
		{"an edge on a budget park", func(c graph.Checkpoint) graph.Checkpoint {
			c.Status = graph.StatusBudgetExhausted
			return c
		}},
		{"an edge the graph does not declare", func(c graph.Checkpoint) graph.Checkpoint {
			c.ParkedAt = &graph.EdgeRef{From: "done", To: "check", Rounds: 2}
			return c
		}},
		{"a bound the edge does not declare", func(c graph.Checkpoint) graph.Checkpoint {
			c.ParkedAt = &graph.EdgeRef{From: "fix", To: "check", Rounds: 3}
			return c
		}},
		{"an edge leading somewhere the run does not stand", func(c graph.Checkpoint) graph.Checkpoint {
			c.Position = "fix"
			return c
		}},
		{"an edge no round limit counts", func(c graph.Checkpoint) graph.Checkpoint {
			c.ParkedAt = &graph.EdgeRef{From: "check", To: "fix"}
			c.Position = "fix"
			return c
		}},
		{"a convergence park on an edge that is not a back edge", func(c graph.Checkpoint) graph.Checkpoint {
			c.Status = graph.StatusConverged
			c.ParkedAt = &graph.EdgeRef{From: "check", To: "fix"}
			c.Position = "fix"
			return c
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := g.Validate(c.mutate(parked))
			var refused *graph.CheckpointError
			if !errors.As(err, &refused) || refused.Field != "parked_at" {
				t.Fatalf("Validate = %v, want a refusal naming parked_at", err)
			}
		})
	}
}
