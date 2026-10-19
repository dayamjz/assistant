package agents_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// TestDecodeCodexOutputReadsJSONLEvents holds the decoder to the wire shape
// `codex exec --json` actually prints. The stand-in helper still speaks a
// Claude-shaped single envelope for process-level tests; without this fixture,
// deleting decodeCodexJSONL would not fail the suite.
func TestDecodeCodexOutputReadsJSONLEvents(t *testing.T) {
	t.Parallel()
	stdout := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-abc"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"reasoning","text":"thinking"}}`,
		`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"first"}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"final answer"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":20,"cached_input_tokens":3,"cache_write_input_tokens":4}}`,
	}, "\n")

	text, threadID, _, usage, agentError, err := agents.DecodeCodexOutput([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeCodexOutput: %v", err)
	}
	if text != "final answer" {
		t.Errorf("text = %q, want the last agent_message", text)
	}
	if threadID != "thread-abc" {
		t.Errorf("threadID = %q, want thread-abc", threadID)
	}
	if agentError != "" {
		t.Errorf("agentError = %q, want empty", agentError)
	}
	wantUsage := agents.Usage{
		InputTokens:         agents.ReportedCount(10),
		OutputTokens:        agents.ReportedCount(20),
		CacheReadTokens:     agents.ReportedCount(3),
		CacheCreationTokens: agents.ReportedCount(4),
	}
	if usage != wantUsage {
		t.Errorf("usage = %+v, want %+v", usage, wantUsage)
	}
}

func TestDecodeCodexOutputReportsJSONLError(t *testing.T) {
	t.Parallel()
	stdout := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-err"}`,
		`{"type":"error","message":"quota exceeded"}`,
	}, "\n")

	_, threadID, _, _, agentError, err := agents.DecodeCodexOutput([]byte(stdout))
	if err != nil {
		t.Fatalf("DecodeCodexOutput: %v", err)
	}
	if threadID != "thread-err" {
		t.Errorf("threadID = %q, want thread-err", threadID)
	}
	if agentError != "quota exceeded" {
		t.Errorf("agentError = %q, want quota exceeded", agentError)
	}
}

func TestDecodeCodexOutputRefusesEmptyStdout(t *testing.T) {
	t.Parallel()
	if _, _, _, _, _, err := agents.DecodeCodexOutput(nil); err == nil {
		t.Fatal("empty stdout succeeded")
	}
}

// TestCodexFixerResumesViaExecResume proves the resume argv path the
// capability declaration alone does not cover: a second Apply must pass
// `resume <thread_id>` rather than opening a fresh thread.
func TestCodexFixerResumesViaExecResume(t *testing.T) {
	t.Parallel()
	factory := agents.CodexFactory(
		agents.WithCodexBinary(helperBinary(t)),
		agents.WithCodexBaseEnvironment([]string{helperModeVar + "=call"}),
	)
	runner, err := factory.New(t.Context(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fixer, err := agents.OpenFixer(t.Context(), runner, "")
	if err != nil {
		t.Fatalf("OpenFixer: %v", err)
	}

	inv := invocation(t, agents.ShapeText, nil)
	first, err := fixer.Apply(t.Context(), inv)
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if resumed := decodeCall(t, first.Text).resumedSession(); resumed != "" {
		t.Errorf("first round resumed %q", resumed)
	}
	if fixer.Reference() != "session-opened" {
		t.Fatalf("fixer holds %q after first round, want session-opened", fixer.Reference())
	}

	second, err := fixer.Apply(t.Context(), inv)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	call := decodeCall(t, second.Text)
	if resumed := call.resumedSession(); resumed != "session-opened" {
		t.Errorf("second round resumed %q, want session-opened", resumed)
	}
	if !slicesContains(call.Args, "resume") {
		t.Errorf("second round missing resume subcommand: %q", call.Args)
	}
	// Parent grant/json flags must precede the resume subcommand so non-global
	// Codex options remain parseable.
	resumeAt := indexOf(call.Args, "resume")
	jsonAt := indexOf(call.Args, "--json")
	if resumeAt < 0 || jsonAt < 0 || jsonAt > resumeAt {
		t.Errorf("--json must precede resume subcommand: %q", call.Args)
	}
}

// A JSONL error event is the CLI's own failure, including a missing login.
// The words are not reclassified as a cancellation, and a process that did
// report a status is not FailureProcess.
func TestCodexReportsALoginErrorAsTheAgentsOwnFailure(t *testing.T) {
	t.Parallel()
	runner, err := agents.CodexFactory(agents.WithCodexBinary(helperBinary(t))).New(t.Context(), nil)
	if err != nil {
		t.Fatalf("building the codex adapter: %v", err)
	}
	_, err = runner.Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "raw",
		helperExitVar:   "1",
		helperStdoutVar: "{\"type\":\"error\",\"message\":\"Not logged in. Run codex login to authenticate.\"}\n",
	}))
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) {
		t.Fatalf("Run answered %v, want an *InvocationError", err)
	}
	if refusal.Failure != agents.FailureAgent {
		t.Errorf("failure = %s, want %s", refusal.Failure, agents.FailureAgent)
	}
	if !strings.Contains(refusal.Message, "Not logged in") {
		t.Errorf("message = %q, want the CLI's login text", refusal.Message)
	}
}

// Text that says the run was cancelled is still the agent's own failure when
// the caller's context was not cancelled. This adapter cancels only by ending
// the local process tree.
func TestCodexDoesNotTreatAgentTextAsCancellation(t *testing.T) {
	t.Parallel()
	runner, err := agents.CodexFactory(agents.WithCodexBinary(helperBinary(t))).New(t.Context(), nil)
	if err != nil {
		t.Fatalf("building the codex adapter: %v", err)
	}
	_, err = runner.Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "raw",
		helperExitVar:   "1",
		helperStdoutVar: "{\"type\":\"error\",\"message\":\"the run was cancelled by the user\"}\n",
	}))
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) || refusal.Failure != agents.FailureAgent {
		t.Fatalf("Run answered %v, want FailureAgent", err)
	}
}

// A non-zero exit with no JSONL event is the process's own status, which is
// FailureExit, distinct from a process that never started.
func TestCodexReportsANonZeroExitWithoutEventsAsExit(t *testing.T) {
	t.Parallel()
	runner, err := agents.CodexFactory(agents.WithCodexBinary(helperBinary(t))).New(t.Context(), nil)
	if err != nil {
		t.Fatalf("building the codex adapter: %v", err)
	}
	_, err = runner.Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, map[string]string{
		helperModeVar:   "raw",
		helperExitVar:   "2",
		helperStderrVar: "codex: boom",
	}))
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) || refusal.Failure != agents.FailureExit {
		t.Fatalf("Run answered %v, want FailureExit", err)
	}
	if !strings.Contains(refusal.Message, "boom") {
		t.Errorf("message = %q, want the process's stderr", refusal.Message)
	}
}

// Replacing the executable after New is a process that cannot start. The
// refusal names FailureProcess rather than an agent verdict.
func TestCodexReportsAProcessThatCannotStart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner, err := agents.CodexFactory(agents.WithCodexBinary(bin)).New(t.Context(), nil)
	if err != nil {
		t.Fatalf("building the codex adapter: %v", err)
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, nil))
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) || refusal.Failure != agents.FailureProcess {
		t.Fatalf("Run answered %v, want FailureProcess", err)
	}
}

// Cancellation is the caller's context ending the local process tree.
func TestCodexCancellationIsTheCallersContext(t *testing.T) {
	runner, err := agents.CodexFactory(agents.WithCodexBinary(helperBinary(t))).New(t.Context(), nil)
	if err != nil {
		t.Fatalf("building the codex adapter: %v", err)
	}
	inv, pidFile := spawningInvocation(t, "spawn")
	ctx, spawned := cancelOnceSpawned(t, pidFile)
	_, err = runner.Run(ctx, agents.PurposeReview, inv)
	spawned()
	var refusal *agents.InvocationError
	if !errors.As(err, &refusal) || refusal.Failure != agents.FailureCancelled {
		t.Fatalf("Run answered %v, want FailureCancelled", err)
	}
}

func slicesContains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}
