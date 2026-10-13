package cli_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
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
	serveCatalog(t, h, stages.All, agents.NewCatalog(fixedFactory{runner: agent.Runner()}))

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
