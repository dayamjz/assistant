package checkpoints_test

import (
	"context"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// seamUserinfo is the shape internal/vcs redacts: the userinfo of a URL that
// carries a scheme. internal/store requires a redactor to open at all, and its
// own is unexported, so the tests supply one of that shape rather than reaching
// into another package.
var seamUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/@\s]+)@`)

func workingRedactor() vcs.Redactor {
	return vcs.RedactorFunc(func(s string) string {
		return seamUserinfo.ReplaceAllString(s, "${1}REDACTED@")
	})
}

// openRecords opens a fresh database in a temporary directory.
func openRecords(t *testing.T) *store.Store {
	t.Helper()
	return openRecordsAt(t, filepath.Join(t.TempDir(), "state.db"))
}

// openRecordsAt opens the database at path, creating it if it is not there, and
// closes it when the test ends. A test that means to survive a restart closes
// it itself and opens it again at the same path.
func openRecordsAt(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), path, store.WithRedactor(workingRedactor()))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// durableStore returns the implementation this package provides.
func durableStore(t *testing.T) graph.CheckpointStore {
	t.Helper()
	return checkpoints.New(openRecords(t))
}

// eachStore runs check against every implementation of the interface this
// repository has.
//
// Honouring the anchor, and everything else the interface states, is required
// of every implementation rather than a description of any one of them, so a
// behaviour is checked against both or it is checked against neither. Two
// implementations of one interface that disagree is the defect this exists to
// catch, and it is not a defect a suite that only exercised the new one could
// find.
func eachStore(t *testing.T, check func(t *testing.T, s graph.CheckpointStore)) {
	t.Helper()
	for _, impl := range []struct {
		name string
		open func(*testing.T) graph.CheckpointStore
	}{
		{name: "in memory", open: func(*testing.T) graph.CheckpointStore { return graph.NewMemoryStore() }},
		{name: "durable", open: durableStore},
	} {
		t.Run(impl.name, func(t *testing.T) { check(t, impl.open(t)) })
	}
}

// recorder notes which node bodies ran, in order. It is safe for concurrent use
// so one recorder can observe two callers racing over the same run.
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

// stateless adapts a plain function into a body constructor.
func stateless(fn graph.Body) func() graph.Body { return graph.Stateless(fn) }

// appendTrace returns a body that records the node ran, in the recorder and in
// state, so what a checkpoint carries moves as the run advances.
func appendTrace(rec *recorder, name string) func() graph.Body {
	return stateless(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
		rec.note(name)
		return w.Set("trace", graph.ListValue(name))
	})
}

// fixLoopGraph is a run with a decision inside a bounded cycle: review routes
// to the gate while there is anything to fix, the gate halts for an answer, the
// fixer records a fix, and the bounded back edge sends the run around again.
//
// It is the fixture the parity tests walk because crossing it moves everything
// a checkpoint carries: the state, the open decision, the step count, and the
// per-edge traversals and fingerprints. A fixture that left any of those still
// would let a store that dropped one pass.
func fixLoopGraph(t *testing.T, rec *recorder) *graph.Graph {
	t.Helper()
	g, err := graph.NewBuilder().
		Start("review").
		Key(graph.Key{Name: "findings", Kind: graph.KindInt}).
		Key(graph.Key{Name: "fixes", Kind: graph.KindInt, Merge: graph.MergeSum}).
		Key(graph.Key{Name: "trace", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Node(graph.Node{
			Name:   "review",
			Reads:  []string{"fixes"},
			Writes: []string{"findings", "trace"},
			NewBody: stateless(func(_ context.Context, r graph.Reader, w graph.Writer) error {
				rec.note("review")
				if err := w.Set("trace", graph.ListValue("review")); err != nil {
					return err
				}
				fixes, err := r.Get("fixes")
				if err != nil {
					return err
				}
				if applied, _ := fixes.Int(); applied > 0 {
					return w.Set("findings", graph.IntValue(0))
				}
				return w.Set("findings", graph.IntValue(1))
			}),
		}).
		Node(graph.Node{
			Name:    "gate",
			Reads:   []string{"answer"},
			Writes:  []string{"trace"},
			Halt:    &graph.Halt{Question: "apply the fix?", Options: []string{"fix", "stop"}, Into: "answer"},
			NewBody: appendTrace(rec, "gate"),
		}).
		Node(graph.Node{
			Name:   "fix",
			Writes: []string{"fixes", "trace"},
			NewBody: stateless(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
				rec.note("fix")
				if err := w.Set("trace", graph.ListValue("fix")); err != nil {
					return err
				}
				return w.Set("fixes", graph.IntValue(1))
			}),
		}).
		Node(graph.Node{Name: "done", Writes: []string{"trace"}, NewBody: appendTrace(rec, "done")}).
		Edge(graph.Edge{From: "review", To: "gate", Guard: &graph.Guard{
			Key: "findings", Op: graph.OpGreaterThan, Value: graph.IntValue(0),
		}}).
		Edge(graph.Edge{From: "review", To: "done"}).
		Edge(graph.Edge{From: "gate", To: "fix", Guard: &graph.Guard{
			Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("fix"),
		}}).
		Edge(graph.Edge{From: "gate", To: "done"}).
		Edge(graph.Edge{From: "fix", To: "review", Rounds: 2}).
		Build()
	if err != nil {
		t.Fatalf("Build refused the fixture graph: %v", err)
	}
	return g
}

// linearGraph is three nodes in a row, for the cases that need a run to walk
// and finish without stopping to ask anything.
func linearGraph(t *testing.T, rec *recorder) *graph.Graph {
	t.Helper()
	g, err := graph.NewBuilder().
		Start("a").
		Key(graph.Key{Name: "trace", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Node(graph.Node{Name: "a", Writes: []string{"trace"}, NewBody: appendTrace(rec, "a")}).
		Node(graph.Node{Name: "b", Writes: []string{"trace"}, NewBody: appendTrace(rec, "b")}).
		Node(graph.Node{Name: "c", Writes: []string{"trace"}, NewBody: appendTrace(rec, "c")}).
		Edge(graph.Edge{From: "a", To: "b"}).
		Edge(graph.Edge{From: "b", To: "c"}).
		Build()
	if err != nil {
		t.Fatalf("Build refused the fixture graph: %v", err)
	}
	return g
}

// everyKindGraph declares one key of every kind, so a checkpoint written over
// it exercises the whole of the state encoding.
func everyKindGraph(t *testing.T) *graph.Graph {
	t.Helper()
	g, err := graph.NewBuilder().
		Start("only").
		Key(graph.Key{Name: "note", Kind: graph.KindText}).
		Key(graph.Key{Name: "count", Kind: graph.KindInt}).
		Key(graph.Key{Name: "flag", Kind: graph.KindBool}).
		Key(graph.Key{Name: "items", Kind: graph.KindList}).
		Node(graph.Node{
			Name:    "only",
			Writes:  []string{"note", "count", "flag", "items"},
			NewBody: stateless(func(context.Context, graph.Reader, graph.Writer) error { return nil }),
		}).
		Build()
	if err != nil {
		t.Fatalf("Build refused the fixture graph: %v", err)
	}
	return g
}

// mustExecutor returns an executor over g with the given run-wide budget.
func mustExecutor(t *testing.T, g *graph.Graph, s graph.CheckpointStore, budget int) *graph.Executor {
	t.Helper()
	e, err := graph.NewExecutor(g, graph.Config{Store: s, Budget: budget})
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

// positions renders a history as the node each checkpoint stands at, which is
// what two implementations walking one graph must agree on.
func positions(history []graph.Checkpoint) []string {
	out := make([]string, 0, len(history))
	for _, cp := range history {
		out = append(out, cp.Position)
	}
	return out
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
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
