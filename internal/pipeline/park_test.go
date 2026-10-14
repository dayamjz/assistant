package pipeline

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
)

// Every stage that takes automatic fix rounds names the configuration key
// that sets its limit, and the key is the one that moves the limit the stage's
// fix loop is bounded by: a document setting only that key changes the bound
// on that stage's edge into its fixer and on no other stage's. The checks
// stage's key is fix_rounds.checks while its name is ci, so a key spelled
// from the stage's name would fail here. A stage that takes none names no key.
func TestEachStagesRoundsKeySetsTheLimitItsLoopIsBoundedBy(t *testing.T) {
	const raised = 7
	for _, stage := range Order() {
		key, ok := stage.RoundsKey()
		row, _ := stage.spec()
		if ok != (row.rounds != nil) {
			t.Fatalf("%s names key %q (%v) and takes automatic rounds: %v", stage, key, ok, row.rounds != nil)
		}
		if !ok {
			continue
		}
		layer, err := config.Parse(config.OriginPushed, documentSetting(t, key, raised))
		if err != nil {
			t.Fatalf("%s: parsing a document setting %s: %v", stage, key, err)
		}
		resolved, err := config.Resolve(config.Absent(config.OriginGlobal), layer)
		if err != nil {
			t.Fatalf("%s: resolving: %v", stage, err)
		}
		p := build(t, Options{Stages: recordingStages(newCalls()), Fixer: recordingFixer(newCalls(), nil, nil, nil),
			Rounds: resolved.Config.FixRounds, Budget: 100})
		for _, other := range Order() {
			if _, takes := other.RoundsKey(); !takes {
				continue
			}
			bound := entryBound(t, p.Graph(), other)
			if other == stage && bound != raised {
				t.Errorf("setting %s bounds %s's loop at %d, want %d", key, stage, bound, raised)
			}
			if other != stage && bound == raised {
				t.Errorf("setting %s bounds %s's loop too", key, other)
			}
		}
	}
}

// documentSetting renders a configuration document setting one dotted key.
func documentSetting(t *testing.T, key config.Key, value int) []byte {
	t.Helper()
	parts := strings.Split(string(key), ".")
	var doc any = value
	for i := len(parts) - 1; i >= 0; i-- {
		doc = map[string]any{parts[i]: doc}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encoding a document: %v", err)
	}
	return out
}

// entryBound is the round bound on the edge from a stage into its fixer.
func entryBound(t *testing.T, g *graph.Graph, stage Stage) int {
	t.Helper()
	for _, e := range g.Edges() {
		if e.From == stage.Node() && e.To == stage.FixNode() {
			return e.Rounds
		}
	}
	t.Fatalf("the graph has no edge from %s into its fixer", stage)
	return 0
}

// Every node a stage builds belongs to that stage, and a park is attributed to
// the stage whose loop its edge was counting: the round limit on the edge out
// of the stage into its fixer, convergence on the edge back from the fixer.
// A budget park, and an edge park with no edge recorded, belong to the stage
// the run stands in front of. A node no stage builds belongs to none.
func TestAParkBelongsToTheStageItsBoundWasCounting(t *testing.T) {
	for _, stage := range Order() {
		for _, node := range []string{stage.Node(), stage.HoldNode(), stage.FixNode(), stage.RequestNode()} {
			if got, ok := StageOfNode(node); !ok || got != stage {
				t.Errorf("node %q belongs to %s (%v), want %s", node, got, ok, stage)
			}
		}
	}
	for _, node := range []string{NodeCancel, "", "fix:", "no-such-node"} {
		if got, ok := StageOfNode(node); ok {
			t.Errorf("node %q belongs to %s", node, got)
		}
	}

	c := newCalls()
	stages := recordingStages(c)
	alwaysFixable(c, StageLint, &stages)
	p := build(t, Options{Stages: stages, Fixer: movingFixer(c), Rounds: config.FixRounds{Lint: 1}, Budget: 500})
	_, result := start(t, p, complete())
	park, ok := result.Park()
	if !ok || park.Status != graph.StatusRoundsExhausted || park.Edge == nil {
		t.Fatalf("the run stopped %s with park %+v (%v), want a round limit park on an edge", result.Status, park, ok)
	}
	if got, ok := ParkedStage(park); !ok || got != StageLint {
		t.Fatalf("the lint stage's round limit park belongs to %s (%v)", got, ok)
	}
	if park.Edge.From != StageLint.Node() || park.Edge.To != StageLint.FixNode() || park.Edge.Rounds != 1 {
		t.Fatalf("the round limit parked on %+v, want lint's edge into its fixer with its limit of 1", park.Edge)
	}

	budget := graph.Park{Status: graph.StatusBudgetExhausted, Node: StageTest.Node(), Budget: 4}
	if got, ok := ParkedStage(budget); !ok || got != StageTest {
		t.Fatalf("a budget park in front of the test stage belongs to %s (%v)", got, ok)
	}
}
