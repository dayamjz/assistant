package pipeline

import (
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/principles"
)

func TestOrderIsNineStagesAndACallerCannotChangeIt(t *testing.T) {
	order := Order()
	if len(order) != len(theNineStages) {
		t.Fatalf("Order has %d stages, want %d", len(order), len(theNineStages))
	}
	for i, want := range theNineStages {
		if order[i].String() != want {
			t.Errorf("stage %d is %q, want %q", i, order[i], want)
		}
	}
	order[0] = StageCI
	if again := Order(); again[0] != StageIntent {
		t.Errorf("writing to the result of Order reordered the pipeline: it now starts at %s", again[0])
	}
}

func TestParseStageRoundTripsAndRefusesAnythingElse(t *testing.T) {
	for _, stage := range Order() {
		got, ok := ParseStage(stage.String())
		if !ok || got != stage {
			t.Errorf("ParseStage(%q) = %s, %v, want %s, true", stage, got, ok, stage)
		}
	}
	for _, name := range []string{"", "checks", "pull_request", "Intent", "fix:lint"} {
		if got, ok := ParseStage(name); ok {
			t.Errorf("ParseStage(%q) = %s, true, want false", name, got)
		}
	}
	if StageInvalid.Valid() {
		t.Error("the zero Stage reports as valid")
	}
	if got := Stage(200).String(); got != "stage(200)" {
		t.Errorf("Stage(200) renders as %q, want it to quote what it holds", got)
	}
}

// TestEveryStageHasItsOwnKeys checks the per-stage half of the schema: the
// three keys every stage has, the fix summary only the five stages that take
// fix rounds have, and no key shared between two stages.
func TestEveryStageHasItsOwnKeys(t *testing.T) {
	declared := make(map[Key]bool, len(Keys()))
	for _, key := range Keys() {
		declared[key] = true
	}
	withRounds := map[Stage]bool{StageRebase: true, StageReview: true, StageTest: true, StageLint: true, StageCI: true}
	seen := make(map[Key]Stage)
	for _, stage := range Order() {
		for _, key := range []Key{stage.ReportKey(), stage.OutcomeKey(), stage.AnswerKey()} {
			if !declared[key] {
				t.Errorf("%s names key %q, which the schema does not declare", stage, key)
			}
			if other, dup := seen[key]; dup {
				t.Errorf("key %q belongs to both %s and %s", key, other, stage)
			}
			seen[key] = stage
		}
		if got := declared[stage.FixKey()]; got != withRounds[stage] {
			t.Errorf("%s declares a fix summary key: %v, want %v", stage, got, withRounds[stage])
		}
	}
}

// TestTheGraphDeclaresExactlyTheSchema is what keeps the schema the one owner
// of the state shape: the built graph holds these keys and no others.
func TestTheGraphDeclaresExactlyTheSchema(t *testing.T) {
	principles.Cite(t, principles.P14)
	p := build(t, Options{Stages: ConstantStages(passingSummary), Budget: 100})
	got := p.Graph().Keys()
	want := Keys()
	if len(got) != len(want) {
		t.Fatalf("the graph declares %d keys, the schema declares %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != string(want[i]) {
			t.Fatalf("the graph declares %q where the schema declares %q", got[i].Name, want[i])
		}
	}
}

// TestAnOutcomeKeyMergesAndAnAnswerKeyDoesNot pins the two rows the topology
// depends on. A stage's outcome has two writers, its own node and its hold, so
// it must declare a merge rule or the graph refuses to build. An answer key
// has exactly one, its halt point, and the graph refuses a merge rule on it.
func TestAnOutcomeKeyMergesAndAnAnswerKeyDoesNot(t *testing.T) {
	p := build(t, Options{Stages: ConstantStages(passingSummary), Budget: 100})
	for _, k := range p.Graph().Keys() {
		for _, stage := range Order() {
			switch Key(k.Name) {
			case stage.OutcomeKey():
				if k.Merge == graph.MergeNone {
					t.Errorf("%s declares no merge rule, and both %s and its hold write it", k.Name, stage)
				}
			case stage.AnswerKey():
				if k.Merge != graph.MergeNone {
					t.Errorf("%s declares merge rule %s, and an answer key has one writer", k.Name, k.Merge)
				}
			}
		}
	}
}
