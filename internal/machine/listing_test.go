package machine_test

import (
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
