package machine_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// parkOf is a park record of each bound, of the shape internal/service writes.
func parkOf(bound store.ParkBound) store.RunPark {
	switch bound {
	case store.ParkRoundLimit:
		return store.RunPark{Bound: bound, Stage: "ci", Node: pipeline.StageCI.FixNode(), Limit: 3, Checkpoint: 4}
	case store.ParkConvergence:
		return store.RunPark{Bound: bound, Stage: "review", Node: pipeline.StageReview.Node(), Checkpoint: 4}
	case store.ParkUsageBudget:
		return store.RunPark{Bound: bound, Stage: "review", Node: pipeline.StageReview.Node(),
			UsageBudget: 5000, Spent: 5120, Checkpoint: 4}
	default:
		return store.RunPark{Bound: bound, Stage: "test", Node: pipeline.StageTest.Node(), Limit: 12, Checkpoint: 4}
	}
}

// A terminated record naming a park is a run that failed, whatever its
// checkpoint says, and one naming none is a cancellation unless its checkpoint
// says a bound stopped it, which is how a run parked before the park record
// existed still reads. Each bound's next action is its own, names what changes
// the outcome for that bound, and is not the cancellation's.
func TestAParkRecordDecidesTheOutcomeAndTheAction(t *testing.T) {
	principles.Cite(t, principles.P8)
	cancelled := machine.Standing{Record: store.RunTerminated}
	if got := machine.OutcomeOf(cancelled); got != machine.OutcomeCancelled {
		t.Fatalf("a terminated record with no park reads as %s", got)
	}
	legacy := machine.Standing{Record: store.RunTerminated,
		Execution: machine.ExecutionAt(graph.StatusRoundsExhausted, graph.State{})}
	if got := machine.OutcomeOf(legacy); got != machine.OutcomeFailed {
		t.Fatalf("a run parked before the park record existed reads as %s", got)
	}

	wants := map[store.ParkBound][]string{
		// The ci stage's key is fix_rounds.checks, so the key comes from the
		// stage table and not from the stage's name.
		store.ParkRoundLimit:  {"round limit", "ci stage", "3 automatic rounds", string(config.KeyFixRoundsChecks)},
		store.ParkConvergence: {"changed nothing", "review stage"},
		store.ParkStepBudget:  {"step budget of 12", "AdoptBudget", string(config.KeyRunBudget), "no command"},
		store.ParkUsageBudget: {"usage budget of 5000 output tokens", "reported 5120 output tokens", "AdoptBudget",
			string(config.KeyRunUsageBudgetOutputTokens), "no command", "spend so much. assistant runs --usage reports what each of the run's stages spent."},
	}
	actions := map[string]store.ParkBound{}
	for _, bound := range store.ParkBounds() {
		standing := machine.Standing{Record: store.RunTerminated, Park: store.Known(parkOf(bound))}
		if got := machine.OutcomeOf(standing); got != machine.OutcomeFailed {
			t.Errorf("a %s park reads as %s", bound, got)
		}
		action := machine.NextActionOf(standing)
		for _, want := range wants[bound] {
			if !strings.Contains(action, want) {
				t.Errorf("the %s action does not say %q:\n%s", bound, want, action)
			}
		}
		if action == machine.NextActionOf(cancelled) || action == machine.OutcomeFailed.NextAction() {
			t.Errorf("the %s action is not its own: %q", bound, action)
		}
		if other, seen := actions[action]; seen {
			t.Errorf("%s and %s share the action %q", bound, other, action)
		}
		actions[action] = bound
	}
	// The wrong half of that is guarded too: a round limit that parked
	// somewhere other than the stage's fixer is not a limit its key sets.
	requested := parkOf(store.ParkRoundLimit)
	requested.Node = pipeline.StageCI.RequestNode()
	if action := machine.ParkAction(requested); strings.Contains(action, string(config.KeyFixRoundsChecks)) {
		t.Errorf("a round limit off the fixer's edge names %s:\n%s", config.KeyFixRoundsChecks, action)
	}
}

// What a person reads about a parked run is its bound's row, read the same
// from a whole answer and from the record alone, and never the row a
// cancellation reads as. Each row is held to P13's rule in both directions:
// the rows are in Phrases, which the vocabulary test holds to the internal
// vocabulary's absence, and each says which bound in words.
func TestAParkedRunReadsAsItsBoundAndNotAsACancellation(t *testing.T) {
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	listed := map[string]bool{}
	for _, phrase := range machine.Phrases() {
		listed[phrase] = true
	}
	cancelled := machine.RecordPhrase(store.Run{Status: store.RunTerminated})
	words := map[store.ParkBound]string{
		store.ParkRoundLimit:  "round limit",
		store.ParkConvergence: "changed nothing",
		store.ParkStepBudget:  "step budget",
		store.ParkUsageBudget: "usage budget",
	}
	seen := map[machine.Phrase]store.ParkBound{}
	for _, bound := range store.ParkBounds() {
		record := store.Run{Status: store.RunTerminated, Park: store.Known(parkOf(bound))}
		fromRecord := machine.RecordPhrase(record)
		standing := machine.Standing{Record: record.Status, Park: record.Park}
		fromStanding := machine.RunPhrase(standing)
		fromAnswer := machine.Run{Record: record}.Decide(standing).Phrase()
		if fromRecord != fromStanding || fromRecord != fromAnswer {
			t.Errorf("a %s park reads three ways:\n%q\n%q\n%q", bound, fromRecord, fromStanding, fromAnswer)
		}
		if fromRecord == cancelled {
			t.Errorf("a %s park reads as a cancellation", bound)
		}
		if !strings.Contains(fromRecord.Outcome, words[bound]) {
			t.Errorf("the %s row %q does not say %q", bound, fromRecord.Outcome, words[bound])
		}
		// The usage budget's row says where its figures are: the run's answer
		// carries the budget and the spend, and the home-wide usage view groups
		// the calls by run. No usage view is scoped to one run, so the row may
		// not point at one.
		if bound == store.ParkUsageBudget {
			for _, want := range []string{"the run's answer gives its budget and what its agents spent", "the usage view lists its calls under the run"} {
				if !strings.Contains(fromRecord.Consequence, want) {
					t.Errorf("the usage budget row's consequence %q does not say %q", fromRecord.Consequence, want)
				}
			}
		}
		if !listed[fromRecord.String()] {
			t.Errorf("the %s row is not in Phrases, so nothing held it to the rule", bound)
		}
		vocabtest.Assert(t, fromRecord.String())
		if other, dup := seen[fromRecord]; dup {
			t.Errorf("%s and %s read alike", bound, other)
		}
		seen[fromRecord] = bound
	}
}
