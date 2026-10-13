package graph_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

// detailGraph is one halt point whose detail declaration a case varies, over
// the keys a detail can be pointed at.
func detailGraph(rec *recorder, halt graph.Halt, extra ...graph.Node) *graph.Builder {
	b := graph.NewBuilder().
		Start("a").
		Key(graph.Key{Name: "answer", Kind: graph.KindText}).
		Key(graph.Key{Name: "detail", Kind: graph.KindText}).
		Key(graph.Key{Name: "count", Kind: graph.KindInt}).
		Node(graph.Node{Name: "a", Halt: &halt, NewBody: noteOnly(rec, "a")})
	for _, n := range extra {
		b.Node(n)
		b.Edge(graph.Edge{From: "a", To: n.Name})
	}
	return b
}

// A detail key is held to an answer key's rules, and the answers that take a
// detail and the key it lands in are declared together or not at all. Each is
// a construction-time rule, so each fails to build; the first case builds, and
// is what shows the refusals are about the declaration rather than about
// detail keys at all.
func TestBuildChecksAHaltPointsDetailDeclaration(t *testing.T) {
	rec := &recorder{}
	well := graph.Halt{Question: "go?", Options: []string{"yes", "fix"}, Into: "answer",
		Detail: "detail", Detailed: []string{"fix"}}
	if _, err := detailGraph(rec, well).Build(); err != nil {
		t.Fatalf("a well-formed detail declaration did not build: %v", err)
	}

	for name, tc := range map[string]struct {
		build *graph.Builder
		rule  graph.Rule
	}{
		"a detail key nothing fills": {detailGraph(rec, graph.Halt{Question: "go?", Into: "answer",
			Detail: "detail"}), graph.RuleWellFormed},
		"detailed answers with no key": {detailGraph(rec, graph.Halt{Question: "go?", Into: "answer",
			Detailed: []string{"fix"}}), graph.RuleWellFormed},
		"a detailed answer that is not an option": {detailGraph(rec, graph.Halt{Question: "go?",
			Options: []string{"yes"}, Into: "answer", Detail: "detail", Detailed: []string{"fix"}}), graph.RuleWellFormed},
		"the answer key doubling as the detail": {detailGraph(rec, graph.Halt{Question: "go?", Into: "answer",
			Detail: "answer", Detailed: []string{"fix"}}), graph.RuleWellFormed},
		"an undeclared detail key": {detailGraph(rec, graph.Halt{Question: "go?", Into: "answer",
			Detail: "ghost", Detailed: []string{"fix"}}), graph.RuleDeclaredKey},
		"a detail key that is not text": {detailGraph(rec, graph.Halt{Question: "go?", Into: "answer",
			Detail: "count", Detailed: []string{"fix"}}), graph.RuleDeclaredKey},
		"a node writing the detail key": {detailGraph(rec, well, graph.Node{Name: "b",
			Writes: []string{"detail"}, NewBody: noteOnly(rec, "b")}), graph.RuleSingleWriter},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tc.build.Build()
			requireBuildError(t, err, tc.rule)
		})
	}
}
