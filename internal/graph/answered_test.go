package graph_test

import (
	"context"
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
	// The round limit is untouched by the exemption.
	if _, err := exec.Answer(ctx, "run", "again"); err == nil {
		t.Fatal("a fourth round past a bound of three was accepted")
	}
	done, err := exec.Answer(ctx, "run", "done")
	if err != nil || done.Status != graph.StatusCompleted {
		t.Fatalf("finishing came to %v, %v", done.Status, err)
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
