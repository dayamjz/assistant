package cli_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/stages"
)

// PRD section 5's Fix action through the command line: --answer fix with the
// findings selected, a note, and a finding of the caller's own reaches the
// held stage's fixer, the stage runs again, and the run holds there again. No
// verb is added for it; it is a flag set on the command section 9 names.
func TestAFixRoundIsAskedForWithFlagsOnTheAnswer(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	agent := standin.New(t, standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")},
		{Times: standin.Always, Reply: standin.Text("did what was asked")},
	}})
	serveCatalog(t, h, stages.All, agentCatalog(agent.Runner()))

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
	started := run(t, h, subject, "--json", "--intent", "a change with its tradeoffs stated")
	first := decodeRun(t, started.stdout)
	if first.Decision == nil || len(first.Decision.Findings) == 0 {
		t.Fatalf("the run is not holding on a finding to select: %+v", first.Decision)
	}
	held := first.Decision.Stage

	human := run(t, h, subject, "status")
	if !strings.Contains(human.stdout, "assistant --answer fix --select") {
		t.Fatalf("the human rendering does not say how to ask for a fix round:\n%s", human.stdout)
	}

	answered := run(t, h, subject, "--json", "--answer", "fix",
		"--select", first.Decision.Findings[0].ID,
		"--note", "keep the public name",
		"--finding", "also say why in the commit")
	if answered.code != machine.ExitOK {
		t.Fatalf("asking for a fix round exited %s:\n%s%s", answered.code, answered.stdout, answered.stderr)
	}
	second := decodeRun(t, answered.stdout)
	if second.Decision == nil || second.Decision.Stage != held {
		t.Fatalf("after the round the run is not holding at %s again: %+v", held, second.Decision)
	}
	// The review stage runs on this walk and the stand-in answers it, so the
	// round is told apart from it by the fixer's own opening line.
	var rounds []standin.Call
	for _, call := range agent.Calls() {
		if strings.HasPrefix(call.Prompt, "You are fixing a change") {
			rounds = append(rounds, call)
		}
	}
	if len(rounds) != 1 {
		t.Fatalf("the fixer was invoked %d time(s), want the one requested round:\n%v", len(rounds), agent.Calls())
	}
	for _, want := range []string{"keep the public name", "also say why in the commit", first.Decision.Findings[0].Description} {
		if !strings.Contains(rounds[0].Prompt, want) {
			t.Errorf("the fixer was not given %q:\n%s", want, rounds[0].Prompt)
		}
	}
}

// The flags saying what a fix round resolves are read only with --answer fix.
// Given with any other answer, or with none, they are refused as incorrect
// usage rather than dropped.
func TestTheFixRoundFlagsAreRefusedWithoutTheFixAnswer(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	subject := newSubject(t)
	for _, args := range [][]string{
		{"--select", "one"},
		{"--answer", string(pipeline.OutcomeApproved), "--select", "one"},
		{"--answer", string(pipeline.OutcomeApproved), "--note", "a note"},
		{"--answer", string(pipeline.OutcomeSkipped), "--finding", "mine"},
	} {
		got := run(t, h, subject, args...)
		if got.code != machine.ExitUsage {
			t.Errorf("assistant %s exited %s, want incorrect usage:\n%s%s",
				strings.Join(args, " "), got.code, got.stdout, got.stderr)
		}
	}
}

// An identifier the held stage's report does not carry is the caller's
// mistake, not the service failing: after a fix round the stage reports again
// and its identifiers can change, so one copied from the previous hold goes
// stale in ordinary use. It crosses the wire as an invalid request, exits with
// the incorrect-usage code, and the refusal names the identifiers the stage
// does report, so the caller can correct the selection without guessing. The
// decision stays open.
func TestAStaleFindingIdentifierIsRefusedAsUsageNamingTheReportedOnes(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	agent := standin.New(t, standin.Script{Steps: []standin.Step{
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")},
	}})
	serveCatalog(t, h, stages.All, agentCatalog(agent.Runner()))

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")
	first := decodeRun(t, run(t, h, subject, "--json", "--intent", "a change with its tradeoffs stated").stdout)
	if first.Decision == nil || len(first.Decision.Findings) == 0 {
		t.Fatalf("the run is not holding on a finding to select: %+v", first.Decision)
	}
	const stale = "an-identifier-from-an-earlier-report"

	answered := run(t, h, subject, "--json", "--answer", string(pipeline.OutcomeFixRequested), "--select", stale)
	if answered.code != machine.ExitUsage {
		t.Fatalf("selecting a stale identifier exited %s, want incorrect usage:\n%s%s", answered.code, answered.stdout, answered.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(answered.stdout), &failure); err != nil {
		t.Fatalf("the refusal is not one document: %v\n%s", err, answered.stdout)
	}
	if failure.Code != string(ipc.CodeInvalidRequest) {
		t.Fatalf("the refusal crossed the wire as %q, want %q: %s", failure.Code, ipc.CodeInvalidRequest, failure.Error)
	}
	if !strings.Contains(failure.Error, strconv.Quote(stale)) {
		t.Errorf("the refusal does not name the identifier it refused: %s", failure.Error)
	}
	for _, f := range first.Decision.Findings {
		if !strings.Contains(failure.Error, strconv.Quote(f.ID)) {
			t.Errorf("the refusal does not name %q, which the held stage reports: %s", f.ID, failure.Error)
		}
	}
	if !strings.Contains(failure.Error, "assistant --json status") {
		t.Errorf("the refusal does not say where every reported identifier is: %s", failure.Error)
	}
	t.Logf("assistant --json --answer fix --select %s:\n%s", stale, answered.stdout)

	// The person's rendering is the same refusal on standard error, under the
	// same exit code.
	human := run(t, h, subject, "--answer", string(pipeline.OutcomeFixRequested), "--select", stale)
	if human.code != machine.ExitUsage || !strings.Contains(human.stderr, strconv.Quote(first.Decision.Findings[0].ID)) {
		t.Fatalf("the human rendering exited %s and said:\n%s", human.code, human.stderr)
	}

	// Nothing was recorded: the run still holds on the same decision.
	still := decodeRun(t, run(t, h, subject, "--json").stdout)
	if still.Decision == nil || still.Decision.Stage != first.Decision.Stage || still.Record.ID != first.Record.ID {
		t.Fatalf("after the refusal the run stands at %+v, want the same hold", still.Decision)
	}
}
