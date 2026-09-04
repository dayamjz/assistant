package pipeline

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
)

// theNineStages is the order PRD section 5 fixes, written out here rather than
// read from the package, so this test can fail for the defect it exists to
// catch. A test that asked the package what the order is would agree with any
// order the package happened to hold.
var theNineStages = []string{
	"intent", "rebase", "review", "test", "document", "lint", "push", "pr", "ci",
}

// chain walks the built graph from its start node, following the unconditional
// edge out of every stage node, and returns the stage nodes it passed through.
// It reads the topology rather than any list, so it fails if the wiring says
// something the stage table does not.
func chain(t *testing.T, g *graph.Graph) []string {
	t.Helper()
	edges := g.Edges()
	var walked []string
	at := g.Start()
	for range len(edges) + 1 {
		if at == NodeDone {
			return walked
		}
		node, ok := g.Node(at)
		if !ok {
			t.Fatalf("walk reached %q, which is not a node", at)
		}
		if node.Halt == nil {
			walked = append(walked, at)
		}
		next := ""
		for _, e := range edges {
			if e.From == at && e.Guard == nil {
				next = e.To
			}
		}
		if next == "" {
			t.Fatalf("node %q has no unconditional edge", at)
		}
		at = next
	}
	t.Fatalf("walking the pipeline from %q did not reach %q", g.Start(), NodeDone)
	return nil
}

func TestTheNineStagesRunInTheFixedOrder(t *testing.T) {
	p := build(t, Options{Stages: ConstantStages(passingSummary), Budget: 100})
	walked := chain(t, p.Graph())
	if len(walked) != len(theNineStages) {
		t.Fatalf("walked %v, want %v", walked, theNineStages)
	}
	for i, name := range theNineStages {
		if walked[i] != name {
			t.Errorf("stage %d is %q, want %q", i, walked[i], name)
		}
	}
}

func TestEveryStageMustHaveAnImplementation(t *testing.T) {
	for _, stage := range Order() {
		t.Run(stage.String(), func(t *testing.T) {
			stages := ConstantStages(passingSummary)
			set(&stages, stage, Implementation{})
			_, err := New(Options{Stages: stages, Budget: 100})
			if !errors.Is(err, ErrMissingStage) {
				t.Fatalf("New with no %s implementation: %v, want ErrMissingStage", stage, err)
			}
			if !strings.Contains(err.Error(), stage.String()) {
				t.Errorf("refusal %q does not name the stage %q", err, stage)
			}
		})
	}
}

// TestConfigurationCannotChangeWhichStagesRun is P2 as a check on the built
// topology: a repository's fix round limits decide whether a stage has a fixer
// and nothing else. The chain of stages, and the state schema, are the same
// under every configuration.
func TestConfigurationCannotChangeWhichStagesRun(t *testing.T) {
	baseline := build(t, Options{Stages: ConstantStages(passingSummary), Budget: 100})
	want := chain(t, baseline.Graph())
	wantKeys := Keys()

	for _, limits := range []config.FixRounds{
		{},
		rounds(1),
		rounds(config.MaxFixRounds),
		{Rebase: 0, Review: 1, Test: 0, Lint: 7, Checks: 0},
	} {
		p := build(t, Options{
			Stages: ConstantStages(passingSummary),
			Fixer:  recordingFixer(newCalls(), nil, nil, nil),
			Rounds: limits,
			Budget: 100,
		})
		got := chain(t, p.Graph())
		if len(got) != len(want) {
			t.Fatalf("rounds %+v walked %v, want %v", limits, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("rounds %+v walked %v, want %v", limits, got, want)
			}
		}
		gotKeys := p.Graph().Keys()
		if len(gotKeys) != len(wantKeys) {
			t.Fatalf("rounds %+v built a graph declaring %d keys, want the schema's %d", limits, len(gotKeys), len(wantKeys))
		}
		for i := range wantKeys {
			if gotKeys[i].Name != string(wantKeys[i]) {
				t.Fatalf("rounds %+v built a graph declaring %q where the schema declares %q",
					limits, gotKeys[i].Name, wantKeys[i])
			}
		}
	}
}

// TestOnlyTheFiveConfiguredStagesTakeFixRounds checks the other half of P2:
// the stages a repository may give fix rounds to are exactly the five
// config.FixRounds declares, so no configuration can give one to the rest.
func TestOnlyTheFiveConfiguredStagesTakeFixRounds(t *testing.T) {
	withRounds := map[Stage]bool{StageRebase: true, StageReview: true, StageTest: true, StageLint: true, StageCI: true}
	p := build(t, Options{
		Stages: ConstantStages(passingSummary),
		Fixer:  recordingFixer(newCalls(), nil, nil, nil),
		Rounds: rounds(config.MaxFixRounds),
		Budget: 100,
	})
	for _, stage := range Order() {
		_, ok := p.Graph().Node(stage.FixNode())
		if ok != withRounds[stage] {
			t.Errorf("%s has a fixer node: %v, want %v", stage, ok, withRounds[stage])
		}
	}
}

func TestEveryStageHasAHaltPointItStopsBefore(t *testing.T) {
	p := build(t, Options{Stages: ConstantStages(passingSummary), Budget: 100})
	for _, stage := range Order() {
		node, ok := p.Graph().Node(stage.HoldNode())
		if !ok {
			t.Fatalf("%s has no hold node", stage)
		}
		if node.Halt == nil {
			t.Fatalf("%s hold node is not a halt point", stage)
		}
		if node.Halt.Into != string(stage.AnswerKey()) {
			t.Errorf("%s halts into %q, want %q", stage, node.Halt.Into, stage.AnswerKey())
		}
	}
}

// TestEveryCycleEdgeCarriesTheStagesRoundLimit reads the bounds off the built
// topology. It is what says the fix loop cannot be built unbounded, since the
// graph refuses a back edge with no bound and this asserts the number is the
// stage's own limit rather than some other number that would also build.
func TestEveryCycleEdgeCarriesTheStagesRoundLimit(t *testing.T) {
	limits := config.FixRounds{Rebase: 2, Review: 1, Test: 4, Lint: 3, Checks: 5}
	p := build(t, Options{
		Stages: ConstantStages(passingSummary),
		Fixer:  recordingFixer(newCalls(), nil, nil, nil),
		Rounds: limits,
		Budget: 100,
	})
	want := map[Stage]int{StageRebase: 2, StageReview: 1, StageTest: 4, StageLint: 3, StageCI: 5}
	seen := map[Stage]int{}
	for i, e := range p.Graph().Edges() {
		for stage, limit := range want {
			if e.To != stage.FixNode() && e.From != stage.FixNode() {
				continue
			}
			if e.Rounds != limit {
				t.Errorf("edge %q -> %q bounds %d rounds, want %d", e.From, e.To, e.Rounds, limit)
			}
			seen[stage]++
			if e.From == stage.FixNode() && !p.Graph().IsBackEdge(i) {
				t.Errorf("edge %q -> %q is not the back edge the loop closes with", e.From, e.To)
			}
		}
	}
	for stage, limit := range want {
		if seen[stage] != 2 {
			t.Errorf("%s (limit %d) has %d bounded cycle edges, want 2", stage, limit, seen[stage])
		}
	}
}
