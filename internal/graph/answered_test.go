package graph_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

// answeredLoop is a cycle a person drives: work runs, hold asks, an "again"
// answer goes through redo back to work, and anything else finishes. No body
// changes state, so every round is one convergence would park on. answered
// marks the back edge from redo to work.
func answeredLoop(rec *recorder, answered bool) *graph.Builder {
	return graph.NewBuilder().
		Start("work").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Node(graph.Node{Name: "work", NewBody: noteOnly(rec, "work")}).
		Node(graph.Node{Name: "hold", NewBody: noteOnly(rec, "hold"), Reads: []string{"answer"},
			Halt: &graph.Halt{Question: "again?", Options: []string{"again", "done"}, Into: "answer"}}).
		Node(graph.Node{Name: "redo", NewBody: noteOnly(rec, "redo")}).
		Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
		Edge(graph.Edge{From: "work", To: "hold"}).
		Edge(graph.Edge{From: "hold", To: "redo", Rounds: 3,
			Guard: &graph.Guard{Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("again")}}).
		Edge(graph.Edge{From: "hold", To: "done"}).
		Edge(graph.Edge{From: "redo", To: "work", Rounds: 3, Answered: answered})
}

// A back edge marked Answered is exempt from convergence: a round there that
// changed nothing returns to the halt point instead of parking. Its round
// limit still holds. The same graph without the mark parks on the second
// round, which is the positive control that the rounds really change nothing.
func TestAnAnsweredBackEdgeIsExemptFromConvergenceAndNothingElse(t *testing.T) {
	ctx := context.Background()

	rec := &recorder{}
	g := mustBuild(t, answeredLoop(rec, true))
	exec := mustExecutor(t, g, graph.NewMemoryStore(), 100)
	if _, err := exec.Run(ctx, "run", mustState(t, g, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i := range 3 {
		got, err := exec.Answer(ctx, "run", "again")
		if err != nil {
			t.Fatalf("answer %d: %v", i+1, err)
		}
		if got.Status != graph.StatusHalted || got.Position != "hold" {
			t.Fatalf("after answered round %d the run is %s at %q (%s), want halted at the hold",
				i+1, got.Status, got.Position, got.Reason)
		}
	}
	// The round limit is untouched by the exemption, and the answer past it
	// is refused with the decision still open: the same run then takes a
	// different answer.
	if _, err := exec.Answer(ctx, "run", "again"); !errors.Is(err, graph.ErrRoundsSpent) {
		t.Fatalf("a fourth round past a bound of three came to %v, want graph.ErrRoundsSpent", err)
	}
	done, err := exec.Answer(ctx, "run", "done")
	if err != nil || done.Status != graph.StatusCompleted {
		t.Fatalf("finishing the same run after the refused round came to %v, %v", done.Status, err)
	}

	control := &recorder{}
	cg := mustBuild(t, answeredLoop(control, false))
	cexec := mustExecutor(t, cg, graph.NewMemoryStore(), 100)
	if _, err := cexec.Run(ctx, "run", mustState(t, cg, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, err := cexec.Answer(ctx, "run", "again"); err != nil || got.Status != graph.StatusHalted {
		t.Fatalf("the unmarked graph's first round came to %v, %v", got.Status, err)
	}
	got, err := cexec.Answer(ctx, "run", "again")
	if err != nil {
		t.Fatalf("the unmarked graph's second round: %v", err)
	}
	if got.Status != graph.StatusConverged {
		t.Fatalf("the unmarked graph's second no-op round came to %s, want converged", got.Status)
	}
}

// The mark is admitted only where every traversal of the edge follows an
// answer at a halt point, so it cannot be used to switch convergence off for
// a loop nobody drives. answeredLoop with the mark builds, which is the
// control for the refusals.
func TestTheBuildAdmitsAnsweredOnlyAfterAnAnswer(t *testing.T) {
	rec := &recorder{}
	mustBuild(t, answeredLoop(rec, true))

	for name, b := range map[string]*graph.Builder{
		"a forward edge": graph.NewBuilder().Start("a").
			Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}).
			Node(graph.Node{Name: "b", NewBody: noteOnly(rec, "b")}).
			Edge(graph.Edge{From: "a", To: "b", Answered: true}),
		"a loop on the start node": graph.NewBuilder().Start("a").
			Key(graph.Key{Name: "n", Kind: graph.KindInt}).
			Node(graph.Node{Name: "a", Reads: []string{"n"}, NewBody: noteOnly(rec, "a")}).
			Node(graph.Node{Name: "b", NewBody: noteOnly(rec, "b")}).
			Edge(graph.Edge{From: "a", To: "a", Rounds: 2, Answered: true,
				Guard: &graph.Guard{Key: "n", Op: graph.OpEquals, Value: graph.IntValue(0)}}).
			Edge(graph.Edge{From: "a", To: "b"}),
		"a loop entered from no halt point": graph.NewBuilder().Start("a").
			Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}).
			Node(graph.Node{Name: "b", NewBody: noteOnly(rec, "b")}).
			Edge(graph.Edge{From: "a", To: "b"}).
			Edge(graph.Edge{From: "b", To: "a", Rounds: 2, Answered: true}),
		// The way in is guarded, but on a key other than the halt point's
		// answer, so the guard says nothing about what was answered.
		"a way in guarded on another key": graph.NewBuilder().Start("work").
			Key(graph.Key{Name: "answer", Kind: graph.KindText}).
			Key(graph.Key{Name: "other", Kind: graph.KindText}).
			Node(graph.Node{Name: "work", NewBody: noteOnly(rec, "work")}).
			Node(graph.Node{Name: "hold", NewBody: noteOnly(rec, "hold"), Reads: []string{"answer"},
				Halt: &graph.Halt{Question: "again?", Options: []string{"again", "done"}, Into: "answer"}}).
			Node(graph.Node{Name: "redo", NewBody: noteOnly(rec, "redo")}).
			Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
			Edge(graph.Edge{From: "work", To: "hold"}).
			Edge(graph.Edge{From: "hold", To: "redo", Rounds: 3,
				Guard: &graph.Guard{Key: "other", Op: graph.OpEquals, Value: graph.TextValue("again")}}).
			Edge(graph.Edge{From: "hold", To: "done"}).
			Edge(graph.Edge{From: "redo", To: "work", Rounds: 3, Answered: true}),
		// One way in is the valid one and a second comes from a node that is
		// no halt point, so the check has to look at every way in rather
		// than the first.
		"a second way in from no halt point": graph.NewBuilder().Start("work").
			Key(graph.Key{Name: "answer", Kind: graph.KindText}).
			Node(graph.Node{Name: "work", NewBody: noteOnly(rec, "work")}).
			Node(graph.Node{Name: "hold", NewBody: noteOnly(rec, "hold"), Reads: []string{"answer"},
				Halt: &graph.Halt{Question: "again?", Options: []string{"again", "side", "done"}, Into: "answer"}}).
			Node(graph.Node{Name: "side", NewBody: noteOnly(rec, "side")}).
			Node(graph.Node{Name: "redo", NewBody: noteOnly(rec, "redo")}).
			Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
			Edge(graph.Edge{From: "work", To: "hold"}).
			Edge(graph.Edge{From: "hold", To: "redo", Rounds: 3,
				Guard: &graph.Guard{Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("again")}}).
			Edge(graph.Edge{From: "hold", To: "side",
				Guard: &graph.Guard{Key: "answer", Op: graph.OpEquals, Value: graph.TextValue("side")}}).
			Edge(graph.Edge{From: "hold", To: "done"}).
			Edge(graph.Edge{From: "side", To: "redo"}).
			Edge(graph.Edge{From: "redo", To: "work", Rounds: 3, Answered: true}),
		"a halt point's unguarded way in": graph.NewBuilder().Start("work").
			Key(graph.Key{Name: "answer", Kind: graph.KindText}).
			Node(graph.Node{Name: "work", NewBody: noteOnly(rec, "work")}).
			Node(graph.Node{Name: "hold", NewBody: noteOnly(rec, "hold"),
				Halt: &graph.Halt{Question: "?", Into: "answer"}}).
			Node(graph.Node{Name: "redo", NewBody: noteOnly(rec, "redo")}).
			Edge(graph.Edge{From: "work", To: "hold"}).
			Edge(graph.Edge{From: "hold", To: "redo"}).
			Edge(graph.Edge{From: "redo", To: "work", Rounds: 2, Answered: true}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := b.Build()
			requireBuildError(t, err, graph.RuleBoundedCycle)
		})
	}
}

// haltedAt runs g on store to its first halt and returns the executor.
func haltedAt(t *testing.T, g *graph.Graph, store graph.CheckpointStore) *graph.Executor {
	t.Helper()
	exec := mustExecutor(t, g, store, 100)
	got, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != graph.StatusHalted {
		t.Fatalf("the run is %s, want halted", got.Status)
	}
	return exec
}

// latest is the run's latest checkpoint in store.
func latest(t *testing.T, store graph.CheckpointStore) graph.Checkpoint {
	t.Helper()
	cp, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	return cp
}

// The mark is part of the edge digest in both directions, because it decides
// whether a fingerprint is compared at all: a run recorded under the marked
// loop is refused by the unmarked one and the other way round, with the
// refusal the digest already gives for edges that differ.
func TestTheAnsweredMarkIsPartOfTheEdgeDigest(t *testing.T) {
	for name, tc := range map[string]struct{ recorded, resumed bool }{
		"marked then unmarked": {true, false},
		"unmarked then marked": {false, true},
	} {
		t.Run(name, func(t *testing.T) {
			rec := &recorder{}
			store := graph.NewMemoryStore()
			haltedAt(t, mustBuild(t, answeredLoop(rec, tc.recorded)), store)
			other := mustExecutor(t, mustBuild(t, answeredLoop(rec, tc.resumed)), store, 100)
			_, err := other.Answer(context.Background(), "run", "again")
			var ce *graph.CheckpointError
			if !errors.As(err, &ce) || ce.Field != "counters.edge_digest" {
				t.Fatalf("answering under the other marking came to %v, want the edge digest refused", err)
			}
		})
	}
}

// The digest of a graph that marks no edge is the one the same edges had
// before Edge.Answered existed, so runs recorded before it resume. The
// unmarked literal was taken from the build before the field was added, over
// these same edges; the marked one pins that the mark changes it.
func TestTheEdgeDigestOfAnUnmarkedGraphIsUnchanged(t *testing.T) {
	const (
		unmarked = "2976e4953d07afaae89f12c7ba0537c96ce698fcd53025c997360b63a42fb184"
		marked   = "ef6fdafddaa8d5e8e3c26646d47eb73ac1dafadab39365bafcae48ffbaffcabd"
	)
	for _, tc := range []struct {
		answered bool
		want     string
	}{{false, unmarked}, {true, marked}} {
		rec := &recorder{}
		store := graph.NewMemoryStore()
		haltedAt(t, mustBuild(t, answeredLoop(rec, tc.answered)), store)
		if got := latest(t, store).Counters.EdgeDigest; got != tc.want {
			t.Errorf("answered=%v: the edge digest is %s, want %s", tc.answered, got, tc.want)
		}
	}
}

// The exemption and the counts it sits beside survive a restart: an
// executor built afresh over the same store carries the run on, a no-op
// round there still returns to the hold, and the round bound still counts the
// round taken before the restart.
func TestAnAnsweredLoopContinuesUnderAFreshExecutor(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	store := graph.NewMemoryStore()
	first := haltedAt(t, mustBuild(t, answeredLoop(rec, true)), store)
	if got, err := first.Answer(ctx, "run", "again"); err != nil || got.Status != graph.StatusHalted {
		t.Fatalf("the first round came to %v, %v", got.Status, err)
	}

	fresh := mustExecutor(t, mustBuild(t, answeredLoop(rec, true)), store, 100)
	for i := range 2 {
		got, err := fresh.Answer(ctx, "run", "again")
		if err != nil || got.Status != graph.StatusHalted || got.Position != "hold" {
			t.Fatalf("round %d after the restart came to %v at %q (%s), %v", i+2, got.Status, got.Position, got.Reason, err)
		}
	}
	if _, err := fresh.Answer(ctx, "run", "again"); !errors.Is(err, graph.ErrRoundsSpent) {
		t.Fatalf("a fourth round across the restart came to %v, want graph.ErrRoundsSpent", err)
	}
	if got, err := fresh.Answer(ctx, "run", "done"); err != nil || got.Status != graph.StatusCompleted {
		t.Fatalf("finishing came to %v, %v", got.Status, err)
	}
}

// AnswerWith refuses a detail the halt point does not take with that answer,
// and an answer that needs one arriving without it, and in both cases writes
// nothing: the latest checkpoint is exactly what it was. The accepted answer
// at the end is the control that the refusals were about the detail.
func TestAnAnswerWithAMisplacedDetailWritesNothing(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("work").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Key(graph.Key{Name: "detail", Kind: graph.KindText}).
		Node(graph.Node{Name: "work", NewBody: noteOnly(rec, "work")}).
		Node(graph.Node{Name: "hold", NewBody: noteOnly(rec, "hold"),
			Halt: &graph.Halt{Question: "how?", Options: []string{"fix", "done"}, Into: "answer",
				Detail: "detail", Detailed: []string{"fix"}}}).
		Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
		Edge(graph.Edge{From: "work", To: "hold"}).
		Edge(graph.Edge{From: "hold", To: "done"}))
	store := graph.NewMemoryStore()
	exec := haltedAt(t, g, store)
	before := latest(t, store)

	for name, tc := range map[string]struct {
		answer, detail string
		want           error
	}{
		"a detail with an answer that takes none":   {"done", "and this", graph.ErrDetailNotAccepted},
		"an answer that needs a detail without one": {"fix", "", graph.ErrDetailRequired},
	} {
		if _, err := exec.AnswerWith(ctx, "run", tc.answer, tc.detail); !errors.Is(err, tc.want) {
			t.Fatalf("%s came to %v, want %v", name, err, tc.want)
		}
		if after := latest(t, store); !reflect.DeepEqual(after, before) {
			t.Fatalf("%s changed the checkpoint:\nbefore %+v\nafter  %+v", name, before, after)
		}
	}
	if got, err := exec.AnswerWith(ctx, "run", "fix", "the detail"); err != nil || got.Status != graph.StatusCompleted {
		t.Fatalf("a well-formed answer came to %v, %v", got.Status, err)
	}
}
