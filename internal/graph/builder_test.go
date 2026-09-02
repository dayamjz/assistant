package graph_test

import (
	"context"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

func TestBuildRefusesUnboundedBackEdge(t *testing.T) {
	rec := &recorder{}
	// Rounds of 0 on the back edge is no bound at all.
	_, err := fixLoopBuilder(rec, 0, func(_ context.Context, _ graph.Reader, _ graph.Writer) error {
		return nil
	}).Build()

	be := requireBuildError(t, err, graph.RuleBoundedCycle)
	if !strings.Contains(be.Error(), `from "fix" to "check"`) {
		t.Errorf("refusal does not name the offending edge: %v", be)
	}
	if rec.count("check") != 0 {
		t.Error("a refused graph ran a node body")
	}
}

func TestBuildAcceptsBoundedBackEdgeAndIdentifiesIt(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, fixLoopBuilder(rec, 3, func(_ context.Context, _ graph.Reader, _ graph.Writer) error {
		return nil
	}))

	var back []string
	for i, e := range g.Edges() {
		if g.IsBackEdge(i) {
			back = append(back, e.From+"->"+e.To)
		}
	}
	if len(back) != 1 || back[0] != "fix->check" {
		t.Fatalf("back edges = %v, want exactly [fix->check]", back)
	}
}

func TestBackEdgeDetectionDoesNotDependOnDeclarationOrder(t *testing.T) {
	rec := &recorder{}
	forward := mustBuild(t, fixLoopBuilder(rec, 2, func(_ context.Context, _ graph.Reader, _ graph.Writer) error {
		return nil
	}))

	// The same topology with the back edge declared before the forward edges.
	reversed := mustBuild(t, graph.NewBuilder().
		Start("check").
		Key(graph.Key{Name: "findings", Kind: graph.KindInt}).
		Key(graph.Key{Name: "log", Kind: graph.KindList, Merge: graph.MergeAppend}).
		Node(graph.Node{Name: "check", Writes: []string{"findings"}, NewBody: noteOnly(rec, "check")}).
		Node(graph.Node{Name: "fix", Writes: []string{"log"}, NewBody: noteOnly(rec, "fix")}).
		Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
		Edge(graph.Edge{From: "fix", To: "check", Rounds: 2}).
		Edge(graph.Edge{From: "check", To: "fix", Guard: &graph.Guard{
			Key: "findings", Op: graph.OpGreaterThan, Value: graph.IntValue(0),
		}}).
		Edge(graph.Edge{From: "check", To: "done"}))

	if got := backEdges(forward); len(got) != 1 || got[0] != "fix->check" {
		t.Fatalf("back edges with the forward declaration order = %v", got)
	}
	if got := backEdges(reversed); len(got) != 1 || got[0] != "fix->check" {
		t.Fatalf("back edges with the reversed declaration order = %v", got)
	}
}

func backEdges(g *graph.Graph) []string {
	var out []string
	for i, e := range g.Edges() {
		if g.IsBackEdge(i) {
			out = append(out, e.From+"->"+e.To)
		}
	}
	return out
}

func TestBuildRefusesSelfLoopWithoutBound(t *testing.T) {
	rec := &recorder{}
	_, err := graph.NewBuilder().
		Start("spin").
		Key(graph.Key{Name: "go", Kind: graph.KindBool}).
		Node(graph.Node{Name: "spin", NewBody: noteOnly(rec, "spin")}).
		Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
		Edge(graph.Edge{From: "spin", To: "spin", Guard: &graph.Guard{
			Key: "go", Op: graph.OpEquals, Value: graph.BoolValue(true),
		}}).
		Edge(graph.Edge{From: "spin", To: "done"}).
		Build()

	be := requireBuildError(t, err, graph.RuleBoundedCycle)
	if !strings.Contains(be.Error(), `from "spin" to "spin"`) {
		t.Errorf("refusal does not name the self loop: %v", be)
	}
}

func TestBuildRefusesTwoWritersWithoutMergeRule(t *testing.T) {
	rec := &recorder{}
	_, err := graph.NewBuilder().
		Start("first").
		Key(graph.Key{Name: "summary", Kind: graph.KindText}).
		Node(graph.Node{Name: "first", Writes: []string{"summary"}, NewBody: noteOnly(rec, "first")}).
		Node(graph.Node{Name: "second", Writes: []string{"summary"}, NewBody: noteOnly(rec, "second")}).
		Edge(graph.Edge{From: "first", To: "second"}).
		Build()

	be := requireBuildError(t, err, graph.RuleSingleWriter)
	for _, want := range []string{`"summary"`, "first", "second"} {
		if !strings.Contains(be.Error(), want) {
			t.Errorf("refusal does not mention %s: %v", want, be)
		}
	}
}

func TestBuildAcceptsTwoWritersWithMergeRule(t *testing.T) {
	rec := &recorder{}
	mustBuild(t, graph.NewBuilder().
		Start("first").
		Key(graph.Key{Name: "summary", Kind: graph.KindText, Merge: graph.MergeLastWriteWins}).
		Node(graph.Node{Name: "first", Writes: []string{"summary"}, NewBody: noteOnly(rec, "first")}).
		Node(graph.Node{Name: "second", Writes: []string{"summary"}, NewBody: noteOnly(rec, "second")}).
		Edge(graph.Edge{From: "first", To: "second"}))
}

func TestBuildCountsAHaltAnswerKeyAsAWrite(t *testing.T) {
	rec := &recorder{}
	// "answer" is written by the halt point on "ask" and by node "note".
	_, err := graph.NewBuilder().
		Start("ask").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Node(graph.Node{
			Name:    "ask",
			Reads:   []string{"answer"},
			Halt:    &graph.Halt{Question: "proceed?", Options: []string{"yes", "no"}, Into: "answer"},
			NewBody: noteOnly(rec, "ask"),
		}).
		Node(graph.Node{Name: "note", Writes: []string{"answer"}, NewBody: noteOnly(rec, "note")}).
		Edge(graph.Edge{From: "ask", To: "note"}).
		Build()

	requireBuildError(t, err, graph.RuleSingleWriter)
}

// forkJoinBuilder declares a fan-out that "split" opens and "join" closes, with
// a bounded back edge from "join" to backTo.
func forkJoinBuilder(rec *recorder, backTo string) *graph.Builder {
	return graph.NewBuilder().
		Start("split").
		Key(graph.Key{Name: "again", Kind: graph.KindBool}).
		Node(graph.Node{Name: "split", Fork: "review", NewBody: noteOnly(rec, "split")}).
		Node(graph.Node{Name: "left", NewBody: noteOnly(rec, "left")}).
		Node(graph.Node{Name: "right", NewBody: noteOnly(rec, "right")}).
		Node(graph.Node{Name: "join", Join: "review", NewBody: noteOnly(rec, "join")}).
		Node(graph.Node{Name: "done", NewBody: noteOnly(rec, "done")}).
		Edge(graph.Edge{From: "split", To: "left", Guard: &graph.Guard{
			Key: "again", Op: graph.OpEquals, Value: graph.BoolValue(true),
		}}).
		Edge(graph.Edge{From: "split", To: "right"}).
		Edge(graph.Edge{From: "left", To: "join"}).
		Edge(graph.Edge{From: "right", To: "join"}).
		Edge(graph.Edge{From: "join", To: backTo, Rounds: 2, Guard: &graph.Guard{
			Key: "again", Op: graph.OpEquals, Value: graph.BoolValue(true),
		}}).
		Edge(graph.Edge{From: "join", To: "done"})
}

func TestBuildRefusesCycleThroughAJoinThatSkipsItsFork(t *testing.T) {
	rec := &recorder{}
	// The cycle join -> left -> join never passes through "split".
	_, err := forkJoinBuilder(rec, "left").Build()

	be := requireBuildError(t, err, graph.RuleJoinCycle)
	for _, want := range []string{`"join"`, `"review"`, `"split"`} {
		if !strings.Contains(be.Error(), want) {
			t.Errorf("refusal does not mention %s: %v", want, be)
		}
	}
}

func TestBuildAcceptsCycleThroughAJoinThatIncludesItsFork(t *testing.T) {
	rec := &recorder{}
	// The same cycle, routed back through the fork.
	mustBuild(t, forkJoinBuilder(rec, "split"))
}

func TestBuildAcceptsAForkAndJoinWithNoCycle(t *testing.T) {
	rec := &recorder{}
	mustBuild(t, graph.NewBuilder().
		Start("split").
		Key(graph.Key{Name: "again", Kind: graph.KindBool}).
		Node(graph.Node{Name: "split", Fork: "review", NewBody: noteOnly(rec, "split")}).
		Node(graph.Node{Name: "left", NewBody: noteOnly(rec, "left")}).
		Node(graph.Node{Name: "right", NewBody: noteOnly(rec, "right")}).
		Node(graph.Node{Name: "join", Join: "review", NewBody: noteOnly(rec, "join")}).
		Edge(graph.Edge{From: "split", To: "left", Guard: &graph.Guard{
			Key: "again", Op: graph.OpEquals, Value: graph.BoolValue(true),
		}}).
		Edge(graph.Edge{From: "split", To: "right"}).
		Edge(graph.Edge{From: "left", To: "join"}).
		Edge(graph.Edge{From: "right", To: "join"}))
}

func TestBuildRefusesAJoinWithNoFork(t *testing.T) {
	rec := &recorder{}
	_, err := graph.NewBuilder().
		Start("a").
		Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}).
		Node(graph.Node{Name: "b", Join: "nothing", NewBody: noteOnly(rec, "b")}).
		Edge(graph.Edge{From: "a", To: "b"}).
		Build()

	requireBuildError(t, err, graph.RuleWellFormed)
}

func TestBuildRefusesUndeclaredStateKeys(t *testing.T) {
	rec := &recorder{}
	cases := map[string]*graph.Builder{
		"read": graph.NewBuilder().
			Start("a").
			Node(graph.Node{Name: "a", Reads: []string{"ghost"}, NewBody: noteOnly(rec, "a")}),
		"write": graph.NewBuilder().
			Start("a").
			Node(graph.Node{Name: "a", Writes: []string{"ghost"}, NewBody: noteOnly(rec, "a")}),
		"guard": graph.NewBuilder().
			Start("a").
			Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}).
			Node(graph.Node{Name: "b", NewBody: noteOnly(rec, "b")}).
			Edge(graph.Edge{From: "a", To: "b", Guard: &graph.Guard{
				Key: "ghost", Op: graph.OpEquals, Value: graph.BoolValue(true),
			}}),
		"halt answer": graph.NewBuilder().
			Start("a").
			Node(graph.Node{
				Name:    "a",
				Halt:    &graph.Halt{Question: "go?", Into: "ghost"},
				NewBody: noteOnly(rec, "a"),
			}),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := b.Build()
			be := requireBuildError(t, err, graph.RuleDeclaredKey)
			if !strings.Contains(be.Error(), "ghost") {
				t.Errorf("refusal does not name the undeclared key: %v", be)
			}
		})
	}
}

func TestBuildRefusesAHaltAnswerKeyThatIsNotText(t *testing.T) {
	rec := &recorder{}
	_, err := graph.NewBuilder().
		Start("a").
		Key(graph.Key{Name: "answer", Kind: graph.KindInt}).
		Node(graph.Node{
			Name:    "a",
			Halt:    &graph.Halt{Question: "go?", Into: "answer"},
			NewBody: noteOnly(rec, "a"),
		}).
		Build()

	requireBuildError(t, err, graph.RuleDeclaredKey)
}

func TestBuildRefusesAMergeRuleTheKindCannotUse(t *testing.T) {
	rec := &recorder{}
	_, err := graph.NewBuilder().
		Start("a").
		Key(graph.Key{Name: "name", Kind: graph.KindText, Merge: graph.MergeSum}).
		Node(graph.Node{Name: "a", Writes: []string{"name"}, NewBody: noteOnly(rec, "a")}).
		Build()

	be := requireBuildError(t, err, graph.RuleDeclaredKey)
	if !strings.Contains(be.Error(), "sum") {
		t.Errorf("refusal does not name the merge rule: %v", be)
	}
}

func TestBuildRefusesGuardsThatCannotApplyToTheirKey(t *testing.T) {
	rec := &recorder{}
	_, err := graph.NewBuilder().
		Start("a").
		Key(graph.Key{Name: "name", Kind: graph.KindText}).
		Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}).
		Node(graph.Node{Name: "b", NewBody: noteOnly(rec, "b")}).
		Edge(graph.Edge{From: "a", To: "b", Guard: &graph.Guard{
			Key: "name", Op: graph.OpGreaterThan, Value: graph.IntValue(1),
		}}).
		Build()

	requireBuildError(t, err, graph.RuleDeclaredKey)
}

func TestBuildRefusesAnEdgeThatCouldNeverBeTaken(t *testing.T) {
	rec := &recorder{}
	_, err := graph.NewBuilder().
		Start("a").
		Key(graph.Key{Name: "ok", Kind: graph.KindBool}).
		Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}).
		Node(graph.Node{Name: "b", NewBody: noteOnly(rec, "b")}).
		Node(graph.Node{Name: "c", NewBody: noteOnly(rec, "c")}).
		Edge(graph.Edge{From: "a", To: "b"}).
		Edge(graph.Edge{From: "a", To: "c", Guard: &graph.Guard{
			Key: "ok", Op: graph.OpEquals, Value: graph.BoolValue(true),
		}}).
		Build()

	requireBuildError(t, err, graph.RuleDeterministicEdges)
}

func TestBuildRefusesStructuralDefects(t *testing.T) {
	rec := &recorder{}
	cases := map[string]*graph.Builder{
		"no start": graph.NewBuilder().
			Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}),
		"start is not a node": graph.NewBuilder().
			Start("missing").
			Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}),
		"duplicate node": graph.NewBuilder().
			Start("a").
			Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}).
			Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}),
		"no body": graph.NewBuilder().
			Start("a").
			Node(graph.Node{Name: "a"}),
		"edge to nowhere": graph.NewBuilder().
			Start("a").
			Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}).
			Edge(graph.Edge{From: "a", To: "elsewhere"}),
		"unreachable node": graph.NewBuilder().
			Start("a").
			Node(graph.Node{Name: "a", NewBody: noteOnly(rec, "a")}).
			Node(graph.Node{Name: "orphan", NewBody: noteOnly(rec, "orphan")}),
		"no nodes": graph.NewBuilder().Start("a"),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := b.Build()
			requireBuildError(t, err, graph.RuleWellFormed)
		})
	}
}

func TestBuildReportsEveryBrokenRuleAtOnce(t *testing.T) {
	rec := &recorder{}
	_, err := graph.NewBuilder().
		Start("check").
		Key(graph.Key{Name: "findings", Kind: graph.KindInt}).
		Node(graph.Node{Name: "check", Writes: []string{"findings"}, NewBody: noteOnly(rec, "check")}).
		Node(graph.Node{Name: "fix", Writes: []string{"findings"}, NewBody: noteOnly(rec, "fix")}).
		Edge(graph.Edge{From: "check", To: "fix"}).
		Edge(graph.Edge{From: "fix", To: "check"}).
		Build()

	be := requireBuildError(t, err, graph.RuleSingleWriter, graph.RuleBoundedCycle)
	if len(be.Violations) != 2 {
		t.Fatalf("violations = %v, want two", be.Violations)
	}
}

func TestGraphExposesItsTopologyWithoutExecuting(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, fixLoopBuilder(rec, 2, func(_ context.Context, _ graph.Reader, _ graph.Writer) error {
		return nil
	}))

	if g.Start() != "check" {
		t.Errorf("Start() = %q, want check", g.Start())
	}
	if got := len(g.Nodes()); got != 3 {
		t.Errorf("Nodes() has %d entries, want 3", got)
	}
	if got := len(g.Keys()); got != 2 {
		t.Errorf("Keys() has %d entries, want 2", got)
	}
	edges := g.Edges()
	if len(edges) != 3 {
		t.Fatalf("Edges() has %d entries, want 3", len(edges))
	}
	if edges[0].Guard == nil || edges[0].Guard.Key != "findings" {
		t.Errorf("edge 0 lost its guard: %+v", edges[0])
	}
	if rec.count("check") != 0 {
		t.Error("inspecting the topology ran a node body")
	}

	// The copies handed out must not reach back into the graph.
	edges[0].Guard.Key = "tampered"
	if again := g.Edges(); again[0].Guard.Key != "findings" {
		t.Error("mutating an inspected edge changed the graph")
	}
}
