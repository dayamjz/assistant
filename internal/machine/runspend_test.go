package machine_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// A run's spend is the output tokens of the rows naming it: rows naming
// another run or none are left out, a row with no output count is
// unreported rather than zero, and the total sums the rows that reported.
func TestRunSpendOfTotalsTheRunsOwnOutputTokens(t *testing.T) {
	t.Parallel()
	rows := []store.AgentInvocation{
		{Run: store.Known("run-1"), OutputTokens: store.Known[int64](10)},
		{Run: store.Known("run-2"), OutputTokens: store.Known[int64](1000)},
		{Run: store.Unknown[string](), OutputTokens: store.Known[int64](1000)},
		{Run: store.Known("run-1"), OutputTokens: store.Known[int64](5)},
		{Run: store.Known("run-1"), OutputTokens: store.Unknown[int64]()},
	}
	got := machine.RunSpendOf("run-1", rows)
	if n, ok := got.Total.Get(); !ok || n != 15 || got.Reported != 2 || got.Unreported != 1 {
		t.Fatalf("run-1 spent %+v, want 15 over two reporting calls and one unreported", got)
	}
	if none := machine.RunSpendOf("run-3", rows); none.Total.IsKnown() || none.Reported != 0 || none.Unreported != 0 {
		t.Fatalf("a run with no rows spent %+v, want nothing known", none)
	}
}

// A usage park whose reading was not established is answered with a
// different remedy from one that reached its budget: a larger number does not
// help a budget on a count the agent does not report, so the action says to
// lift the budget or change agents and says how many calls reported nothing.
func TestAnUnestablishedUsageParkNamesTheUnreportedCalls(t *testing.T) {
	t.Parallel()
	reached := store.RunPark{Bound: store.ParkUsageBudget, Node: "review", UsageBudget: 50, Spent: 60, Checkpoint: 2}
	unknown := store.RunPark{Bound: store.ParkUsageBudget, Node: "review", UsageBudget: 50, Spent: 3, Unreported: 2, Checkpoint: 2}
	a, b := machine.ParkAction(reached), machine.ParkAction(unknown)
	if a == b {
		t.Fatal("the reached and the unestablished usage parks share one action")
	}
	for _, want := range []string{"2 of its agent calls reported no output token count", "to zero",
		string(config.KeyRunUsageBudgetOutputTokens)} {
		if !strings.Contains(b, want) {
			t.Errorf("the unestablished action does not say %q:\n%s", want, b)
		}
	}
	if strings.Contains(a, "to zero") {
		t.Errorf("the reached action tells the operator to lift the budget:\n%s", a)
	}
}
