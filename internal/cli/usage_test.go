package cli_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// TestRunsUsageReportsTheInvocationRecordThroughTheSurface drives PRD section
// 9's usage view through cli.Run against a real service, over a home whose
// agent invocation history holds a review call and a fix round serving one
// run's review stage, a call from before the record kept a run or stage, and
// a call older than the window. The rows are written straight to the store
// because which calls a run makes, and that each lands with its run and
// stage, are other tests' subjects; what is under test here is what
// runs --usage makes of the record once it exists.
//
// It establishes that the structured answer is the usage view over the
// window asked for, totalled from the record (P8): the call older than the
// window is in no total, the grouping by run and stage names the run and
// stage the two calls served and puts the old row under unknown rather than
// guessing, and an input count one call did not report is summed over the
// call that did and counted as unreported. The human rendering is the same
// answer behind the phrase that points at it (P13), and --usage alone reads
// the default window.
func TestRunsUsageReportsTheInvocationRecordThroughTheSurface(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P13)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)

	records := openStore(t, h)
	t.Cleanup(func() { _ = records.Close() })
	now := time.Now().UTC()
	for _, inv := range []store.AgentInvocation{
		{Purpose: "review", Agent: "claude", Model: "some-model", SessionUse: "none", Started: now.Add(-2 * time.Hour),
			Duration: time.Minute, InputTokens: store.Known[int64](1000), Run: store.Known("usage-run-1"), Stage: store.Known("review")},
		{Purpose: "fix", Agent: "claude", Model: "some-model", SessionUse: "opened", Started: now.Add(-time.Hour),
			Duration: 2 * time.Second, Failure: "timeout", Run: store.Known("usage-run-1"), Stage: store.Known("review")},
		{Purpose: "intent", Agent: "claude", SessionUse: "none", Started: now.Add(-30 * time.Minute), Duration: time.Second,
			InputTokens: store.Known[int64](5)},
		{Purpose: "review", Agent: "claude", Model: "some-model", SessionUse: "none", Started: now.Add(-48 * time.Hour),
			Duration: time.Minute, InputTokens: store.Known[int64](99999)},
	} {
		if _, err := records.AppendAgentInvocation(t.Context(), inv); err != nil {
			t.Fatalf("seeding the history: %v", err)
		}
	}

	got := run(t, h, subject, "--json", "runs", "--usage=1d")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant runs --usage=1d exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var usage machine.Usage
	if err := json.Unmarshal([]byte(got.stdout), &usage); err != nil {
		t.Fatalf("the answer is not the usage view: %v\n%s", err, got.stdout)
	}
	if usage.Window != "1d" || usage.Total.Calls != 3 {
		t.Fatalf("the read covers %q with %d calls, want 1d with the three inside it:\n%s", usage.Window, usage.Total.Calls, got.stdout)
	}
	if n, ok := usage.Total.InputTokens.Total.Get(); !ok || n != 1005 || usage.Total.InputTokens.Unreported != 1 {
		t.Errorf("the input total is %+v, want 1005 with the fix round unreported", usage.Total.InputTokens)
	}
	if usage.Total.Failures["timeout"] != 1 {
		t.Errorf("the failures are %+v, want the fix round's timeout", usage.Total.Failures)
	}
	if len(usage.ByRun) != 2 {
		t.Fatalf("grouped by run as %+v, want the served run and the unknown", usage.ByRun)
	}
	served, unknown := usage.ByRun[0], usage.ByRun[1]
	if r, _ := served.Run.Get(); r != "usage-run-1" || served.Stage.String() != "review" || served.Calls != 2 {
		t.Errorf("the served group is run %v stage %v with %d calls", served.Run, served.Stage, served.Calls)
	}
	if unknown.Run.IsKnown() || unknown.Stage.IsKnown() || unknown.Calls != 1 {
		t.Errorf("the old row is grouped under run %v stage %v with %d calls, want unknown for both", unknown.Run, unknown.Stage, unknown.Calls)
	}
	if !strings.Contains(got.stdout, `"input_tokens":{"total":null`) {
		t.Errorf("no group carries an unreported input total as null:\n%s", got.stdout)
	}
	if usage.Ceiling.Standing != machine.CeilingOff || len(usage.Ceiling.Counts) != 0 {
		t.Errorf("a home that set no ceiling answered %+v, want it off", usage.Ceiling)
	}

	human := run(t, h, subject, "runs", "--usage")
	if human.code != machine.ExitOK {
		t.Fatalf("assistant runs --usage exited %s:\n%s%s", human.code, human.stdout, human.stderr)
	}
	if !strings.HasPrefix(human.stdout, machine.PhraseUsageFollows.String()+"\n") {
		t.Errorf("the usage view does not open with the phrase that points at it:\n%s", human.stdout)
	}
	if !strings.Contains(human.stdout, "Window     "+machine.DefaultUsageWindow+":") ||
		!strings.Contains(human.stdout, "run usage-run-1  stage review") || !strings.Contains(human.stdout, "run unknown  stage unknown") {
		t.Errorf("the usage view over the default window does not carry its window and groups:\n%s", human.stdout)
	}
	if !strings.Contains(human.stdout, "Ceiling    "+machine.CeilingPhrase(machine.CeilingOff).String()+"\n") {
		t.Errorf("the usage view does not say no ceiling is set:\n%s", human.stdout)
	}
}

// TestRunsUsageWeighsTheOperatorsCeilingAgainstTheSameTotal drives the usage
// view over a home whose operator set an input-token ceiling, through
// cli.Run against a real service. The ceiling's window and the window asked
// for are the same day, so the view's input total and the total the ceiling
// was weighed against are the same rows, and the answer says the two are one
// figure: the ceiling reached at the record's total, with the call that
// reported no input count beside it not read as none. The human rendering
// carries internal/machine's row for a reached ceiling and the exact figures
// (P13), and the call outside the ceiling's window is in neither total (P8).
func TestRunsUsageWeighsTheOperatorsCeilingAgainstTheSameTotal(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	h := newHome(t)
	subject := newSubject(t)
	if err := os.WriteFile(h.ConfigFile(), []byte("usage_ceiling:\n  window: 24h\n  input_tokens: 1000\n"), 0o600); err != nil {
		t.Fatalf("writing the operator's configuration: %v", err)
	}
	serve(t, h)

	records := openStore(t, h)
	t.Cleanup(func() { _ = records.Close() })
	now := time.Now().UTC()
	for _, inv := range []store.AgentInvocation{
		{Purpose: "review", Agent: "claude", SessionUse: "none", Started: now.Add(-2 * time.Hour), InputTokens: store.Known[int64](1000)},
		{Purpose: "fix", Agent: "claude", SessionUse: "opened", Started: now.Add(-time.Hour), Failure: "timeout"},
		{Purpose: "review", Agent: "claude", SessionUse: "none", Started: now.Add(-48 * time.Hour), InputTokens: store.Known[int64](99999)},
	} {
		if _, err := records.AppendAgentInvocation(t.Context(), inv); err != nil {
			t.Fatalf("seeding the history: %v", err)
		}
	}

	got := run(t, h, subject, "--json", "runs", "--usage=1d")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant runs --usage=1d exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var usage machine.Usage
	if err := json.Unmarshal([]byte(got.stdout), &usage); err != nil {
		t.Fatalf("the answer is not the usage view: %v\n%s", err, got.stdout)
	}
	ceiling := usage.Ceiling
	if ceiling.Standing != machine.CeilingReached || ceiling.Window != "1d" || len(ceiling.Counts) != 1 {
		t.Fatalf("the ceiling answered %+v, want the input ceiling reached over a day", ceiling)
	}
	if ceiling.Counts[0].Recorded != usage.Total.InputTokens {
		t.Errorf("the ceiling weighed %+v, the view totals %+v", ceiling.Counts[0].Recorded, usage.Total.InputTokens)
	}
	if n, ok := ceiling.Counts[0].Recorded.Total.Get(); !ok || n != 1000 || ceiling.Counts[0].Recorded.Unreported != 1 {
		t.Errorf("the ceiling weighed %+v, want 1000 with the fix round unreported", ceiling.Counts[0].Recorded)
	}

	human := run(t, h, subject, "runs", "--usage=1d")
	if human.code != machine.ExitOK {
		t.Fatalf("assistant runs --usage=1d exited %s:\n%s%s", human.code, human.stdout, human.stderr)
	}
	for _, want := range []string{
		"Ceiling    " + machine.CeilingPhrase(machine.CeilingReached).String() + "\n",
		"input_tokens at least 1000 (1 of 2 calls unreported) of 1000 (reached)",
	} {
		if !strings.Contains(human.stdout, want) {
			t.Errorf("the usage view does not carry %q:\n%s", want, human.stdout)
		}
	}
}

// A window runs --usage cannot read is incorrect usage, refused before the
// service is asked, and so is one written as a separate word, a run named
// beside the flag, and --limit with it: each would otherwise be read as
// something it is not. The control is that --usage with a window it can read
// is answered.
func TestRunsUsageRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	for _, args := range [][]string{
		{"runs", "--usage=0d"},
		{"runs", "--usage=a-week"},
		{"runs", "--usage", "7d"},
		{"runs", "--usage", "some-run"},
		{"runs", "--usage", "--limit", "3"},
	} {
		got := run(t, h, subject, args...)
		if got.code != machine.ExitUsage {
			t.Errorf("assistant %s exited %s, want incorrect usage:\n%s%s", strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
	}
	if got := run(t, h, subject, "--json", "runs", "--usage=90m"); got.code != machine.ExitOK {
		t.Fatalf("assistant runs --usage=90m exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
}
