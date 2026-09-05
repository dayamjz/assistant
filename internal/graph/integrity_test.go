package graph_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

// digestBuilder returns a three-edge graph with one bounded cycle, shaped so a
// test can change exactly one thing about one edge without changing how many
// edges there are. Every variant declares the same nodes, the same state keys,
// and the same node bodies, so a checkpoint written under one and refused by
// another is refused for its edges and nothing else.
//
// fail decides which execution of "fix" returns an error, which is how a test
// ends a segment with the run standing mid-cycle with the back edge already
// counted.
func digestBuilder(rec *recorder, guard int64, rounds int, loops bool, fail int) *graph.Builder {
	calls := 0
	b := graph.NewBuilder().
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
			Name:   "fix",
			Writes: []string{"log"},
			NewBody: body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
				rec.note("fix")
				calls++
				if calls == fail {
					return errors.New("interrupted")
				}
				return w.Set("log", graph.ListValue("round "+strconv.Itoa(calls)))
			}),
		}).
		Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
		Edge(graph.Edge{From: "check", To: "fix", Guard: &graph.Guard{
			Key: "findings", Op: graph.OpEquals, Value: graph.IntValue(guard),
		}}).
		Edge(graph.Edge{From: "check", To: "done"})
	if loops {
		return b.Edge(graph.Edge{From: "fix", To: "check", Rounds: rounds})
	}
	return b.Edge(graph.Edge{From: "fix", To: "done"})
}

// TestCountersAreRefusedByAGraphWhoseEdgesDiffer is the defect this check
// exists for. Traversals and Fingerprints are indexed by edge, so counters read
// against another graph's edges are read against edges that did not produce
// them, and comparing how many entries they hold does not notice: every
// variant below has exactly the three edges the run's own graph had.
//
// The control is the same resume onto a graph built again from the same
// declarations. It must be admitted, or the refusals below would prove only
// that a rebuilt graph is refused, which would make every resume across a
// process restart fail rather than these four.
func TestCountersAreRefusedByAGraphWhoseEdgesDiffer(t *testing.T) {
	ctx := context.Background()

	// The run stops with its second round of "fix" failing, so its latest
	// checkpoint stands mid-cycle with the bounded back edge counted once.
	interrupted := func() (*graph.Graph, graph.CheckpointStore) {
		t.Helper()
		rec := &recorder{}
		g := mustBuild(t, digestBuilder(rec, 1, 3, true, 2))
		store := graph.NewMemoryStore()
		if _, err := mustExecutor(t, g, store, 50).Run(ctx, "run", mustState(t, g, nil)); !errors.Is(err, graph.ErrNodeFailed) {
			t.Fatalf("Run = %v, want the second round of fix to fail", err)
		}
		cp, err := store.Latest(ctx, "run")
		if err != nil {
			t.Fatalf("Latest: %v", err)
		}
		if cp.Position != "fix" || cp.Counters.Traversals[2] != 1 {
			t.Fatalf("the run stands at %q with traversals %v, want it mid-cycle with the back edge counted once",
				cp.Position, cp.Counters.Traversals)
		}
		return g, store
	}

	t.Run("the same edges, built again", func(t *testing.T) {
		g, store := interrupted()
		rec := &recorder{}
		same := mustBuild(t, digestBuilder(rec, 1, 3, true, 2))
		if len(same.Edges()) != len(g.Edges()) {
			t.Fatalf("the control graph has %d edges, the run's had %d", len(same.Edges()), len(g.Edges()))
		}
		// The body fails on its second execution again, and reaching that
		// failure is the point: the resume got past validation and ran a node.
		if _, err := mustExecutor(t, same, store, 50).Resume(ctx, "run"); !errors.Is(err, graph.ErrNodeFailed) {
			t.Fatalf("resuming onto the same edges built again = %v, want the run admitted and fix run", err)
		}
		if rec.count("fix") == 0 {
			t.Error("the admitted resume ran no node")
		}
	})

	variants := map[string]*graph.Builder{
		"a different bound on the cycle":   digestBuilder(&recorder{}, 1, 2, true, 0),
		"a different guard on an edge":     digestBuilder(&recorder{}, 2, 3, true, 0),
		"an edge that goes somewhere else": digestBuilder(&recorder{}, 1, 3, false, 0),
	}
	for name, builder := range variants {
		t.Run(name, func(t *testing.T) {
			g, store := interrupted()
			other := mustBuild(t, builder)
			if len(other.Edges()) != len(g.Edges()) {
				t.Fatalf("this variant has %d edges and the run's graph had %d, so a length check would "+
					"already have caught it and the case proves nothing",
					len(other.Edges()), len(g.Edges()))
			}
			before, err := store.History(ctx, "run")
			if err != nil {
				t.Fatalf("History: %v", err)
			}
			_, err = mustExecutor(t, other, store, 50).Resume(ctx, "run")
			var ce *graph.CheckpointError
			if !errors.As(err, &ce) {
				t.Fatalf("Resume = %v, want a *graph.CheckpointError refusing the counters", err)
			}
			if ce.Field != "counters.edge_digest" {
				t.Fatalf("the refusal blames %q (%s), want it to name the counters' edge digest", ce.Field, ce.Detail)
			}
			after, err := store.History(ctx, "run")
			if err != nil {
				t.Fatalf("History: %v", err)
			}
			if len(after) != len(before) {
				t.Errorf("the refused resume wrote %d checkpoints, want none", len(after)-len(before))
			}
		})
	}
}

// TestResumingUnderADifferentBudgetIsRefused is the other half of the same
// shape: a bound read from executor configuration rather than from the run is
// a bound that can differ across a resume with nothing said about it.
func TestResumingUnderADifferentBudgetIsRefused(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()

	held, err := mustExecutor(t, g, store, 20).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if held.Status != graph.StatusHalted {
		t.Fatalf("the run ended %s, want halted at its decision", held.Status)
	}
	if held.Budget != 20 {
		t.Errorf("the run reports a budget of %d, want the 20 it started under", held.Budget)
	}
	before, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	// Raising it is refused as readily as lowering it. Neither is wrong to
	// want; deciding it silently is.
	for _, budget := range []int{50, 3} {
		_, err := mustExecutor(t, g, store, budget).Resume(ctx, "run")
		if !errors.Is(err, graph.ErrBudgetChanged) {
			t.Fatalf("resuming a run recorded at 20 with an executor at %d = %v, want ErrBudgetChanged",
				budget, err)
		}
		if errors.Is(err, graph.ErrBudgetSpent) {
			t.Errorf("the refusal at %d reads as a spent budget, which is a different thing", budget)
		}
	}
	after, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the refused resumes wrote %d checkpoints, want none", len(after)-len(before))
	}

	// The control: the same resume under the budget the run recorded, which
	// re-emits the decision and still writes nothing.
	got, err := mustExecutor(t, g, store, 20).Resume(ctx, "run")
	if err != nil {
		t.Fatalf("Resume under the run's own budget: %v", err)
	}
	if got.Status != graph.StatusHalted || got.Decision == nil {
		t.Fatalf("the restored run is %s carrying %v, want its decision re-emitted", got.Status, got.Decision)
	}
}

// TestAdoptBudgetRecordsTheChangeAndRevivesTheRunItParked is the deliberate
// half. A budget is not immutable, because wanting to give a run more room is
// legitimate; what it may not be is different without being recorded.
func TestAdoptBudgetRecordsTheChangeAndRevivesTheRunItParked(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()

	// A budget of one is spent by "prep", so the run parks in front of the
	// gate without asking its question.
	parked, err := mustExecutor(t, g, store, 1).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if parked.Status != graph.StatusBudgetExhausted || parked.Position != "gate" {
		t.Fatalf("the run ended %s at %q, want budget_exhausted standing at gate", parked.Status, parked.Position)
	}

	roomy := mustExecutor(t, g, store, 20)
	got, err := roomy.AdoptBudget(ctx, "run")
	if err != nil {
		t.Fatalf("AdoptBudget: %v", err)
	}
	if got.Budget != 20 {
		t.Errorf("the run reports a budget of %d, want the 20 it adopted", got.Budget)
	}
	// Raising the budget is the remedy for the bound that parked the run, the
	// way an answer is the remedy for a halt, and the gate never started, so
	// the decision it was holding is asked now rather than replayed.
	if got.Status != graph.StatusHalted || got.Decision == nil {
		t.Fatalf("the revived run is %s carrying %v, want it halted at the gate's decision", got.Status, got.Decision)
	}
	if got.Reason != "" {
		t.Errorf("the revived run still reports %q, want the park it was released from left behind", got.Reason)
	}
	if rec.count("gate") != 0 {
		t.Error("adopting a budget ran the halt point's body")
	}

	// The change is in the history rather than in one executor's head.
	history, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if n := len(history); n < 2 || history[n-1].Counters.Budget != 20 || history[n-2].Counters.Budget != 1 {
		t.Fatalf("the history records budgets %v, want the change from 1 to 20 written down", budgets(history))
	}

	done, err := roomy.Answer(ctx, "run", "approve")
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("the answered run ended %s, want completed", done.Status)
	}
}

// budgets renders the budget recorded by each checkpoint in a history.
func budgets(history []graph.Checkpoint) []int {
	out := make([]int, len(history))
	for i, cp := range history {
		out[i] = cp.Counters.Budget
	}
	return out
}

// TestAdoptBudgetDoesNotReleaseTheOtherTwoBounds keeps the three bounds three.
// The run-wide budget is the only one AdoptBudget decides, so a run either of
// the others parked stays parked whatever budget it is given.
func TestAdoptBudgetDoesNotReleaseTheOtherTwoBounds(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		builder *graph.Builder
		want    graph.Status
	}{
		"a run its round limit parked": {
			// A fixer that changes state every round, so convergence cannot be
			// what stops it and the round limit is.
			builder: digestBuilder(&recorder{}, 1, 1, true, 0),
			want:    graph.StatusRoundsExhausted,
		},
		"a converged run": {
			// A fixer that writes nothing, so the round limit is nowhere near
			// reached when the round that changes nothing ends the loop.
			builder: graph.NewBuilder().
				Start("check").
				Key(graph.Key{Name: "findings", Kind: graph.KindInt}).
				Key(graph.Key{Name: "log", Kind: graph.KindList, Merge: graph.MergeAppend}).
				Node(graph.Node{
					Name:   "check",
					Writes: []string{"findings"},
					NewBody: body(func(_ context.Context, _ graph.Reader, w graph.Writer) error {
						return w.Set("findings", graph.IntValue(1))
					}),
				}).
				Node(graph.Node{Name: "fix", NewBody: body(func(context.Context, graph.Reader, graph.Writer) error {
					return nil
				})}).
				Node(graph.Node{Name: "done", NewBody: body(func(context.Context, graph.Reader, graph.Writer) error {
					return nil
				})}).
				Edge(graph.Edge{From: "check", To: "fix", Guard: &graph.Guard{
					Key: "findings", Op: graph.OpEquals, Value: graph.IntValue(1),
				}}).
				Edge(graph.Edge{From: "check", To: "done"}).
				Edge(graph.Edge{From: "fix", To: "check", Rounds: 50}),
			want: graph.StatusConverged,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := mustBuild(t, tc.builder)
			store := graph.NewMemoryStore()
			parked, err := mustExecutor(t, g, store, 30).Run(ctx, "run", mustState(t, g, nil))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if parked.Status != tc.want {
				t.Fatalf("the run ended %s (%s), want %s", parked.Status, parked.Reason, tc.want)
			}

			got, err := mustExecutor(t, g, store, 300).AdoptBudget(ctx, "run")
			if err != nil {
				t.Fatalf("AdoptBudget: %v", err)
			}
			if got.Status != tc.want {
				t.Errorf("the run is %s after a tenfold budget, want it still %s: the budget is not this bound",
					got.Status, tc.want)
			}
			if got.Budget != 300 {
				t.Errorf("the run reports a budget of %d, want the 300 it adopted", got.Budget)
			}
			if got.Reason == "" {
				t.Error("the still-parked run reports no reason")
			}
		})
	}
}

// TestAdoptBudgetWritesNothingWhenThereIsNothingToRecord covers the two calls
// that have nothing to say: one that would record the budget the run already
// has, and one on a run with no bound left to change.
func TestAdoptBudgetWritesNothingWhenThereIsNothingToRecord(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, haltingBuilder(rec))
	store := graph.NewMemoryStore()

	if _, err := mustExecutor(t, g, store, 20).Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	before, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	got, err := mustExecutor(t, g, store, 20).AdoptBudget(ctx, "run")
	if err != nil {
		t.Fatalf("AdoptBudget with the run's own budget: %v", err)
	}
	if got.Status != graph.StatusHalted {
		t.Errorf("the run is %s, want it left exactly as it stood", got.Status)
	}
	after, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("adopting the budget the run already had wrote %d checkpoints, want none", len(after)-len(before))
	}

	if _, err := mustExecutor(t, g, store, 20).Answer(ctx, "run", "approve"); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	finished, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if _, err := mustExecutor(t, g, store, 40).AdoptBudget(ctx, "run"); !errors.Is(err, graph.ErrRunCompleted) {
		t.Fatalf("adopting a budget onto a completed run = %v, want ErrRunCompleted", err)
	}
	last, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(last) != len(finished) {
		t.Errorf("the refused adoption wrote %d checkpoints, want none", len(last)-len(finished))
	}
}

// linearBuilder returns a straight line of nodes with no halt point and no
// cycle, so the run-wide step budget is the only thing that can stop a run of
// it and every node it parks in front of is an ordinary one.
func linearBuilder(rec *recorder, names ...string) *graph.Builder {
	b := graph.NewBuilder().Start(names[0])
	for i, name := range names {
		b = b.Node(graph.Node{Name: name, NewBody: noteOnly(rec, name)})
		if i > 0 {
			b = b.Edge(graph.Edge{From: names[i-1], To: name})
		}
	}
	return b
}

// TestAdoptBudgetRevivesARunParkedInFrontOfAnOrdinaryNode is what raising a
// budget is for. The node the run stands in front of is not a halt point, so
// reviving it leaves it running rather than halted, and the run then has to
// actually go on: a revival that reported StatusRunning without the resume
// carrying on would have released nothing.
func TestAdoptBudgetRevivesARunParkedInFrontOfAnOrdinaryNode(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, linearBuilder(rec, "one", "two", "three"))
	store := graph.NewMemoryStore()

	parked, err := mustExecutor(t, g, store, 1).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if parked.Status != graph.StatusBudgetExhausted || parked.Position != "two" {
		t.Fatalf("the run ended %s at %q, want budget_exhausted standing in front of two",
			parked.Status, parked.Position)
	}

	roomy := mustExecutor(t, g, store, 3)
	revived, err := roomy.AdoptBudget(ctx, "run")
	if err != nil {
		t.Fatalf("AdoptBudget: %v", err)
	}
	if revived.Status != graph.StatusRunning {
		t.Fatalf("the revived run is %s (%s), want it running: the node it stands at asks nothing",
			revived.Status, revived.Reason)
	}
	if revived.Position != "two" {
		t.Errorf("the revived run stands at %q, want the node that never started", revived.Position)
	}
	if revived.Reason != "" {
		t.Errorf("the revived run still reports %q, want the park it was released from left behind", revived.Reason)
	}
	if revived.Budget != 3 {
		t.Errorf("the run reports a budget of %d, want the 3 it adopted", revived.Budget)
	}
	if got := rec.order(); len(got) != 1 || got[0] != "one" {
		t.Fatalf("adopting a budget ran %v, want only the node the original budget paid for", got)
	}

	done, err := roomy.Resume(ctx, "run")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("the resumed run ended %s (%s), want completed: raising the budget released the bound",
			done.Status, done.Reason)
	}
	if got := rec.order(); !equalStrings(got, []string{"one", "two", "three"}) {
		t.Errorf("the run executed %v, want it to continue from the node it was parked in front of", got)
	}
	if done.Steps != 3 {
		t.Errorf("the run spent %d steps, want the 3 its adopted budget allowed", done.Steps)
	}
}

// TestAdoptBudgetReParksARunTheNewBudgetStillLeavesNoStepFor covers the third
// outcome: a budget may be lowered, and one lowered past what the run has
// already spent releases nothing. The run parks again, and the reason it
// carries has to name the budget it is now held to rather than the one it was
// released from, because a reason naming the old number would describe a bound
// that is no longer in force.
func TestAdoptBudgetReParksARunTheNewBudgetStillLeavesNoStepFor(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, linearBuilder(rec, "one", "two", "three", "four"))
	store := graph.NewMemoryStore()

	parked, err := mustExecutor(t, g, store, 2).Run(ctx, "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if parked.Status != graph.StatusBudgetExhausted || parked.Position != "three" {
		t.Fatalf("the run ended %s at %q, want budget_exhausted standing in front of three",
			parked.Status, parked.Position)
	}

	got, err := mustExecutor(t, g, store, 1).AdoptBudget(ctx, "run")
	if err != nil {
		t.Fatalf("AdoptBudget: %v", err)
	}
	if got.Status != graph.StatusBudgetExhausted {
		t.Fatalf("the run is %s after a budget below what it had spent, want it parked again", got.Status)
	}
	if got.Budget != 1 || got.Steps != 2 {
		t.Fatalf("the run reports %d steps against a budget of %d, want 2 spent against the adopted 1",
			got.Steps, got.Budget)
	}
	if !strings.Contains(got.Reason, strconv.Itoa(got.Budget)) {
		t.Errorf("the re-parked run reports %q, want a reason naming the budget of %d now in force",
			got.Reason, got.Budget)
	}
	if strings.Contains(got.Reason, strconv.Itoa(parked.Budget)) {
		t.Errorf("the re-parked run reports %q, want the budget of %d it was released from gone from it",
			got.Reason, parked.Budget)
	}
	if !strings.Contains(got.Reason, got.Position) {
		t.Errorf("the re-parked run reports %q, want a reason naming node %q", got.Reason, got.Position)
	}
	if n := rec.count("three"); n != 0 {
		t.Errorf("the node the run is parked in front of ran %d times, want 0", n)
	}

	// The lowering is in the history like any other adoption, and the run is
	// still parked rather than resumable.
	after, err := mustExecutor(t, g, store, 1).Resume(ctx, "run")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if after.Status != graph.StatusBudgetExhausted || after.Reason != got.Reason {
		t.Errorf("resuming the re-parked run gave %s (%s), want it returned exactly as it stood",
			after.Status, after.Reason)
	}
	history, err := store.History(ctx, "run")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if n := len(history); n < 2 || history[n-1].Counters.Budget != 1 || history[n-2].Counters.Budget != 2 {
		t.Fatalf("the history records budgets %v, want the change from 2 to 1 written down", budgets(history))
	}
}
