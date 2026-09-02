package graph_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

// recorder notes which node bodies ran, in order. It is safe for concurrent
// use so the same recorder can observe two runs at once.
type recorder struct {
	mu  sync.Mutex
	ran []string
}

func (r *recorder) note(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ran = append(r.ran, name)
}

func (r *recorder) order() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ran...)
}

func (r *recorder) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, got := range r.ran {
		if got == name {
			n++
		}
	}
	return n
}

// noteOnly returns a body constructor that records the node ran and does
// nothing else.
func noteOnly(rec *recorder, name string) func() graph.Body {
	return graph.Stateless(func(_ context.Context, _ graph.Reader, _ graph.Writer) error {
		rec.note(name)
		return nil
	})
}

// sameDecision reports whether two decisions are the same question in the same
// place with the same options.
func sameDecision(a, b *graph.Decision) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Node != b.Node || a.Question != b.Question || a.Into != b.Into {
		return false
	}
	return equalStrings(a.Options, b.Options)
}

// body adapts a plain function into a body constructor.
func body(fn graph.Body) func() graph.Body { return graph.Stateless(fn) }

// requireBuildError asserts that err is a *graph.BuildError breaking exactly
// the given rules, and returns it.
func requireBuildError(t *testing.T, err error, want ...graph.Rule) *graph.BuildError {
	t.Helper()
	if err == nil {
		t.Fatal("expected the graph to be refused at construction, got no error")
	}
	var be *graph.BuildError
	if !errors.As(err, &be) {
		t.Fatalf("expected a *graph.BuildError, got %T: %v", err, err)
	}
	got := be.Rules()
	gotCopy := append([]graph.Rule(nil), got...)
	wantCopy := append([]graph.Rule(nil), want...)
	sort.Slice(gotCopy, func(i, j int) bool { return gotCopy[i] < gotCopy[j] })
	sort.Slice(wantCopy, func(i, j int) bool { return wantCopy[i] < wantCopy[j] })
	if len(gotCopy) != len(wantCopy) {
		t.Fatalf("broken rules = %v, want exactly %v\n%v", got, want, be)
	}
	for i := range gotCopy {
		if gotCopy[i] != wantCopy[i] {
			t.Fatalf("broken rules = %v, want exactly %v\n%v", got, want, be)
		}
	}
	for _, rule := range want {
		if !be.HasRule(rule) {
			t.Fatalf("HasRule(%q) = false for %v", rule, be)
		}
	}
	return be
}

// mustBuild builds b and fails the test if it is refused.
func mustBuild(t *testing.T, b *graph.Builder) *graph.Graph {
	t.Helper()
	g, err := b.Build()
	if err != nil {
		t.Fatalf("Build() refused a valid graph: %v", err)
	}
	return g
}

// mustExecutor returns an executor over g with the given run-wide budget.
func mustExecutor(t *testing.T, g *graph.Graph, store graph.CheckpointStore, budget int) *graph.Executor {
	t.Helper()
	e, err := graph.NewExecutor(g, graph.Config{Store: store, Budget: budget})
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	return e
}

// mustState builds an initial state for g.
func mustState(t *testing.T, g *graph.Graph, values map[string]graph.Value) graph.State {
	t.Helper()
	s, err := g.NewState(values)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	return s
}

// text returns the text held under key, failing the test if it is absent or
// of another kind.
func text(t *testing.T, s graph.State, key string) string {
	t.Helper()
	v, ok := s.Get(key)
	if !ok {
		t.Fatalf("state has no key %q", key)
	}
	got, ok := v.Text()
	if !ok {
		t.Fatalf("key %q holds %s, want text", key, v.Kind())
	}
	return got
}

// number returns the integer held under key.
func number(t *testing.T, s graph.State, key string) int64 {
	t.Helper()
	v, ok := s.Get(key)
	if !ok {
		t.Fatalf("state has no key %q", key)
	}
	got, ok := v.Int()
	if !ok {
		t.Fatalf("key %q holds %s, want int", key, v.Kind())
	}
	return got
}

// list returns the string list held under key.
func list(t *testing.T, s graph.State, key string) []string {
	t.Helper()
	v, ok := s.Get(key)
	if !ok {
		t.Fatalf("state has no key %q", key)
	}
	got, ok := v.List()
	if !ok {
		t.Fatalf("key %q holds %s, want list", key, v.Kind())
	}
	return got
}

// traversals returns the per-edge traversal counts recorded in a run's latest
// checkpoint, keyed by "from->to".
func traversals(t *testing.T, g *graph.Graph, store graph.CheckpointStore, run string) map[string]int {
	t.Helper()
	cp, err := store.Latest(context.Background(), run)
	if err != nil {
		t.Fatalf("Latest(%q): %v", run, err)
	}
	edges := g.Edges()
	out := make(map[string]int, len(edges))
	for i, e := range edges {
		out[e.From+"->"+e.To] = cp.Counters.Traversals[i]
	}
	return out
}

// fixLoopBuilder returns the canonical bounded cycle: a node that inspects
// something, a node that changes it, and a bounded back edge between them.
// fixBody decides whether a round changes state, which is what separates the
// round bound from the convergence check.
func fixLoopBuilder(rec *recorder, rounds int, fixBody graph.Body) *graph.Builder {
	return graph.NewBuilder().
		Start("check").
		Key(graph.Key{Name: "findings", Kind: graph.KindInt}).
		Key(graph.Key{Name: "log", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Node(graph.Node{
			Name:   "check",
			Writes: []string{"findings"},
			NewBody: body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
				rec.note("check")
				return w.Set("findings", graph.IntValue(1))
			}),
		}).
		Node(graph.Node{
			Name:    "fix",
			Reads:   []string{"findings", "log"},
			Writes:  []string{"log"},
			NewBody: body(fixBody),
		}).
		Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
		Edge(graph.Edge{From: "check", To: "fix", Guard: &graph.Guard{
			Key: "findings", Op: graph.OpGreaterThan, Value: graph.IntValue(0),
		}}).
		Edge(graph.Edge{From: "check", To: "done"}).
		Edge(graph.Edge{From: "fix", To: "check", Rounds: rounds})
}
