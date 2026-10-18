package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
)

// fixerAccount is what the scripted fixer says it did.
const fixerAccount = "wrote what the person asked for"

// requestedRound drives one run to its first hold, puts in the run's isolated
// copy what a fixer would have written at path - the stand-in prints and
// cannot edit, so the edit is placed the way internal/stages' fix tests place
// it, and everything after it is the product's - and answers the hold with a
// fix round selecting every finding the decision carried, with a note.
//
// It returns the run as the answer left it and the invocations the agent saw.
func requestedRound(t *testing.T, path string) (machine.Run, pipeline.Stage, []standin.Call) {
	t.Helper()
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	agent := standin.New(t, standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")},
		{Times: standin.Always, Reply: standin.Text(fixerAccount)},
	}})
	o := options(t, h)
	o.Catalog = agents.NewCatalog(fixedFactory{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()})

	stage := holdingStage(t, 0)
	var after machine.Run
	withServiceOptions(t, o, func(running serviceUnderTest) {
		run := startRunSkipping(t, running.client, subject, pipeline.StageReview)
		decision := holdingAt(t, run, stage)
		if !contains(decision.Detailed, string(pipeline.OutcomeFixRequested)) {
			t.Fatalf("the hold does not offer a fix round: %+v", decision.Decision)
		}
		var selected []string
		for _, f := range decision.Findings {
			selected = append(selected, f.ID)
		}
		if len(selected) == 0 {
			t.Fatalf("the %s stage holds with no finding to select", stage)
		}
		writeInCopy(t, h, run.Record, path)

		if err := running.client.Call(t.Context(), ipc.MethodRunRespond, machine.RespondRequest{
			Run:    run.Record.ID,
			Answer: string(pipeline.OutcomeFixRequested),
			Fix:    &pipeline.FixRequest{Selected: selected, Notes: "fix the code, not the test"},
		}, &after); err != nil {
			t.Fatalf("answering with a fix round: %v", err)
		}
	})
	return after, stage, agent.Calls()
}

// A fix round asked for at a hold runs the stage's fixer over the selection,
// the stage runs again, and it holds again so the person sees the result. This
// is PRD section 5's Fix action through the machine interface, against the
// real fix body and the real adapter over the stand-in.
//
// The round's commit touches a path no judge pattern matches, so the only
// thing holding the stage afterwards that the round added is the hold every
// requested round leaves; the judge test below is the same run with a judge
// path, and the difference between the two is the judge rule.
func TestAFixRoundAskedForAtAHoldRunsAndTheStageHoldsAgain(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	run, stage, calls := requestedRound(t, "notes.txt")

	holdingAt(t, run, stage)
	report := stageView(t, run, stage).Report
	if report == nil || !hasHeld(*report, "You asked for a fix round") {
		t.Fatalf("the %s stage holds again without the requested round's hold: %+v", stage, report)
	}
	if hasHeld(*report, "notes.txt") {
		t.Fatalf("a round that touched no judge path was held for one: %+v", report.Findings)
	}
	if got := stageView(t, run, stage).Fix; !strings.Contains(got, fixerAccount) {
		t.Fatalf("the stage's fix summary reads %q, want the fixer's account", got)
	}
	if len(calls) != 1 {
		t.Fatalf("the agent was invoked %d time(s), want the one fix round:\n%v", len(calls), calls)
	}
	if !strings.Contains(calls[0].Prompt, "fix the code, not the test") {
		t.Fatalf("the fixer was not given the person's note:\n%s", calls[0].Prompt)
	}
}

// The judge rule reaches a requested round: a commit adding a file a judge
// pattern matches holds the stage with an ask naming it, decided from the
// paths the commit touched. The fixer said nothing about tests, so the account
// is not what decided it.
func TestAFixRoundAskedForAtAHoldIsHeldToTheJudgeRule(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	run, stage, _ := requestedRound(t, "added_test.go")

	holdingAt(t, run, stage)
	report := stageView(t, run, stage).Report
	if report == nil || !hasHeld(*report, "added_test.go") {
		t.Fatalf("a requested round that added a judge file was not held for it: %+v", report)
	}
}

// A request selecting a finding the stage never reported is refused to the
// caller before anything is recorded, and the hold is still open afterwards:
// the same run can still be approved.
func TestAFixRequestSelectingNothingTheStageReportedIsRefused(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	withService(t, h, func(running serviceUnderTest) {
		stage := holdingStage(t, 0)
		run := startRunSkipping(t, running.client, subject, pipeline.StageReview)
		holdingAt(t, run, stage)

		var refused machine.Run
		err := running.client.Call(t.Context(), ipc.MethodRunRespond, machine.RespondRequest{
			Run:    run.Record.ID,
			Answer: string(pipeline.OutcomeFixRequested),
			Fix:    &pipeline.FixRequest{Selected: []string{"never-reported"}},
		}, &refused)
		if err == nil || !strings.Contains(err.Error(), "never-reported") {
			t.Fatalf("a request selecting an unreported finding came to %v, want a refusal naming it", err)
		}
		err = running.client.Call(t.Context(), ipc.MethodRunRespond, machine.RespondRequest{
			Run:    run.Record.ID,
			Answer: string(pipeline.OutcomeApproved),
			Fix:    &pipeline.FixRequest{Selected: []string{"anything"}},
		}, &refused)
		if err == nil {
			t.Fatal("an approval carrying a fix request was accepted")
		}

		after := answer(t, running.client, run.Record.ID, string(pipeline.OutcomeApproved))
		if got := stageView(t, after, stage).Outcome; got != pipeline.OutcomeApproved {
			t.Fatalf("after the refusals the %s stage could not be approved: %s", stage, got)
		}
	})
}

// writeInCopy puts a file in the run's isolated copy, where the fix round
// commits whatever it finds.
func writeInCopy(t *testing.T, h *home.Home, record store.Run, path string) {
	t.Helper()
	dir := h.Worktree(record.RepositoryID, record.ID)
	if err := os.WriteFile(filepath.Join(dir, path), []byte("written by the round\n"), 0o600); err != nil {
		t.Fatalf("writing %s in the run's copy: %v", path, err)
	}
}

// hasHeld reports whether a report holds for a finding whose description
// carries text.
func hasHeld(report findings.Report, text string) bool {
	for _, f := range report.Held() {
		if strings.Contains(f.Description, text) {
			return true
		}
	}
	return false
}

// contains reports whether list holds s.
func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
