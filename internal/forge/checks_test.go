package forge_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/forge"
)

// declared returns the no-CI declaration a configuration that sets no_ci
// carries. It goes through the resolved configuration rather than building a
// declaration directly, because that is the only route this package offers and
// the point of the type is that there is no other.
func declared(t *testing.T) forge.NoCIDeclaration {
	t.Helper()
	cfg := config.Defaults()
	cfg.NoCI = true
	d := forge.DeclaredNoCI(cfg)
	if !d.Declared() {
		t.Fatalf("DeclaredNoCI over a configuration that sets no_ci reported no declaration")
	}
	return d
}

func TestDeclaredNoCIRequiresTheConfigurationToSaySo(t *testing.T) {
	if d := forge.DeclaredNoCI(config.Defaults()); d.Declared() {
		t.Errorf("the default configuration produced a declaration: %v", d)
	}
	var zero forge.NoCIDeclaration
	if zero.Declared() {
		t.Errorf("the zero NoCIDeclaration reported a declaration")
	}
	if got := declared(t).Key(); got != config.KeyNoCI {
		t.Errorf("declaration names key %q, want %q", got, config.KeyNoCI)
	}
}

func TestEmptyCheckListIsNotGreen(t *testing.T) {
	got := forge.ChecksReport{HeadCommit: "c0ffee"}.Evaluate(forge.NoCIDeclaration{})
	if got.Verdict != forge.VerdictNoChecks {
		t.Errorf("verdict over no checks is %v, want no-checks", got.Verdict)
	}
	if got.Green() {
		t.Errorf("an empty check list was reported green: %v", got)
	}
	if got.Declaration.Declared() {
		t.Errorf("a result with no declaration carried one: %v", got.Declaration)
	}
}

func TestEmptyCheckListIsGreenOnlyOnTheDeclarationAndNamesIt(t *testing.T) {
	decl := declared(t)
	got := forge.ChecksReport{HeadCommit: "c0ffee"}.Evaluate(decl)
	if got.Verdict != forge.VerdictPassed {
		t.Fatalf("verdict over no checks with the declaration is %v, want passed", got.Verdict)
	}
	if !got.Green() {
		t.Fatalf("declared no-CI was not reported green: %v", got)
	}
	if !got.Declaration.Declared() {
		t.Fatalf("a pass resting on the declaration did not carry it: %+v", got)
	}
	if got.Declaration.Key() != config.KeyNoCI {
		t.Errorf("the carried declaration names %q, want %q", got.Declaration.Key(), config.KeyNoCI)
	}
	if line := got.String(); !strings.Contains(line, string(config.KeyNoCI)) {
		t.Errorf("the recorded line does not name the evidence: %q", line)
	}
}

func TestDeclarationDecidesNothingOnceACheckExists(t *testing.T) {
	report := forge.ChecksReport{
		HeadCommit: "c0ffee",
		Runs:       []forge.CheckRun{{Name: "build", State: forge.CheckStateFailed}},
	}
	got := report.Evaluate(declared(t))
	if got.Verdict != forge.VerdictFailed {
		t.Errorf("a failing check under a no-CI declaration is %v, want failed", got.Verdict)
	}
	if got.Declaration.Declared() {
		t.Errorf("a verdict that does not rest on the declaration carried it: %v", got.Declaration)
	}
}

func TestCheckStateClassification(t *testing.T) {
	cases := []struct {
		state   forge.CheckState
		settled bool
		passes  bool
	}{
		{forge.CheckStateUnrecognized, false, false},
		{forge.CheckStatePending, false, false},
		{forge.CheckStateSucceeded, true, true},
		{forge.CheckStateFailed, true, false},
		{forge.CheckStateCancelled, true, false},
		{forge.CheckStateNeutral, true, true},
		{forge.CheckStateSkipped, true, true},
	}
	for _, c := range cases {
		if got := c.state.Settled(); got != c.settled {
			t.Errorf("%v.Settled() = %v, want %v", c.state, got, c.settled)
		}
		if got := c.state.Passes(); got != c.passes {
			t.Errorf("%v.Passes() = %v, want %v", c.state, got, c.passes)
		}
	}
	var zero forge.CheckState
	if zero != forge.CheckStateUnrecognized {
		t.Errorf("the zero CheckState is %v, want unrecognized", zero)
	}
}

func TestEvaluateVerdicts(t *testing.T) {
	run := func(name string, s forge.CheckState) forge.CheckRun {
		return forge.CheckRun{Name: name, State: s}
	}
	cases := []struct {
		name string
		runs []forge.CheckRun
		want forge.Verdict
	}{
		{
			name: "every check settled and passing",
			runs: []forge.CheckRun{run("a", forge.CheckStateSucceeded), run("b", forge.CheckStateSkipped)},
			want: forge.VerdictPassed,
		},
		{
			name: "one still running",
			runs: []forge.CheckRun{run("a", forge.CheckStateSucceeded), run("b", forge.CheckStatePending)},
			want: forge.VerdictRunning,
		},
		{
			name: "a cancelled check is terminal, so the run does not wait on it",
			runs: []forge.CheckRun{run("a", forge.CheckStateSucceeded), run("b", forge.CheckStateCancelled)},
			want: forge.VerdictFailed,
		},
		{
			name: "every check cancelled",
			runs: []forge.CheckRun{run("a", forge.CheckStateCancelled)},
			want: forge.VerdictFailed,
		},
		{
			name: "an unrecognized state keeps the caller waiting",
			runs: []forge.CheckRun{run("a", forge.CheckStateSucceeded), run("b", forge.CheckStateUnrecognized)},
			want: forge.VerdictRunning,
		},
		{
			name: "a settled failure decides while others still run",
			runs: []forge.CheckRun{run("a", forge.CheckStatePending), run("b", forge.CheckStateFailed)},
			want: forge.VerdictFailed,
		},
		{
			name: "neutral and skipped do not stand in the way",
			runs: []forge.CheckRun{run("a", forge.CheckStateNeutral), run("b", forge.CheckStateSkipped)},
			want: forge.VerdictPassed,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := forge.ChecksReport{HeadCommit: "c0ffee", Runs: c.runs}.Evaluate(forge.NoCIDeclaration{})
			if got.Verdict != c.want {
				t.Errorf("verdict = %v, want %v (%v)", got.Verdict, c.want, got)
			}
			if got.Green() != (c.want == forge.VerdictPassed) {
				t.Errorf("Green() = %v for verdict %v", got.Green(), got.Verdict)
			}
		})
	}
}

func TestCancelledCheckIsReportedAsBlockingRatherThanWaitedOn(t *testing.T) {
	report := forge.ChecksReport{
		HeadCommit: "c0ffee",
		Runs: []forge.CheckRun{
			{Name: "build", State: forge.CheckStateCancelled},
			{Name: "lint", State: forge.CheckStateSucceeded},
		},
	}
	got := report.Evaluate(forge.NoCIDeclaration{})
	if w := got.Waiting(); len(w) != 0 {
		t.Errorf("a cancelled check was reported as waiting: %v", w)
	}
	blocking := got.Blocking()
	if len(blocking) != 1 || blocking[0].Name != "build" {
		t.Fatalf("Blocking() = %v, want the cancelled check", blocking)
	}
	if !strings.Contains(got.String(), "cancelled") {
		t.Errorf("the recorded line does not say the check was cancelled: %q", got.String())
	}
}

func TestUnrecognizedCheckIsReportedAsWaiting(t *testing.T) {
	report := forge.ChecksReport{
		HeadCommit: "c0ffee",
		Runs:       []forge.CheckRun{{Name: "build", State: forge.CheckStateUnrecognized, Reported: "MOONWALKING"}},
	}
	got := report.Evaluate(forge.NoCIDeclaration{})
	if got.Verdict != forge.VerdictRunning {
		t.Fatalf("verdict = %v, want running", got.Verdict)
	}
	waiting := got.Waiting()
	if len(waiting) != 1 || waiting[0].Name != "build" {
		t.Fatalf("Waiting() = %v, want the unrecognized check", waiting)
	}
	if len(got.Blocking()) != 0 {
		t.Errorf("an unrecognized check was reported as blocking: %v", got.Blocking())
	}
	if line := got.String(); !strings.Contains(line, "MOONWALKING") {
		t.Errorf("the recorded line does not name what was not recognized: %q", line)
	}
}

func TestZeroValuesConcludeNothing(t *testing.T) {
	var result forge.ChecksResult
	if result.Green() {
		t.Errorf("the zero ChecksResult reported green")
	}
	if result.Verdict != forge.VerdictNoChecks {
		t.Errorf("the zero Verdict is %v, want no-checks", result.Verdict)
	}
	var pr forge.PullRequest
	if pr.State != forge.PullRequestStateUnknown {
		t.Errorf("the zero PullRequestState is %v, want unknown", pr.State)
	}
	if pr.Mergeability != forge.MergeabilityUnknown {
		t.Errorf("the zero Mergeability is %v, want unknown", pr.Mergeability)
	}
}
