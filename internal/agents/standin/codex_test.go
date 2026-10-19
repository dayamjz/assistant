package standin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
)

// Codex one-shots go through the same stand-in the Claude adapter does, on
// the wire `codex exec --json` prints. A review carries no session. A fixer
// round resumes the thread id the first round was given. OpenWorker is the
// typed refusal, and it starts no process.
func TestCodexOneShotAnswersOnTheExecJSONWire(t *testing.T) {
	principles.Cite(t, principles.P4)
	agent := NewCodex(t, Script{Steps: []Step{{
		Times: Always,
		Reply: Text("the change looks fine"),
	}}})
	dir := t.TempDir()
	result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, agents.Invocation{
		Prompt: "review the subject", Shape: agents.ShapeText, Dir: dir,
	})
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if result.Text != "the change looks fine" {
		t.Fatalf("text = %q", result.Text)
	}
	if result.Record.Agent != agents.CodexName || result.Record.Session != agents.SessionNone {
		t.Fatalf("record = %+v, want codex with no session", result.Record)
	}
	in, ok := result.Record.Usage.InputTokens.Value()
	if !ok || in != 101 {
		t.Fatalf("input tokens = %d reported=%v, want 101", in, ok)
	}
	if _, ok := result.Record.Usage.Turns.Value(); ok {
		t.Fatal("turns were reported; turn.completed has no turns field")
	}

	fixer, err := agents.OpenFixer(t.Context(), agent.Runner(), "")
	if err != nil {
		t.Fatalf("OpenFixer: %v", err)
	}
	fixed, err := fixer.Apply(t.Context(), agents.Invocation{
		Prompt: "fix the subject", Shape: agents.ShapeText, Dir: dir,
	})
	if err != nil {
		t.Fatalf("fix: %v", err)
	}
	if fixed.Record.Session != agents.SessionOpened {
		t.Fatalf("the first fix round recorded session %s, want opened", fixed.Record.Session)
	}
	if _, err := fixer.Apply(t.Context(), agents.Invocation{
		Prompt: "fix the subject again", Shape: agents.ShapeText, Dir: dir,
	}); err != nil {
		t.Fatalf("second fix: %v", err)
	}

	calls := agent.Calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want review and two fix rounds: %s", len(calls), calls)
	}
	if calls[0].Session() != "" || calls[0].Prompt != "review the subject" || calls[0].Worker {
		t.Fatalf("review call = %s, want a one-shot with no session", calls[0])
	}
	if !containsAll(calls[0].Args, "exec", "--json", "--sandbox", "read-only") {
		t.Fatalf("review args = %q, want exec --json and a read-only grant", calls[0].Args)
	}
	if slicesHas(calls[0].Args, "--approve-for-me") {
		t.Fatalf("a review was given the edit grant: %q", calls[0].Args)
	}
	if calls[1].Session() != "" {
		t.Fatalf("the first fix round resumed %q; opening a session resumes nothing", calls[1].Session())
	}
	opened := fmt.Sprintf("standin-session-%d", calls[1].Seq)
	if calls[2].Session() != opened {
		t.Fatalf("the second fix round resumed %q, want %q", calls[2].Session(), opened)
	}
	if !slicesHas(calls[1].Args, "--approve-for-me") || slicesHas(calls[1].Args, "--sandbox") {
		t.Fatalf("fix args = %q, want --approve-for-me and no --sandbox", calls[1].Args)
	}

	before := len(agent.Calls())
	_, err = agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: dir, Instructions: "work"})
	var refusal *agents.CapabilityError
	if !errors.As(err, &refusal) || refusal.Capability != agents.CapabilityWorkerSessions || refusal.Agent != agents.CodexName {
		t.Fatalf("OpenWorker answered %v, want a *CapabilityError for worker_sessions on codex", err)
	}
	if len(agent.Calls()) != before {
		t.Fatal("OpenWorker started a process")
	}
}

// A count the envelope reports is on the wire, and one it does not report
// stays unreported. Turns are omitted even when the envelope named them,
// which TestCodexOneShotAnswersOnTheExecJSONWire holds for the default usage.
func TestCodexWireOmitsUnreportedCounts(t *testing.T) {
	agent := NewCodex(t, Script{Steps: []Step{{Times: 1, Reply: Reply{Envelope: &Envelope{
		Result: "done",
		Usage:  agents.Usage{InputTokens: agents.ReportedCount(7)},
	}}}}})
	result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, agents.Invocation{
		Prompt: "count", Shape: agents.ShapeText, Dir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	in, ok := result.Record.Usage.InputTokens.Value()
	if !ok || in != 7 {
		t.Fatalf("input = %d reported=%v, want 7", in, ok)
	}
	if _, ok := result.Record.Usage.OutputTokens.Value(); ok {
		t.Fatal("output tokens were reported from an envelope that omitted them")
	}
	if _, ok := result.Record.Usage.Turns.Value(); ok {
		t.Fatal("turns were reported")
	}
}

func TestCodexLoginFailureIsTheAgentsOwn(t *testing.T) {
	agent := NewCodex(t, Script{Steps: []Step{{Times: 1, Reply: Failed("Not logged in. Run codex login to authenticate.")}}})
	_, err := agent.Runner().Run(t.Context(), agents.PurposeReview, agents.Invocation{
		Prompt: "review", Shape: agents.ShapeText, Dir: t.TempDir(),
	})
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) || refusal.Failure != agents.FailureAgent {
		t.Fatalf("Run answered %v, want FailureAgent", err)
	}
	if !strings.Contains(refusal.Message, "Not logged in") {
		t.Fatalf("message = %q", refusal.Message)
	}
}

func TestCodexAgentTextSayingCancelledIsNotACancellation(t *testing.T) {
	agent := NewCodex(t, Script{Steps: []Step{{Times: 1, Reply: Failed("the run was cancelled by the user")}}})
	_, err := agent.Runner().Run(t.Context(), agents.PurposeReview, agents.Invocation{
		Prompt: "review", Shape: agents.ShapeText, Dir: t.TempDir(),
	})
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) || refusal.Failure != agents.FailureAgent {
		t.Fatalf("Run answered %v, want FailureAgent", err)
	}
}

func TestCodexCancellationEndsTheLocalProcess(t *testing.T) {
	agent := NewCodex(t, Script{Steps: []Step{{Times: 1, Reply: Hang()}}})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := agent.Runner().Run(ctx, agents.PurposeReview, agents.Invocation{
			Prompt: "hang", Shape: agents.ShapeText, Dir: t.TempDir(),
		})
		done <- err
	}()
	deadline := time.Now().Add(30 * time.Second)
	for len(agent.Calls()) == 0 {
		if !time.Now().Before(deadline) {
			cancel()
			t.Fatal("the stand-in never recorded the invocation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	var refusal *agents.InvocationError
	if err := <-done; !errors.As(err, &refusal) || refusal.Failure != agents.FailureCancelled {
		t.Fatalf("Run answered %v, want FailureCancelled", err)
	}
}

func containsAll(args []string, want ...string) bool {
	for _, w := range want {
		if !slicesHas(args, w) {
			return false
		}
	}
	return true
}

func slicesHas(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
