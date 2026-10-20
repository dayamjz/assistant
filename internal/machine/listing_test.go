package machine_test

import (
	"encoding/json"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// A listed run reads as its standing reads, through the chooser a run's own
// answer uses, for every standing a run's answer distinguishes: a run waiting
// on a decision, one being advanced, one standing still at a position, one
// with no position, one a bound parked, and one a merge ended. A run the
// listing carries no standing for, or a standing under another run's
// identifier, reads from its record alone, which for an unfinished record is
// the weaker row and never a sharpened one. Every row read here is a row of
// the table, so the vocabulary test holds it.
func TestAListedRunReadsAsItsStanding(t *testing.T) {
	t.Parallel()
	inTable := map[string]bool{}
	for _, phrase := range machine.Phrases() {
		inTable[phrase] = true
	}
	park := store.Known(store.RunPark{Bound: store.ParkConvergence, Stage: "review", Checkpoint: 4})
	cases := []struct {
		name     string
		record   store.Run
		standing machine.Standing
	}{
		{"held", store.Run{ID: "r", Status: store.RunHeld},
			machine.Standing{Record: store.RunHeld, Execution: machine.ExecutionAt(graph.StatusHalted, graph.State{})}},
		{"held with the record behind its checkpoint", store.Run{ID: "r", Status: store.RunRunning},
			machine.Standing{Record: store.RunRunning, Execution: machine.ExecutionAt(graph.StatusHalted, graph.State{})}},
		{"advancing", store.Run{ID: "r", Status: store.RunRunning},
			machine.Standing{Record: store.RunRunning, Execution: machine.ExecutionAt(graph.StatusRunning, graph.State{}), Advancing: true}},
		{"stalled", store.Run{ID: "r", Status: store.RunRunning},
			machine.Standing{Record: store.RunRunning, Execution: machine.ExecutionAt(graph.StatusRunning, graph.State{})}},
		{"unrecorded", store.Run{ID: "r", Status: store.RunPending},
			machine.Standing{Record: store.RunPending, Execution: machine.ExecutionUnrecorded()}},
		{"parked", store.Run{ID: "r", Status: store.RunTerminated, Park: park},
			machine.Standing{Record: store.RunTerminated, Park: park}},
		{"merged", store.Run{ID: "r", Status: store.RunPassed},
			machine.Standing{Record: store.RunPassed, PullRequestEnd: store.PullRequestMerged}},
	}
	for _, c := range cases {
		listing := machine.Runs{
			Runs:      []store.Run{c.record},
			Standings: []machine.ListedStanding{machine.ListedStandingOf(c.record.ID, c.standing)},
		}
		got := listing.PhraseOf(c.record)
		if want := machine.RunPhrase(c.standing); got != want {
			t.Errorf("%s: the listing reads %q and the run's own answer %q", c.name, got, want)
		}
		if !inTable[got.String()] {
			t.Errorf("%s: %q is not a row of the table", c.name, got)
		}
	}
	decision := machine.RunPhrase(cases[0].standing)
	if decision.Outcome != "This change is waiting on a decision from you." {
		t.Errorf("a held run reads %q, want the decision row", decision)
	}

	held := store.Run{ID: "r", Status: store.RunHeld}
	weaker := machine.RecordPhrase(held)
	if weaker == decision {
		t.Fatalf("the record row and the decision row are one sentence, so the fallback cannot be told apart: %q", weaker)
	}
	if got := (machine.Runs{Runs: []store.Run{held}}).PhraseOf(held); got != weaker {
		t.Errorf("a run with no standing reads %q, want its record's %q", got, weaker)
	}
	elsewhere := machine.Runs{
		Runs:      []store.Run{held},
		Standings: []machine.ListedStanding{machine.ListedStandingOf("another", cases[0].standing)},
	}
	if got := elsewhere.PhraseOf(held); got != weaker {
		t.Errorf("a run read through another run's standing reads %q, want its record's %q", got, weaker)
	}
}

// A listed run carries the next action its own answer carries for the same
// standing, for every standing a run's answer distinguishes, and the action
// survives the wire. PRD section 9 requires an action with every outcome, so
// a listing that reported an outcome without one would leave a driving agent
// reading "executing" with no instruction to attach and carry the run on.
// The stalled and advancing standings carry different actions, so the check
// cannot pass by every standing collapsing onto one sentence.
func TestAListedRunCarriesTheNextActionItsOwnAnswerCarries(t *testing.T) {
	t.Parallel()
	park := store.Known(store.RunPark{Bound: store.ParkConvergence, Stage: "review", Checkpoint: 4})
	standings := map[string]machine.Standing{
		"held":       {Record: store.RunHeld, Execution: machine.ExecutionAt(graph.StatusHalted, graph.State{})},
		"advancing":  {Record: store.RunRunning, Execution: machine.ExecutionAt(graph.StatusRunning, graph.State{}), Advancing: true},
		"stalled":    {Record: store.RunRunning, Execution: machine.ExecutionAt(graph.StatusRunning, graph.State{})},
		"unrecorded": {Record: store.RunPending, Execution: machine.ExecutionUnrecorded()},
		"parked":     {Record: store.RunTerminated, Park: park},
		"merged":     {Record: store.RunPassed, PullRequestEnd: store.PullRequestMerged},
	}
	for name, standing := range standings {
		want := machine.Run{}.Decide(standing).NextAction()
		if want == "" {
			t.Fatalf("%s: the run's own answer carries no next action, so this case checks nothing", name)
		}
		listed := machine.ListedStandingOf("r", standing)
		if got := listed.NextAction(); got != want {
			t.Errorf("%s: the listing carries %q and the run's own answer %q", name, got, want)
		}
		encoded, err := json.Marshal(machine.Runs{Runs: []store.Run{{ID: "r"}}, Standings: []machine.ListedStanding{listed}})
		if err != nil {
			t.Fatalf("%s: encoding the listing: %v", name, err)
		}
		var raw struct {
			Standings []map[string]any `json:"standings"`
		}
		if err := json.Unmarshal(encoded, &raw); err != nil || len(raw.Standings) != 1 {
			t.Fatalf("%s: the listing does not decode to one standing (%v):\n%s", name, err, encoded)
		}
		if got := raw.Standings[0]["next_action"]; got != want {
			t.Errorf("%s: the wire carries next_action %v, want %q:\n%s", name, got, want, encoded)
		}
		var decoded machine.Runs
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("%s: decoding the listing: %v", name, err)
		}
		if got := decoded.Standings[0]; got.NextAction() != want || got.Outcome != listed.Outcome {
			t.Errorf("%s: the listing decodes to %+v (action %q), want %+v (action %q)",
				name, got, got.NextAction(), listed, want)
		}
	}
	if machine.ListedStandingOf("r", standings["stalled"]).NextAction() ==
		machine.ListedStandingOf("r", standings["advancing"]).NextAction() {
		t.Fatal("a stalled run and an advancing one carry the same next action, so this test cannot tell them apart")
	}
}
