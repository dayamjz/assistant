package machine_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// usageReadAt is the instant every usage test measures its window back from.
var usageReadAt = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// call is one invocation row started ago before usageReadAt.
func call(agent, model, purpose string, ago time.Duration) store.AgentInvocation {
	return store.AgentInvocation{Agent: agent, Model: model, Purpose: purpose, SessionUse: "none",
		Started: usageReadAt.Add(-ago), Duration: time.Second}
}

// window parses a window a test names, failing the test on a refusal.
func window(t *testing.T, text string) machine.UsageWindow {
	t.Helper()
	w, err := machine.ParseUsageWindow(text)
	if err != nil {
		t.Fatalf("ParseUsageWindow(%q): %v", text, err)
	}
	return w
}

// A count the agent did not report stays unreported in every total it would
// have been part of, and is never summed as a zero. A group where no call
// reported the count has an unknown total that encodes as null; a group where
// some did sums only those and says how many it leaves out. The positive
// control is a reported zero, which is a known zero and is counted as
// reported, so the unknown above is the absence of a report and not the
// absence of tokens.
func TestAnUnreportedCountIsShownAsUnreportedAndNeverSummedAsZero(t *testing.T) {
	silent := call("claude", "m", "review", time.Hour)
	reported := call("claude", "m", "fix", time.Hour)
	reported.InputTokens = store.Known[int64](700)
	reported.OutputTokens = store.Known[int64](0)
	mixed := call("claude", "m", "fix", time.Minute)

	usage := machine.UsageOf(window(t, "1d"), usageReadAt, []store.AgentInvocation{silent, reported, mixed})

	review, fix := usage.ByAgent[0], usage.ByAgent[1]
	if review.Purpose != "review" || fix.Purpose != "fix" {
		t.Fatalf("the groups are %+v", usage.ByAgent)
	}
	if review.InputTokens.Total.IsKnown() || review.InputTokens.Reported != 0 || review.InputTokens.Unreported != 1 {
		t.Errorf("a group no call reported input for totals %+v, want unknown with one call unreported", review.InputTokens)
	}
	if n, ok := fix.InputTokens.Total.Get(); !ok || n != 700 || fix.InputTokens.Reported != 1 || fix.InputTokens.Unreported != 1 {
		t.Errorf("a group where one of two calls reported input totals (%d, %v) %+v, want 700 over one call with one unreported",
			n, ok, fix.InputTokens)
	}
	if n, ok := fix.OutputTokens.Total.Get(); !ok || n != 0 || fix.OutputTokens.Reported != 1 {
		t.Errorf("a reported zero totals (%d, %v) %+v, want a known zero over one call", n, ok, fix.OutputTokens)
	}
	if n, ok := usage.Total.InputTokens.Total.Get(); !ok || n != 700 || usage.Total.InputTokens.Unreported != 2 {
		t.Errorf("the overall input total is (%d, %v) %+v, want 700 with two calls unreported", n, ok, usage.Total.InputTokens)
	}
	if usage.Total.CacheReadTokens.Total.IsKnown() || usage.Total.CacheReadTokens.Unreported != 3 {
		t.Errorf("a count no call reported totals %+v overall, want unknown with every call unreported", usage.Total.CacheReadTokens)
	}

	encoded, err := json.Marshal(review.InputTokens)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if got := string(encoded); !strings.Contains(got, `"total":null`) {
		t.Errorf("an unreported total encodes as %s, want a null total", got)
	}
	encoded, err = json.Marshal(fix.OutputTokens)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if got := string(encoded); !strings.Contains(got, `"total":0`) {
		t.Errorf("a reported zero encodes as %s, want a zero total", got)
	}
}

// The calls are totalled by agent, model and purpose, and by run and stage,
// each group in the order its first call was recorded: calls, failures by
// category, duration and the token counts. A row naming no run or stage,
// which is every row written before the record kept them, is grouped under an
// unknown run and stage rather than folded into a named one, and a call
// started before the window is in no total. The grand total is the sum of
// either grouping, which is the control that no call was dropped or counted
// twice.
func TestUsageIsTotalledByAgentModelAndPurposeAndByRunAndStage(t *testing.T) {
	review := call("claude", "m1", "review", time.Hour)
	review.Run, review.Stage = store.Known("run-a"), store.Known("review")
	review.InputTokens = store.Known[int64](100)
	fixA := call("claude", "m1", "fix", 50*time.Minute)
	fixA.Run, fixA.Stage = store.Known("run-a"), store.Known("review")
	fixA.Failure, fixA.Duration = "timeout", 3*time.Second
	fixA.InputTokens = store.Known[int64](10)
	pushReview := call("claude", "m1", "review", 40*time.Minute)
	pushReview.Run, pushReview.Stage = store.Known("run-a"), store.Known("push")
	other := call("cursor", "m2", "review", 30*time.Minute)
	other.Run, other.Stage = store.Known("run-b"), store.Known("review")
	other.Failure = "process"
	old := call("claude", "m1", "review", 20*time.Minute)
	stale := call("claude", "m1", "review", 48*time.Hour)
	stale.Run, stale.Stage = store.Known("run-z"), store.Known("review")

	usage := machine.UsageOf(window(t, "24h"), usageReadAt, []store.AgentInvocation{review, fixA, pushReview, other, old, stale})

	if usage.Window != "24h" || !usage.Since.Equal(usageReadAt.Add(-24*time.Hour)) || !usage.ReadAt.Equal(usageReadAt) {
		t.Errorf("the window reads %q from %v at %v", usage.Window, usage.Since, usage.ReadAt)
	}
	if usage.Total.Calls != 5 || usage.Total.Failures["timeout"] != 1 || usage.Total.Failures["process"] != 1 ||
		usage.Total.Duration != 7*time.Second {
		t.Errorf("the total is %+v, want five calls, one failure of each category and seven seconds", usage.Total)
	}

	type agentGroup struct {
		agent, model, purpose string
		calls                 int
	}
	var agents []agentGroup
	for _, g := range usage.ByAgent {
		agents = append(agents, agentGroup{g.Agent, g.Model, g.Purpose, g.Calls})
	}
	wantAgents := []agentGroup{{"claude", "m1", "review", 3}, {"claude", "m1", "fix", 1}, {"cursor", "m2", "review", 1}}
	if len(agents) != len(wantAgents) {
		t.Fatalf("grouped by agent as %+v, want %+v", agents, wantAgents)
	}
	for i := range agents {
		if agents[i] != wantAgents[i] {
			t.Errorf("agent group %d is %+v, want %+v", i, agents[i], wantAgents[i])
		}
	}
	if n, _ := usage.ByAgent[0].InputTokens.Total.Get(); n != 100 || usage.ByAgent[0].InputTokens.Unreported != 2 {
		t.Errorf("claude's review input is %+v, want 100 with two calls unreported", usage.ByAgent[0].InputTokens)
	}

	type runGroup struct {
		run, stage string
		calls      int
	}
	var runs []runGroup
	for _, g := range usage.ByRun {
		runs = append(runs, runGroup{g.Run.String(), g.Stage.String(), g.Calls})
	}
	wantRuns := []runGroup{{"run-a", "review", 2}, {"run-a", "push", 1}, {"run-b", "review", 1}, {"unknown", "unknown", 1}}
	if len(runs) != len(wantRuns) {
		t.Fatalf("grouped by run as %+v, want %+v", runs, wantRuns)
	}
	for i := range runs {
		if runs[i] != wantRuns[i] {
			t.Errorf("run group %d is %+v, want %+v", i, runs[i], wantRuns[i])
		}
	}
	if g := usage.ByRun[0]; g.Failures["timeout"] != 1 || g.Duration != 4*time.Second {
		t.Errorf("run-a's review totals %+v, want the fix round's failure and both durations", g.UsageTotals)
	}
	if last := usage.ByRun[len(usage.ByRun)-1]; last.Run.IsKnown() || last.Stage.IsKnown() {
		t.Errorf("the rows naming no run were grouped under %v / %v, want unknown for both", last.Run, last.Stage)
	}

	sumAgents, sumRuns := 0, 0
	for _, g := range usage.ByAgent {
		sumAgents += g.Calls
	}
	for _, g := range usage.ByRun {
		sumRuns += g.Calls
	}
	if sumAgents != usage.Total.Calls || sumRuns != usage.Total.Calls {
		t.Errorf("the groupings sum to %d and %d calls, want the total's %d", sumAgents, sumRuns, usage.Total.Calls)
	}
}

// The view is resolved from every row of the history in the window and
// nothing else: a row appended to the history changes the next read, and the
// view of a history is never the view of its newest row. The control is that
// the newest row alone would answer differently, so the assertion is not one
// a reading of the last line could pass. It is P8 for this read: the
// invocation history is appended, and what it totals is read from all of it.
func TestTheUsageViewIsReadFromTheWholeHistoryNotItsNewestRow(t *testing.T) {
	principles.Cite(t, principles.P8)
	first := call("claude", "m", "review", 2*time.Hour)
	first.InputTokens = store.Known[int64](40)
	second := call("claude", "m", "review", time.Hour)
	second.InputTokens = store.Known[int64](2)
	w := window(t, "1d")

	whole := machine.UsageOf(w, usageReadAt, []store.AgentInvocation{first, second})
	newest := machine.UsageOf(w, usageReadAt, []store.AgentInvocation{second})
	if n, _ := whole.Total.InputTokens.Total.Get(); n != 42 || whole.Total.Calls != 2 {
		t.Errorf("the whole history totals %d input over %d calls, want 42 over 2", n, whole.Total.Calls)
	}
	if whole.Total.Calls == newest.Total.Calls {
		t.Fatal("the newest row alone answers the same as the history, so this test discriminates nothing")
	}

	third := call("claude", "m", "review", time.Minute)
	again := machine.UsageOf(w, usageReadAt, []store.AgentInvocation{first, second, third})
	if again.Total.Calls != 3 || again.Total.InputTokens.Unreported != 1 {
		t.Errorf("a row appended to the history did not change the next read: %+v", again.Total)
	}
}

// A window is a positive span written as whole days or as anything Go reads
// as a duration, empty is the documented default, and anything else is
// refused with ErrUsageWindow rather than read as the default.
func TestAUsageWindowIsAPositiveSpanAndAnythingElseIsRefused(t *testing.T) {
	for text, want := range map[string]time.Duration{
		"":      7 * 24 * time.Hour,
		"7d":    7 * 24 * time.Hour,
		"1d":    24 * time.Hour,
		"24h":   24 * time.Hour,
		"90m":   90 * time.Minute,
		"1h30m": 90 * time.Minute,
	} {
		w, err := machine.ParseUsageWindow(text)
		if err != nil {
			t.Errorf("ParseUsageWindow(%q) refused: %v", text, err)
			continue
		}
		if w.Span() != want {
			t.Errorf("ParseUsageWindow(%q) spans %v, want %v", text, w.Span(), want)
		}
	}
	if w, _ := machine.ParseUsageWindow(""); w.String() != machine.DefaultUsageWindow {
		t.Errorf("the default window reads %q, want %q", w.String(), machine.DefaultUsageWindow)
	}
	for _, text := range []string{"0d", "0s", "-1h", "7", "d", "a week", "7days", "1.5d", "99999999999999999999d", "106752d"} {
		if _, err := machine.ParseUsageWindow(text); !errors.Is(err, machine.ErrUsageWindow) {
			t.Errorf("ParseUsageWindow(%q) = %v, want ErrUsageWindow", text, err)
		}
	}
}

// An effort the launch did not name is its own total, and so is an effort a
// row written before the column cannot say. Neither is folded into a named
// effort, and two named efforts stay apart.
func TestUsageKeepsAnUnnamedEffortApartFromANamedOne(t *testing.T) {
	named := call("claude", "m", "review", time.Hour)
	named.Role, named.Provider, named.Type, named.Version, named.Effort = store.Known("review"), store.Known("anthropic"), store.Known("opus"), store.Known("5.5"), store.Known("high")
	unnamed := call("claude", "m", "review", time.Hour)
	unnamed.Role, unnamed.Provider, unnamed.Type, unnamed.Version, unnamed.Effort = store.Known("review"), store.Known(""), store.Known(""), store.Known(""), store.Known("")
	unknown := call("claude", "m", "review", time.Hour)
	low := named
	low.Effort = store.Known("low")

	usage := machine.UsageOf(window(t, "24h"), usageReadAt, []store.AgentInvocation{named, unnamed, unknown, low})
	if len(usage.ByAgent) != 4 {
		t.Fatalf("grouped as %+v, want four groups", usage.ByAgent)
	}
	if usage.ByAgent[0].Effort.String() != "high" || usage.ByAgent[1].Effort.String() != "" ||
		usage.ByAgent[2].Effort.String() != "unknown" || usage.ByAgent[3].Effort.String() != "low" {
		t.Fatalf("efforts grouped as %s, %s, %s, %s", usage.ByAgent[0].Effort, usage.ByAgent[1].Effort, usage.ByAgent[2].Effort, usage.ByAgent[3].Effort)
	}
}
