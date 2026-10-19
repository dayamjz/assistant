package agents_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// recordedLines reads a recording under testdata as its lines.
func recordedLines(t *testing.T, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// turnFailedMessage is the message of the one turn.failed event in a
// recorded `codex exec --json` stream.
func turnFailedMessage(t *testing.T, lines []string) string {
	t.Helper()
	for _, line := range lines {
		var event struct {
			Type  string `json:"type"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && event.Type == "turn.failed" {
			return event.Error.Message
		}
	}
	t.Fatal("the recording has no turn.failed event")
	return ""
}

// Every row of the one marker table recognizes its own line, the line with
// detail after a separator, and nothing that merely mentions it or runs it
// into another word. The near misses are what keep a row from widening into
// a substring match: a line that only quotes a credential error is not one.
func TestEveryAvailabilityMarkerRecognizesItsLineAndNotANearMiss(t *testing.T) {
	rows := agents.AvailabilityMarkers()
	if len(rows) == 0 {
		t.Fatal("the marker table is empty")
	}
	var credential int
	for _, row := range rows {
		t.Run(row.Marker, func(t *testing.T) {
			if row.Failure != agents.FailureQuota && row.Failure != agents.FailureUnavailable {
				t.Fatalf("row %q classifies as %q, which no fallback reads", row.Marker, row.Failure)
			}
			for _, line := range []string{row.Marker, strings.ToUpper(row.Marker), row.Marker + ": detail", row.Marker + ". detail", row.Marker + " detail"} {
				failure, cause := agents.AvailabilityMessage(line)
				if failure != row.Failure || !errors.Is(cause, row.Cause) || (row.Cause == nil && cause != nil) {
					t.Errorf("%q read as (%q, %v), want (%q, %v)", line, failure, cause, row.Failure, row.Cause)
				}
			}
			for _, miss := range []string{"note: " + row.Marker, row.Marker + "x", "the log says " + row.Marker + ": detail"} {
				// A near miss may still be recognized by a different row,
				// as "429x" is by none and "note: 429" by none; what it
				// may not do is borrow this row's reading by containing
				// its text.
				if failure, _ := agents.AvailabilityMessage(miss); failure != agents.FailureNone {
					t.Errorf("near miss %q read as %q, want unrecognized", miss, failure)
				}
			}
		})
		if errors.Is(row.Cause, agents.ErrCredentialRefused) {
			credential++
			if row.Failure != agents.FailureUnavailable {
				t.Errorf("credential row %q is %q, want unavailable so the fallback rule is unchanged", row.Marker, row.Failure)
			}
		}
	}
	if credential == 0 {
		t.Fatal("no row carries ErrCredentialRefused")
	}
}

// The Codex credential row is pinned to what Codex CLI 0.163.0 printed:
// the turn.failed message of both recordings, missing and rejected, is
// that row's line, and the retry lines before it, which carry the same
// words inside parentheses, are not.
func TestTheCodexCredentialRowMatchesTheRecordedTurnFailed(t *testing.T) {
	for _, name := range []string{"codex-exec-unauthenticated-0.163.0.jsonl", "codex-exec-rejected-0.163.0.jsonl"} {
		t.Run(name, func(t *testing.T) {
			lines := recordedLines(t, name)
			failure, cause := agents.AvailabilityMessage(turnFailedMessage(t, lines))
			if failure != agents.FailureUnavailable || !errors.Is(cause, agents.ErrCredentialRefused) {
				t.Fatalf("the recorded turn.failed read as (%q, %v), want unavailable with ErrCredentialRefused", failure, cause)
			}
			for _, line := range lines {
				var event struct{ Type, Message string }
				if json.Unmarshal([]byte(line), &event) != nil || !strings.HasPrefix(event.Message, "Reconnecting") {
					continue
				}
				if failure, _ := agents.AvailabilityMessage(event.Message); failure != agents.FailureNone {
					t.Errorf("a retry line read as %q: %s", failure, event.Message)
				}
			}
		})
	}
	// The same line with another status is a near miss the row must not
	// take: a 403 is not a credential this package can say is missing.
	if failure, _ := agents.AvailabilityMessage("unexpected status 403 Forbidden: denied, url: https://api.openai.com/v1/responses"); failure != agents.FailureNone {
		t.Fatalf("a 403 read as %q", failure)
	}
}

// A missing or rejected credential reaches a caller of the real adapter as
// FailureUnavailable carrying ErrCredentialRefused, read from the events
// Codex CLI 0.163.0 printed, which the stand-in plays back with the exit
// status and the standard error that CLI gave. The counterfeit is the same
// recording with the status changed to a server error: still a failure the
// agent reported, never a credential and never a reason to change agents.
func TestCodexExecReportsAMissingCredentialAsTypedUnavailable(t *testing.T) {
	for _, test := range []struct {
		name, recording string
		from, to        string
		credential      bool
	}{
		{"unauthenticated", "codex-exec-unauthenticated-0.163.0.jsonl", "", "", true},
		{"rejected", "codex-exec-rejected-0.163.0.jsonl", "", "", true},
		{"server-error", "codex-exec-unauthenticated-0.163.0.jsonl", "401 Unauthorized", "500 Internal Server Error", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout := strings.Join(recordedLines(t, test.recording), "\n") + "\n"
			if test.from != "" {
				stdout = strings.ReplaceAll(stdout, test.from, test.to)
			}
			agent := standin.NewCodex(t, standin.Script{Steps: []standin.Step{{
				Reply: standin.Reply{Stdout: stdout, Stderr: "Reading additional input from stdin...\n", Exit: 1},
			}}})
			_, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, nil))
			var failed *agents.InvocationError
			if !errors.As(err, &failed) {
				t.Fatalf("Run answered %v, want an *InvocationError", err)
			}
			if failed.ExitCode != 1 || failed.Agent != agents.CodexName {
				t.Fatalf("the failure is %+v, want codex exiting 1", failed)
			}
			if test.credential {
				if failed.Failure != agents.FailureUnavailable || !errors.Is(err, agents.ErrCredentialRefused) || !agents.FallbackAllowed(err) {
					t.Fatalf("a missing credential answered %v (%q), want unavailable carrying ErrCredentialRefused", err, failed.Failure)
				}
				return
			}
			if failed.Failure != agents.FailureAgent || errors.Is(err, agents.ErrCredentialRefused) || agents.FallbackAllowed(err) {
				t.Fatalf("a server error answered %v (%q), want the agent's own failure and no fallback", err, failed.Failure)
			}
			if calls := agent.Calls(); len(calls) != 1 || calls[0].Prompt != invocation(t, agents.ShapeText, nil).Prompt {
				t.Fatalf("the stand-in recorded %v, want the one exec call carrying the prompt", calls)
			}
		})
	}
}

// A retry Codex announced and recovered from is not the turn's failure: the
// turn.completed after it is the CLI saying the turn finished.
func TestARecoveredCodexRetryIsNotAFailure(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"thread.started","thread_id":"thread-retry"}`,
		`{"type":"turn.started"}`,
		`{"type":"error","message":"Reconnecting... 1/5 (unexpected status 401 Unauthorized: Missing bearer or basic authentication in header)"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`,
	}, "\n")
	text, _, _, _, agentError, err := agents.DecodeCodexOutput([]byte(stdout))
	if err != nil || agentError != "" || text != "done" {
		t.Fatalf("decoded (%q, %q, %v), want the result and no error", text, agentError, err)
	}
	// Its counterfeit: the same stream with the turn ending failed.
	failed := strings.Replace(stdout, `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`, `{"type":"turn.failed","error":{"message":"unexpected status 401 Unauthorized: Missing bearer"}}`, 1)
	if _, _, _, _, agentError, _ := agents.DecodeCodexOutput([]byte(failed)); agentError != "unexpected status 401 Unauthorized: Missing bearer" {
		t.Fatalf("a failed turn decoded agentError %q", agentError)
	}
}

// A Codex binary that is not there is refused when the runner is built,
// typed as the operating system's own not-found, before any process
// starts; one that goes missing after it was resolved is refused the same
// way by Run and by Start. Neither is an agent's failure, and both are the
// one missing-executable case the fallback rule reads.
func TestAMissingCodexBinaryIsATypedRefusalBeforeAnythingStarts(t *testing.T) {
	_, err := agents.CodexFactory(agents.WithCodexBinary("assistant-no-such-codex")).New(t.Context(), nil)
	if !errors.Is(err, exec.ErrNotFound) || !agents.FallbackAllowed(err) {
		t.Fatalf("a name not on PATH answered %v, want exec.ErrNotFound", err)
	}
	_, err = agents.CodexFactory(agents.WithCodexBinary(filepath.Join(t.TempDir(), "codex"))).New(t.Context(), nil)
	if !errors.Is(err, os.ErrNotExist) || !agents.FallbackAllowed(err) {
		t.Fatalf("a path that does not exist answered %v, want os.ErrNotExist", err)
	}

	if runtime.GOOS == "windows" {
		t.Skip("the vanished-binary half needs an executable script, which this test writes for a unix shell")
	}
	bin := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	recorded := &recorder{}
	runner, err := agents.CodexFactory(agents.WithCodexBinary(bin), agents.WithCodexRecorder(recorded)).New(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, nil))
	var failed *agents.InvocationError
	if !errors.As(err, &failed) || failed.Failure != agents.FailureProcess || failed.ExitCode != -1 || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Run over a vanished binary answered %v, want a process failure carrying os.ErrNotExist and no exit status", err)
	}
	if len(recorded.records) != 1 || recorded.records[0].Failure != agents.FailureProcess {
		t.Fatalf("the recorder saw %+v, want the one process failure", recorded.records)
	}
	_, err = agents.OpenWorker(t.Context(), runner, agents.WorkerSpec{Dir: t.TempDir(), Instructions: "begin"})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Start over a vanished binary answered %v, want os.ErrNotExist", err)
	}
}

// A Codex process that exits non-zero without printing a result is a typed
// failure naming the status, with what it wrote on standard error as the
// message, and is not a reason to change agents.
func TestACodexNonZeroExitWithNoReportNamesTheStatus(t *testing.T) {
	agent := standin.NewCodex(t, standin.Script{Steps: []standin.Step{{
		Reply: standin.Reply{Stderr: "codex: the sandbox could not start\n", Exit: 3},
	}}})
	_, err := agent.Runner().Run(t.Context(), agents.PurposeReview, invocation(t, agents.ShapeText, nil))
	var failed *agents.InvocationError
	if !errors.As(err, &failed) || failed.Failure != agents.FailureExit || failed.ExitCode != 3 {
		t.Fatalf("Run answered %v, want FailureExit with status 3", err)
	}
	if !strings.Contains(err.Error(), "exited 3") || !strings.Contains(failed.Message, "the sandbox could not start") {
		t.Fatalf("the failure reads %q, want the status and the agent's message", err)
	}
	if agents.FallbackAllowed(err) {
		t.Fatal("an ordinary non-zero exit allowed a fallback")
	}
}

// The Codex app-server reports a missing or rejected credential in a
// structured field, and the worker reads it there: the failed turn/completed
// Codex CLI 0.163.0 emitted carries codexErrorInfo with httpStatusCode 401,
// and the worker records FailureUnavailable with ErrCredentialRefused. That
// turn never goes idle, so it also has to settle the turn, or the next Send
// would wait forever; the next Send going through, and clearing the state,
// is that half, and it holds for the server-error counterfeit too, which
// TestCodexTurnFailureReadsTheStructuredFieldFirst shows is not recorded.
func TestCodexWorkerReadsAMissingCredentialFromTheStructuredField(t *testing.T) {
	for _, test := range []struct {
		name, recording string
		credential      bool
	}{
		{"unauthenticated", "codex-app-server-unauthenticated-0.163.0.jsonl", true},
		{"rejected", "codex-app-server-rejected-0.163.0.jsonl", true},
		{"server-error", "codex-app-server-unauthenticated-0.163.0.jsonl", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			frames := recordedLines(t, test.recording)
			if !test.credential {
				for i := range frames {
					frames[i] = strings.ReplaceAll(strings.ReplaceAll(frames[i], `"httpStatusCode":401`, `"httpStatusCode":500`), "401 Unauthorized", "500 Internal Server Error")
				}
			}
			agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{
				{Contains: "initial", Recorded: frames},
				{Contains: "again", Reply: "answered"},
			}}})
			worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "initial"})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			if test.credential {
				deadline := time.Now().Add(5 * time.Second)
				for agents.CurrentWorkerFailure(worker) == agents.FailureNone {
					if time.Now().After(deadline) {
						t.Fatal("the failed turn was never recorded")
					}
					time.Sleep(5 * time.Millisecond)
				}
				if got := agents.CurrentWorkerFailure(worker); got != agents.FailureUnavailable {
					t.Fatalf("the worker reads %q, want unavailable", got)
				}
				typed := agents.CurrentWorkerError(worker)
				var protocol *agents.WorkerError
				if !errors.Is(typed, agents.ErrCredentialRefused) || !errors.As(typed, &protocol) || !agents.FallbackAllowed(typed) {
					t.Fatalf("the typed failure is %v, want a *WorkerError carrying ErrCredentialRefused", typed)
				}
				if worker.Capture().Failure != agents.FailureUnavailable {
					t.Fatalf("the capture reads %q", worker.Capture().Failure)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := worker.Send(ctx, "again"); err != nil {
				t.Fatalf("the Send after a failed turn answered %v; the failed turn did not settle", err)
			}
			if agents.CurrentWorkerFailure(worker) != agents.FailureNone || agents.CurrentWorkerError(worker) != nil {
				t.Fatal("a new turn did not clear the previous turn's failure")
			}
		})
	}
}

// An app-server that dies in the middle of a turn, after accepting it and
// before it settles, is a typed failure: the next Send refuses with a
// *WorkerError that is FailureProcess, names the exit status, and still
// matches ErrWorkerEnded, and the handle's Done is closed. The stand-in
// plays back the first frame Codex CLI 0.163.0 emitted for a turn, an
// agent message delta, and exits. The control is a worker Close ended in
// the middle of a turn, which is not a failure of the agent's.
func TestACodexAppServerThatDiesMidTurnIsATypedFailure(t *testing.T) {
	delta := recordedLines(t, "codex-app-server-0.163.0.jsonl")[3]
	if !strings.Contains(delta, "item/agentMessage/delta") {
		t.Fatalf("line 4 of the recording is not the delta: %s", delta)
	}
	agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{Exit: 3, Exchanges: []standin.Exchange{
		{Contains: "die", Recorded: []string{delta}, Ends: true},
		{Contains: "hang", Recorded: []string{delta}},
	}}})
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "begin"})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	// This Send is accepted, or loses the race with the exit and is refused
	// the same typed way; either is the adapter's to report.
	if err := worker.Send(t.Context(), "die now"); err != nil && !errors.Is(err, agents.ErrWorkerEnded) {
		t.Fatalf("the Send that killed the worker answered %v", err)
	}
	select {
	case <-worker.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the worker never ended")
	}
	err = worker.Send(t.Context(), "anything")
	var protocol *agents.WorkerError
	if !errors.As(err, &protocol) || protocol.Failure != agents.FailureProcess || !errors.Is(err, agents.ErrWorkerEnded) {
		t.Fatalf("a Send after the mid-turn exit answered %v, want a *WorkerError that is a process failure and ErrWorkerEnded", err)
	}
	if !strings.Contains(protocol.Message, "exit status 3") {
		t.Fatalf("the failure reads %q, want the exit status", protocol.Message)
	}
	if agents.FallbackAllowed(err) || agents.CurrentWorkerFailure(worker) != agents.FailureNone {
		t.Fatal("a mid-turn exit was read as an availability failure")
	}

	// The control: the same partial turn, ended by Close.
	closed, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "begin"})
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Send(t.Context(), "hang here"); err != nil {
		t.Fatal(err)
	}
	closed.Close()
	err = closed.Send(t.Context(), "anything")
	if !errors.Is(err, agents.ErrWorkerEnded) || errors.As(err, &protocol) {
		t.Fatalf("a Send after Close answered %v, want ErrWorkerEnded alone", err)
	}
}
