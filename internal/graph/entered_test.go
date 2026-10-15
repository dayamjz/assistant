package graph_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

// Entered reads how often a node was entered off a real run's counters: a
// loop stopped part way through its third check has taken its fix node twice
// and re-entered its check twice through the back edge, and a node the graph
// does not declare has been entered no times. Counters accrued against other
// edges are refused with the same *CheckpointError a resume would raise, so
// the positional vector is never read against edges that did not produce it.
func TestEnteredCountsTraversalsIntoANodeAndRefusesForeignCounters(t *testing.T) {
	// The fix node writes a new value every round, so the convergence bound
	// does not park the loop before the segment ends where the test ends it.
	loop := &lateLoop{added: &graph.Key{Name: "subject", Kind: graph.KindText, Merge: graph.MergeLastWriteWins}, rename: true, failOn: 3}
	g := loop.build(t)
	store := graph.NewMemoryStore()
	if _, err := mustExecutor(t, g, store, 50).Run(context.Background(), "run", mustState(t, g, nil)); err == nil {
		t.Fatal("the run did not stop where the test ended its segment")
	}
	cp, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	for node, want := range map[string]int{"fix": 2, "check": 2, "done": 0, "nowhere": 0} {
		got, err := g.Entered(cp.Counters, node)
		if err != nil {
			t.Fatalf("Entered(%q): %v", node, err)
		}
		if got != want {
			t.Errorf("Entered(%q) = %d, want %d", node, got, want)
		}
	}

	foreign := cp.Counters
	foreign.EdgeDigest = "edges this graph does not have"
	var refused *graph.CheckpointError
	if _, err := g.Entered(foreign, "fix"); !errors.As(err, &refused) || refused.Field != "counters.edge_digest" {
		t.Fatalf("counters kept against other edges: Entered = %v, want the edge digest refused", err)
	}
	short := cp.Counters
	short.Traversals = short.Traversals[:1]
	if _, err := g.Entered(short, "fix"); !errors.As(err, &refused) {
		t.Fatalf("a traversal vector of the wrong length: Entered = %v, want it refused", err)
	}
}
