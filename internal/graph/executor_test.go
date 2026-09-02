package graph_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

func TestRunWalksALinearGraphAndCarriesState(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("first").
		Key(graph.Key{Name: "trace", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Node(graph.Node{Name: "first", Writes: []string{"trace"}, NewBody: appendTrace(rec, "first")}).
		Node(graph.Node{Name: "second", Writes: []string{"trace"}, NewBody: appendTrace(rec, "second")}).
		Node(graph.Node{Name: "third", Writes: []string{"trace"}, NewBody: appendTrace(rec, "third")}).
		Edge(graph.Edge{From: "first", To: "second"}).
		Edge(graph.Edge{From: "second", To: "third"}))

	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)
	got, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got.Status != graph.StatusCompleted {
		t.Errorf("Status = %s, want completed", got.Status)
	}
	if got.Position != "" {
		t.Errorf("Position = %q, want empty on a completed run", got.Position)
	}
	if got.Steps != 3 {
		t.Errorf("Steps = %d, want 3", got.Steps)
	}
	if want := []string{"first", "second", "third"}; !equalStrings(list(t, got.State, "trace"), want) {
		t.Errorf("trace = %v, want %v", list(t, got.State, "trace"), want)
	}
	if !equalStrings(rec.order(), []string{"first", "second", "third"}) {
		t.Errorf("bodies ran in order %v", rec.order())
	}
}

// appendTrace returns a body that records the node ran and appends its name to
// the "trace" key.
func appendTrace(rec *recorder, name string) func() graph.Body {
	return body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
		rec.note(name)
		return w.Set("trace", graph.ListValue(name))
	})
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

func TestGuardsChooseTheEdgeTaken(t *testing.T) {
	build := func(rec *recorder) *graph.Graph {
		return mustBuild(t, graph.NewBuilder().
			Start("pick").
			Key(graph.Key{Name: "route", Kind: graph.KindText}).
			Node(graph.Node{Name: "pick", NewBody: noteOnly(rec, "pick")}).
			Node(graph.Node{Name: "left", NewBody: noteOnly(rec, "left")}).
			Node(graph.Node{Name: "right", NewBody: noteOnly(rec, "right")}).
			Edge(graph.Edge{From: "pick", To: "left", Guard: &graph.Guard{
				Key: "route", Op: graph.OpEquals, Value: graph.TextValue("left"),
			}}).
			Edge(graph.Edge{From: "pick", To: "right"}))
	}

	for _, route := range []string{"left", "right"} {
		t.Run(route, func(t *testing.T) {
			rec := &recorder{}
			g := build(rec)
			store := graph.NewMemoryStore()
			exec := mustExecutor(t, g, store, 10)
			state := mustState(t, g, map[string]graph.Value{"route": graph.TextValue(route)})
			got, err := exec.Run(context.Background(), "run", state)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got.Status != graph.StatusCompleted {
				t.Fatalf("Status = %s, want completed", got.Status)
			}
			history, err := store.History(context.Background(), "run")
			if err != nil {
				t.Fatalf("History: %v", err)
			}
			// The checkpoint written after "pick" records the node chosen next.
			if history[1].Position != route {
				t.Errorf("after pick the run moved to %q, want %q", history[1].Position, route)
			}
			if !equalStrings(rec.order(), []string{"pick", route}) {
				t.Errorf("bodies ran %v, want [pick %s]", rec.order(), route)
			}
		})
	}
}

func TestRoundBoundParksARunTheOtherTwoBoundsWouldNot(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, fixLoopBuilder(rec, 2, func(_ context.Context, _ graph.Reader, w graph.Writer) error {
		rec.note("fix")
		// Every round changes state, so the convergence check never fires.
		return w.Set("log", graph.ListValue("round"))
	}))
	store := graph.NewMemoryStore()
	const budget = 50
	exec := mustExecutor(t, g, store, budget)

	got, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got.Status != graph.StatusRoundsExhausted {
		t.Fatalf("Status = %s (%s), want rounds_exhausted", got.Status, got.Reason)
	}
	if got.Position != "check" {
		t.Errorf("Position = %q, want the node the run refused to re-enter", got.Position)
	}
	if got.Steps >= budget {
		t.Errorf("Steps = %d, which reached the budget of %d; the budget, not the round bound, would have parked this run", got.Steps, budget)
	}
	if n := len(list(t, got.State, "log")); n != 3 {
		t.Errorf("the loop ran %d rounds of work, want 3; state changed every round so convergence cannot explain the stop", n)
	}
	if n := traversals(t, g, store, "run")["fix->check"]; n != 2 {
		t.Errorf("back edge traversals = %d, want its bound of 2", n)
	}
	if !strings.Contains(got.Reason, "bound of 2 rounds") {
		t.Errorf("Reason = %q, want it to name the bound", got.Reason)
	}
}

func TestStepBudgetParksARunTheOtherTwoBoundsWouldNot(t *testing.T) {
	rec := &recorder{}
	// Two loops in sequence. Each has its own generous round bound, and each
	// changes state every round, so only the run-wide budget can stop this.
	g := mustBuild(t, graph.NewBuilder().
		Start("check_a").
		Key(graph.Key{Name: "a_rounds", Kind: graph.KindInt}).
		Key(graph.Key{Name: "b_rounds", Kind: graph.KindInt}).
		Node(graph.Node{Name: "check_a", NewBody: noteOnly(rec, "check_a")}).
		Node(graph.Node{Name: "fix_a", Reads: []string{"a_rounds"}, Writes: []string{"a_rounds"},
			NewBody: bumpCounter(rec, "fix_a", "a_rounds")}).
		Node(graph.Node{Name: "check_b", NewBody: noteOnly(rec, "check_b")}).
		Node(graph.Node{Name: "fix_b", Reads: []string{"b_rounds"}, Writes: []string{"b_rounds"},
			NewBody: bumpCounter(rec, "fix_b", "b_rounds")}).
		Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
		Edge(graph.Edge{From: "check_a", To: "fix_a", Guard: &graph.Guard{
			Key: "a_rounds", Op: graph.OpLessThan, Value: graph.IntValue(2),
		}}).
		Edge(graph.Edge{From: "check_a", To: "check_b"}).
		Edge(graph.Edge{From: "fix_a", To: "check_a", Rounds: 3}).
		Edge(graph.Edge{From: "check_b", To: "fix_b", Guard: &graph.Guard{
			Key: "b_rounds", Op: graph.OpLessThan, Value: graph.IntValue(5),
		}}).
		Edge(graph.Edge{From: "check_b", To: "done"}).
		Edge(graph.Edge{From: "fix_b", To: "check_b", Rounds: 3}))

	store := graph.NewMemoryStore()
	const budget = 8
	exec := mustExecutor(t, g, store, budget)

	got, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got.Status != graph.StatusBudgetExhausted {
		t.Fatalf("Status = %s (%s), want budget_exhausted", got.Status, got.Reason)
	}
	if got.Steps != budget {
		t.Errorf("Steps = %d, want the whole budget of %d", got.Steps, budget)
	}
	if got.Position != "fix_b" {
		t.Errorf("Position = %q, want the node that could not run", got.Position)
	}
	taken := traversals(t, g, store, "run")
	if n := taken["fix_a->check_a"]; n >= 3 {
		t.Errorf("the first loop took its back edge %d times, reaching its own bound of 3", n)
	}
	if n := taken["fix_b->check_b"]; n >= 3 {
		t.Errorf("the second loop took its back edge %d times, reaching its own bound of 3", n)
	}
	if taken["fix_a->check_a"] == 0 || taken["fix_b->check_b"] == 0 {
		t.Errorf("both loops should have run at least one round, got %v", taken)
	}
	if a, b := number(t, got.State, "a_rounds"), number(t, got.State, "b_rounds"); a == 0 || b == 0 {
		t.Errorf("state stopped changing (a_rounds=%d b_rounds=%d); convergence could explain this stop", a, b)
	}
}

// bumpCounter returns a body that increments an integer state key by one.
func bumpCounter(rec *recorder, name, key string) func() graph.Body {
	return body(func(_ context.Context, r graph.Reader, w graph.Writer) error {
		rec.note(name)
		current, err := r.Get(key)
		if err != nil {
			return err
		}
		n, _ := current.Int()
		return w.Set(key, graph.IntValue(n+1))
	})
}

func TestConvergenceParksARunTheOtherTwoBoundsWouldNot(t *testing.T) {
	rec := &recorder{}
	// The fixer reports success without changing anything. Neither counter
	// would notice that before exhausting itself.
	g := mustBuild(t, fixLoopBuilder(rec, 10, func(_ context.Context, _ graph.Reader, _ graph.Writer) error {
		rec.note("fix")
		return nil
	}))
	store := graph.NewMemoryStore()
	const budget = 50
	exec := mustExecutor(t, g, store, budget)

	got, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got.Status != graph.StatusConverged {
		t.Fatalf("Status = %s (%s), want converged", got.Status, got.Reason)
	}
	if got.Position != "check" {
		t.Errorf("Position = %q, want the node the run refused to re-enter", got.Position)
	}
	if got.Steps >= budget {
		t.Errorf("Steps = %d reached the budget of %d", got.Steps, budget)
	}
	if n := traversals(t, g, store, "run")["fix->check"]; n >= 10 {
		t.Errorf("back edge traversals = %d, reaching its bound of 10", n)
	}
	if !strings.Contains(got.Reason, "changed no state") {
		t.Errorf("Reason = %q, want it to say the round changed nothing", got.Reason)
	}
}

func TestExecutionStopsBeforeAHaltedNode(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)

	got, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got.Status != graph.StatusHalted {
		t.Fatalf("Status = %s, want halted", got.Status)
	}
	if got.Position != "gate" {
		t.Errorf("Position = %q, want gate", got.Position)
	}
	if rec.count("gate") != 0 {
		t.Fatal("the halted node's body ran; a halt point stops before the node, not inside it")
	}
	if rec.count("act") != 0 {
		t.Error("a node past the halt point ran")
	}
	if got.Steps != 1 {
		t.Errorf("Steps = %d, want 1: only the node before the halt point ran", got.Steps)
	}
	if got.Decision == nil {
		t.Fatal("a halted run emitted no decision")
	}
	if got.Decision.Node != "gate" || got.Decision.Question != "ship it?" || got.Decision.Into != "answer" {
		t.Errorf("Decision = %+v", got.Decision)
	}
	if !equalStrings(got.Decision.Options, []string{"approve", "cancel"}) {
		t.Errorf("Decision.Options = %v", got.Decision.Options)
	}
}

// haltingBuilder declares prep -> gate -> act, where gate halts for a decision
// and routes on the answer.
func haltingBuilder(rec *recorder) *graph.Builder {
	return haltingBuilderWithGate(rec, func(_ context.Context, r graph.Reader, w graph.Writer) error {
		answer, err := r.Get("answer")
		if err != nil {
			return err
		}
		got, _ := answer.Text()
		return w.Set("trace", graph.ListValue("gate:"+got))
	})
}

// haltingBuilderWithGate is haltingBuilder with the halt point's body supplied,
// so a test can decide what happens when the answered node runs.
func haltingBuilderWithGate(rec *recorder, gate graph.Body) *graph.Builder {
	return graph.NewBuilder().
		Start("prep").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Key(graph.Key{Name: "trace", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Node(graph.Node{Name: "prep", Writes: []string{"trace"}, NewBody: appendTrace(rec, "prep")}).
		Node(graph.Node{
			Name:   "gate",
			Reads:  []string{"answer"},
			Writes: []string{"trace"},
			Halt:   &graph.Halt{Question: "ship it?", Options: []string{"approve", "cancel"}, Into: "answer"},
			NewBody: body(func(ctx context.Context, r graph.Reader, w graph.Writer) error {
				rec.note("gate")
				return gate(ctx, r, w)
			}),
		}).
		Node(graph.Node{Name: "act", Writes: []string{"trace"}, NewBody: appendTrace(rec, "act")}).
		Node(graph.Node{Name: "stop", Writes: []string{"trace"}, NewBody: appendTrace(rec, "stop")}).
		Edge(graph.Edge{From: "prep", To: "gate"}).
		Edge(graph.Edge{From: "gate", To: "act", Guard: &graph.Guard{
			Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("approve"),
		}}).
		Edge(graph.Edge{From: "gate", To: "stop"})
}

func TestNodeDeclarationsAreEnforced(t *testing.T) {
	cases := map[string]struct {
		node graph.Node
		want error
	}{
		"read a key it did not declare": {
			node: graph.Node{
				Name: "a",
				NewBody: body(func(_ context.Context, r graph.Reader, _ graph.Writer) error {
					_, err := r.Get("secret")
					return err
				}),
			},
			want: graph.ErrUndeclaredRead,
		},
		"write a key it did not declare": {
			node: graph.Node{
				Name: "a",
				NewBody: body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
					return w.Set("secret", graph.TextValue("x"))
				}),
			},
			want: graph.ErrUndeclaredWrite,
		},
		"write a value of the wrong kind": {
			node: graph.Node{
				Name:   "a",
				Writes: []string{"secret"},
				NewBody: body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
					return w.Set("secret", graph.IntValue(1))
				}),
			},
			want: graph.ErrKindMismatch,
		},
		"ignore the refusal it was handed": {
			node: graph.Node{
				Name: "a",
				NewBody: body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
					_ = w.Set("secret", graph.TextValue("x"))
					return nil
				}),
			},
			want: graph.ErrUndeclaredWrite,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := mustBuild(t, graph.NewBuilder().
				Start("a").
				Key(graph.Key{Name: "secret", Kind: graph.KindText}).
				Node(tc.node))
			exec := mustExecutor(t, g, graph.NewMemoryStore(), 10)
			_, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
			if !errors.Is(err, tc.want) {
				t.Fatalf("Run error = %v, want it to wrap %v", err, tc.want)
			}
			if !errors.Is(err, graph.ErrNodeFailed) {
				t.Errorf("Run error = %v, want it to wrap ErrNodeFailed", err)
			}
		})
	}
}

func TestAFailedNodeLeavesNoWritesAndNoCheckpoint(t *testing.T) {
	boom := errors.New("the fixer gave up")
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("first").
		Key(graph.Key{Name: "trace", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Node(graph.Node{Name: "first", Writes: []string{"trace"}, NewBody: appendTrace(rec, "first")}).
		Node(graph.Node{
			Name:   "second",
			Writes: []string{"trace"},
			NewBody: body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
				rec.note("second")
				if err := w.Set("trace", graph.ListValue("second")); err != nil {
					return err
				}
				return boom
			}),
		}).
		Edge(graph.Edge{From: "first", To: "second"}))

	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)
	_, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
	if !errors.Is(err, boom) {
		t.Fatalf("Run error = %v, want it to wrap the node's error", err)
	}

	history, err := store.History(context.Background(), "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	// One checkpoint for the start, one after "first", and none for the node
	// that failed.
	if len(history) != 2 {
		t.Fatalf("history has %d checkpoints, want 2", len(history))
	}
	latest := history[len(history)-1]
	if got := list(t, latest.State, "trace"); !equalStrings(got, []string{"first"}) {
		t.Errorf("trace = %v, want the failed node's write discarded", got)
	}
	if latest.Position != "second" {
		t.Errorf("Position = %q, want the node that had not run", latest.Position)
	}
}

func TestMergeRulesCombineWritesRatherThanReplacingThem(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("tick").
		Key(graph.Key{Name: "total", Kind: graph.KindInt, Merge: graph.MergeSum}).
		Key(graph.Key{Name: "trail", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Key(graph.Key{Name: "last", Kind: graph.KindText, Merge: graph.MergeLastWriteWins}).
		Node(graph.Node{
			Name:   "tick",
			Reads:  []string{"total"},
			Writes: []string{"total", "trail", "last"},
			NewBody: body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
				rec.note("tick")
				if err := w.Set("total", graph.IntValue(2)); err != nil {
					return err
				}
				if err := w.Set("trail", graph.ListValue("step")); err != nil {
					return err
				}
				return w.Set("last", graph.TextValue("tick"))
			}),
		}).
		Node(graph.Node{
			Name:   "tock",
			Writes: []string{"total", "trail", "last"},
			NewBody: body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
				rec.note("tock")
				if err := w.Set("total", graph.IntValue(3)); err != nil {
					return err
				}
				if err := w.Set("trail", graph.ListValue("step")); err != nil {
					return err
				}
				return w.Set("last", graph.TextValue("tock"))
			}),
		}).
		Edge(graph.Edge{From: "tick", To: "tock"}))

	exec := mustExecutor(t, g, graph.NewMemoryStore(), 10)
	got, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := number(t, got.State, "total"); n != 5 {
		t.Errorf("total = %d, want 5 under the sum rule", n)
	}
	if trail := list(t, got.State, "trail"); !equalStrings(trail, []string{"step", "step"}) {
		t.Errorf("trail = %v, want both writes appended", trail)
	}
	if s := text(t, got.State, "last"); s != "tock" {
		t.Errorf("last = %q, want the last write to win", s)
	}
}

func TestNodeBodiesAreConstructedPerRun(t *testing.T) {
	g := mustBuild(t, graph.NewBuilder().
		Start("tick").
		Key(graph.Key{Name: "seen", Kind: graph.KindInt}).
		Node(graph.Node{
			Name:   "tick",
			Writes: []string{"seen"},
			NewBody: func() graph.Body {
				// This counter belongs to one run. If the executor shared a
				// body between runs, concurrent runs would see each other's.
				seen := int64(0)
				return func(_ context.Context, _ graph.Reader, w graph.Writer) error {
					seen++
					return w.Set("seen", graph.IntValue(seen))
				}
			},
		}).
		Node(graph.Node{Name: "done", NewBody: graph.Stateless(
			func(_ context.Context, _ graph.Reader, _ graph.Writer) error { return nil })}).
		Edge(graph.Edge{From: "tick", To: "tick", Rounds: 5, Guard: &graph.Guard{
			Key: "seen", Op: graph.OpLessThan, Value: graph.IntValue(3),
		}}).
		Edge(graph.Edge{From: "tick", To: "done"}))

	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 20)

	const runs = 8
	results := make([]graph.Result, runs)
	errs := make([]error, runs)
	var wg sync.WaitGroup
	for i := 0; i < runs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "run-" + string(rune('a'+i))
			results[i], errs[i] = exec.Run(context.Background(), name, mustStateNoHelper(g))
		}(i)
	}
	wg.Wait()

	for i := range results {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		if results[i].Status != graph.StatusCompleted {
			t.Fatalf("run %d: Status = %s, want completed", i, results[i].Status)
		}
		if n := number(t, results[i].State, "seen"); n != 3 {
			t.Errorf("run %d: seen = %d, want 3; a shared body would have counted other runs' steps", i, n)
		}
	}
}

// mustStateNoHelper builds an initial state off the test goroutine, where
// t.Fatalf is not allowed.
func mustStateNoHelper(g *graph.Graph) graph.State {
	s, err := g.NewState(nil)
	if err != nil {
		panic(err)
	}
	return s
}

func TestRunRefusesANameThatAlreadyHasHistory(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("only").
		Node(graph.Node{Name: "only", NewBody: noteOnly(rec, "only")}))
	store := graph.NewMemoryStore()
	exec := mustExecutor(t, g, store, 10)

	if _, err := exec.Run(context.Background(), "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
	if !errors.Is(err, graph.ErrRunExists) {
		t.Fatalf("second Run error = %v, want ErrRunExists", err)
	}
	if rec.count("only") != 1 {
		t.Errorf("the node ran %d times, want 1", rec.count("only"))
	}
}

func TestNewExecutorRequiresABudgetAndAStore(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("only").
		Node(graph.Node{Name: "only", NewBody: noteOnly(rec, "only")}))

	if _, err := graph.NewExecutor(g, graph.Config{Store: graph.NewMemoryStore()}); err == nil {
		t.Error("NewExecutor accepted a run with no step budget")
	}
	if _, err := graph.NewExecutor(g, graph.Config{Budget: 5}); err == nil {
		t.Error("NewExecutor accepted a run with no checkpoint store")
	}
	if _, err := graph.NewExecutor(nil, graph.Config{Store: graph.NewMemoryStore(), Budget: 5}); err == nil {
		t.Error("NewExecutor accepted a nil graph")
	}
}

func TestNewStateRefusesUndeclaredKeysAndWrongKinds(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("only").
		Key(graph.Key{Name: "name", Kind: graph.KindText}).
		Node(graph.Node{Name: "only", NewBody: noteOnly(rec, "only")}))

	if _, err := g.NewState(map[string]graph.Value{"ghost": graph.TextValue("x")}); !errors.Is(err, graph.ErrUndeclaredKey) {
		t.Errorf("NewState error = %v, want ErrUndeclaredKey", err)
	}
	if _, err := g.NewState(map[string]graph.Value{"name": graph.IntValue(1)}); !errors.Is(err, graph.ErrKindMismatch) {
		t.Errorf("NewState error = %v, want ErrKindMismatch", err)
	}
	s, err := g.NewState(nil)
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if got := text(t, s, "name"); got != "" {
		t.Errorf("an unwritten text key holds %q, want the zero value", got)
	}
}
