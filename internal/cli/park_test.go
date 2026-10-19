package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// stillBroken is the finding the stand-in rebase body below reports on every
// execution: fix-eligible, so the stage goes to its fixer rather than holding,
// and never resolved, so the stage's fix loop runs until a bound parks it.
var stillBroken = findings.Finding{
	ID:          "still-broken",
	Action:      findings.ActionFix,
	Description: "the conflict this round was meant to resolve is still there",
}

// alwaysFixable serves the product's stages with the rebase body replaced by
// one that reports stillBroken every time it runs. Rebase is the first stage
// after intent that takes automatic fix rounds, so a run reaches its fix loop
// without a hold or an agent in the way.
func alwaysFixable(deps stages.StageDeps) pipeline.Stages {
	nine := stages.All(deps)
	nine.Rebase = pipeline.Implementation{NewBody: func() pipeline.Body {
		return func(context.Context, pipeline.Input) (pipeline.Output, error) {
			return pipeline.Output{Report: findings.Report{
				Summary:  "the branch still does not apply",
				Findings: []findings.Finding{stillBroken},
			}}, nil
		}
	}}
	return nine
}

// fixerWritingSummaries is a fix path whose every round writes the summary
// summarize returns for it, counting rounds from one across the run. A
// summary that differs per round changes the run's state, so the loop runs to
// its round limit; one that does not leaves the state where the previous round
// left it, which is what the convergence bound parks.
func fixerWritingSummaries(summarize func(round int64) string) func(stages.FixDeps) pipeline.Fixer {
	var rounds atomic.Int64
	return func(stages.FixDeps) pipeline.Fixer {
		return pipeline.Fixer{NewBody: func() pipeline.FixBody {
			return func(context.Context, pipeline.FixInput) (pipeline.FixOutput, error) {
				return pipeline.FixOutput{Summary: summarize(rounds.Add(1))}, nil
			}
		}}
	}
}

// parkedSurfaces is what every surface said about one run a bound parked:
// the answer the start returned, status and runs in both renderings.
type parkedSurfaces struct {
	started    machine.Run
	status     machine.Status
	statusText string
	runs       machine.Runs
	runsText   string
}

// readSurfaces starts a run from the subject and reads it back through every
// surface the issue names, once the start has answered.
func readSurfaces(t *testing.T, h *home.Home, subject string) parkedSurfaces {
	t.Helper()
	var out parkedSurfaces
	out.started = decodeRun(t, run(t, h, subject, "--json", "--intent", "a change the gate cannot settle").stdout)
	if err := json.Unmarshal([]byte(mustOK(t, run(t, h, subject, "--json", "status")).stdout), &out.status); err != nil {
		t.Fatalf("status does not decode: %v", err)
	}
	out.statusText = mustOK(t, run(t, h, subject, "status")).stdout
	if err := json.Unmarshal([]byte(mustOK(t, run(t, h, subject, "--json", "runs")).stdout), &out.runs); err != nil {
		t.Fatalf("runs does not decode: %v", err)
	}
	out.runsText = mustOK(t, run(t, h, subject, "runs")).stdout
	t.Logf("assistant status:\n%s\nassistant runs:\n%s", out.statusText, out.runsText)
	return out
}

// assertParkNamed holds every surface to naming the bound that parked the
// run: the structured answers by the record's park and the next action, the
// renderings by the bound's own phrase, and neither by the cancellation's.
// words is what the person's phrase for this bound has to say, and action
// what the driving agent's next action has to.
func assertParkNamed(t *testing.T, got parkedSurfaces, bound store.ParkBound, words, action string) {
	t.Helper()
	cancelled := machine.RecordPhrase(store.Run{Status: store.RunTerminated}).Outcome

	// The answer the start returned.
	if got.started.Outcome != machine.OutcomeFailed {
		t.Fatalf("the parked run answered %s, want %s", got.started.Outcome, machine.OutcomeFailed)
	}
	park, ok := got.started.Record.Park.Get()
	if !ok || park.Bound != bound {
		t.Fatalf("the run's record names park %v (known=%v), want %s", park, ok, bound)
	}
	if park.Stage != pipeline.StageRebase.String() {
		t.Fatalf("the park names stage %q, want the rebase stage it was counting", park.Stage)
	}
	if !strings.Contains(got.started.NextAction(), action) {
		t.Fatalf("the next action does not say %q:\n%s", action, got.started.NextAction())
	}

	// status --json: no run is in flight, and the parked one is still there
	// to read, with the bound on its record.
	if got.status.ActiveRun != nil {
		t.Fatalf("status reports the parked run as in flight")
	}
	parked := got.status.ParkedRun
	if parked == nil {
		t.Fatal("status reports nothing about the branch's parked run")
	}
	if p, _ := parked.Record.Park.Get(); p.Bound != bound || parked.Record.ID != got.started.Record.ID {
		t.Fatalf("status reports run %s parked by %q, want run %s parked by %s",
			parked.Record.ID, p.Bound, got.started.Record.ID, bound)
	}
	if parked.Outcome != machine.OutcomeFailed || parked.NextAction() != got.started.NextAction() {
		t.Fatalf("status answers %s with %q, and the start answered %s with %q",
			parked.Outcome, parked.NextAction(), got.started.Outcome, got.started.NextAction())
	}

	// runs --json: the same record, carrying the same park.
	if len(got.runs.Runs) != 1 {
		t.Fatalf("runs lists %d runs, want the one", len(got.runs.Runs))
	}
	if p, _ := got.runs.Runs[0].Park.Get(); p != park {
		t.Fatalf("runs lists park %+v, and the run's own answer names %+v", p, park)
	}

	// The renderings: the bound's phrase, on status and on the listing, and
	// never the phrase a cancellation reads as.
	for surface, text := range map[string]string{"status": got.statusText, "runs": got.runsText} {
		if !strings.Contains(text, words) {
			t.Errorf("%s does not say %q:\n%s", surface, words, text)
		}
		if strings.Contains(text, cancelled) {
			t.Errorf("%s reads the parked run as ended without a verdict:\n%s", surface, text)
		}
	}
}

// assertFindingsKept holds both structured surfaces to carrying the findings
// the parked stage last reported, after the run's record has ended.
func assertFindingsKept(t *testing.T, got parkedSurfaces) {
	t.Helper()
	view := got.status.ParkedRun
	var fromStatus *findings.Report
	for _, stage := range view.Stages {
		if stage.Stage == pipeline.StageRebase.String() {
			fromStatus = stage.Report
		}
	}
	if !reportCarries(fromStatus, stillBroken.ID) {
		t.Fatalf("status --json does not carry the finding the parked stage stopped on: %+v", fromStatus)
	}
	if len(got.runs.Parked) != 1 {
		t.Fatalf("runs --json carries %d parked reports, want the one: %+v", len(got.runs.Parked), got.runs.Parked)
	}
	listed := got.runs.Parked[0]
	if listed.Run != got.started.Record.ID || listed.Stage != pipeline.StageRebase.String() {
		t.Fatalf("runs --json carries a report for run %s stage %s, want run %s stage %s",
			listed.Run, listed.Stage, got.started.Record.ID, pipeline.StageRebase)
	}
	if !reportCarries(listed.Report, stillBroken.ID) {
		t.Fatalf("runs --json does not carry the finding the parked stage stopped on: %+v (%s)", listed.Report, listed.Detail)
	}
	if !strings.Contains(got.statusText, stillBroken.Description) {
		t.Fatalf("status does not show the finding the parked stage stopped on:\n%s", got.statusText)
	}
}

// reportCarries reports whether a stage report holds a finding with this
// identifier.
func reportCarries(report *findings.Report, id string) bool {
	if report == nil {
		return false
	}
	for _, f := range report.Findings {
		if f.ID == id {
			return true
		}
	}
	return false
}

// TestARoundLimitParkIsNamedOnEverySurface parks a real run on a stage's fix
// round limit and reads it back through status, runs and their --json forms.
//
// PRD section 5 parks a run whose limit is spent, and the issue this answers
// is that nothing said which bound. The record names it now, written with the
// move that ended the run, and every surface reads that record rather than an
// event log's last line, which is P8; what a person reads is the bound's own
// phrase with an outcome, a consequence and a decision, which is P13. The
// findings the stage stopped on stay readable after the run has ended.
func TestARoundLimitParkIsNamedOnEverySurface(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P13)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serveFixing(t, h, alwaysFixable, fixerWritingSummaries(func(round int64) string {
		return fmt.Sprintf("round %d rewrote the conflicting hunk again", round)
	}), agentCatalog(reviewingRunner(t)))
	mustOK(t, run(t, h, subject, "init"))
	standingOnChange(t, subject, "round-limit")

	got := readSurfaces(t, h, subject)
	assertParkNamed(t, got, store.ParkRoundLimit, "round limit", string(config.KeyFixRoundsRebase))
	if park, _ := got.started.Record.Park.Get(); park.Limit != config.DefaultFixRoundsStage {
		t.Fatalf("the park records a limit of %d, and the rebase stage's limit is %d", park.Limit, config.DefaultFixRoundsStage)
	}
	assertFindingsKept(t, got)
}

// TestAConvergenceParkIsNamedOnEverySurface parks a real run on a fix round
// that changed nothing, on the terms TestARoundLimitParkIsNamedOnEverySurface
// states for the round limit. The fixer here writes the same summary every
// round, so the second round leaves the run's state where the first left it,
// well inside the stage's round limit.
func TestAConvergenceParkIsNamedOnEverySurface(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P13)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serveFixing(t, h, alwaysFixable, fixerWritingSummaries(func(int64) string {
		return "nothing this fixer did changed the hunk"
	}), agentCatalog(reviewingRunner(t)))
	mustOK(t, run(t, h, subject, "init"))
	standingOnChange(t, subject, "convergence")

	got := readSurfaces(t, h, subject)
	assertParkNamed(t, got, store.ParkConvergence, "changed nothing", "changed nothing")
	assertFindingsKept(t, got)
}

// TestAStepBudgetParkIsNamedOnEverySurface parks a real run on the run-wide
// step budget, on the terms TestARoundLimitParkIsNamedOnEverySurface states.
// The budget is the trusted copy's, the one place a repository may set it,
// and one step lets the intent stage run and nothing after it.
func TestAStepBudgetParkIsNamedOnEverySurface(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P13)
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	if err := os.WriteFile(filepath.Join(subject, config.RepositoryDocument), []byte(`{"run_budget": 1}`), 0o600); err != nil {
		t.Fatalf("writing the trusted document: %v", err)
	}
	git(t, subject, "add", "-A")
	git(t, subject, "commit", "--quiet", "-m", "hold every run to one step")
	git(t, subject, "push", "--quiet", "origin", "main")
	serve(t, h)
	mustOK(t, run(t, h, subject, "init"))
	standingOnChange(t, subject, "step-budget")

	got := readSurfaces(t, h, subject)
	assertParkNamed(t, got, store.ParkStepBudget, "step budget", "AdoptBudget")
	if park, _ := got.started.Record.Park.Get(); park.Limit != 1 || park.Node != pipeline.StageRebase.Node() {
		t.Fatalf("the park records limit %d in front of %q, want 1 in front of the rebase stage", park.Limit, park.Node)
	}
	if !strings.Contains(got.started.NextAction(), string(config.KeyRunBudget)) {
		t.Fatalf("the next action does not name %s:\n%s", config.KeyRunBudget, got.started.NextAction())
	}
}

// TestACancelledRunAndAParkedRunReadApartOnEverySurface ends one run by
// cancelling it at a hold and holds every surface to saying so, against a run
// a bound parked: both records end terminated, and nothing but the park record
// tells them apart, so a surface that read the status alone would read them
// alike. It cites P8 because the difference is read off the record that owns
// it rather than inferred from where execution stopped, and P13 because what a
// person reads differs in words and not only in a field.
func TestACancelledRunAndAParkedRunReadApartOnEverySurface(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P13)
	requiresIdentifiedPeer(t)

	parkedHome := newHome(t)
	parkedSubject := newSubject(t)
	serveFixing(t, parkedHome, alwaysFixable, fixerWritingSummaries(func(int64) string {
		return "nothing changed"
	}), agentCatalog(reviewingRunner(t)))
	mustOK(t, run(t, parkedHome, parkedSubject, "init"))
	standingOnChange(t, parkedSubject, "parked")
	parked := readSurfaces(t, parkedHome, parkedSubject)

	cancelledHome := newHome(t)
	cancelledSubject := newSubject(t)
	serve(t, cancelledHome)
	mustOK(t, run(t, cancelledHome, cancelledSubject, "init"))
	standingOnChange(t, cancelledSubject, "cancelled")
	held := decodeRun(t, run(t, cancelledHome, cancelledSubject, "--json", "--intent", "a change someone ends").stdout)
	if held.Outcome != machine.OutcomeDecision {
		t.Fatalf("the run to cancel answered %s, want a decision to cancel it at", held.Outcome)
	}
	ended := decodeRun(t, mustOK(t, run(t, cancelledHome, cancelledSubject, "--json", "--cancel")).stdout)
	var cancelled parkedSurfaces
	cancelled.started = ended
	if err := json.Unmarshal([]byte(mustOK(t, run(t, cancelledHome, cancelledSubject, "--json", "status")).stdout), &cancelled.status); err != nil {
		t.Fatalf("status does not decode: %v", err)
	}
	cancelled.statusText = mustOK(t, run(t, cancelledHome, cancelledSubject, "status")).stdout
	if err := json.Unmarshal([]byte(mustOK(t, run(t, cancelledHome, cancelledSubject, "--json", "runs")).stdout), &cancelled.runs); err != nil {
		t.Fatalf("runs does not decode: %v", err)
	}
	cancelled.runsText = mustOK(t, run(t, cancelledHome, cancelledSubject, "runs")).stdout

	// Both records end the same way, which is why the park record exists.
	if parked.started.Record.Status != store.RunTerminated || cancelled.started.Record.Status != store.RunTerminated {
		t.Fatalf("the records end %s and %s, and this test is about two terminated records",
			parked.started.Record.Status, cancelled.started.Record.Status)
	}

	// The answers.
	if cancelled.started.Outcome != machine.OutcomeCancelled || parked.started.Outcome != machine.OutcomeFailed {
		t.Fatalf("the cancelled run answers %s and the parked one %s", cancelled.started.Outcome, parked.started.Outcome)
	}
	if cancelled.started.Record.Park.IsKnown() {
		t.Fatalf("the cancelled run's record names a park: %v", cancelled.started.Record.Park)
	}
	if cancelled.started.NextAction() == parked.started.NextAction() {
		t.Fatalf("both runs carry the next action %q", parked.started.NextAction())
	}

	// status --json and status.
	if cancelled.status.ParkedRun != nil {
		t.Fatal("status reports the cancelled run as parked")
	}
	if parked.status.ParkedRun == nil {
		t.Fatal("status reports nothing about the parked run")
	}
	parkedWords := machine.RecordPhrase(parked.runs.Runs[0]).Outcome
	cancelledWords := machine.RecordPhrase(cancelled.runs.Runs[0]).Outcome
	if parkedWords == cancelledWords {
		t.Fatalf("the two records read alike: %q", parkedWords)
	}
	if strings.Contains(cancelled.statusText, parkedWords) || !strings.Contains(parked.statusText, parkedWords) {
		t.Fatalf("status reads the parked phrase %q wrongly:\ncancelled:\n%s\nparked:\n%s",
			parkedWords, cancelled.statusText, parked.statusText)
	}

	// runs --json and runs.
	if cancelled.runs.Runs[0].Park.IsKnown() || len(cancelled.runs.Parked) != 0 {
		t.Fatalf("runs lists the cancelled run as parked: %+v", cancelled.runs)
	}
	if !parked.runs.Runs[0].Park.IsKnown() || len(parked.runs.Parked) != 1 {
		t.Fatalf("runs does not list the parked run as parked: %+v", parked.runs)
	}
	if !strings.Contains(cancelled.runsText, cancelledWords) || strings.Contains(cancelled.runsText, parkedWords) {
		t.Fatalf("runs does not read the cancelled run as cancelled:\n%s", cancelled.runsText)
	}
	if !strings.Contains(parked.runsText, parkedWords) || strings.Contains(parked.runsText, cancelledWords) {
		t.Fatalf("runs does not read the parked run as parked:\n%s", parked.runsText)
	}
}
