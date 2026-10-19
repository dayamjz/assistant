package agents_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// awaitWorkerFailure polls CurrentWorkerFailure, the read a worker handoff
// in internal/service polls, until it is not FailureNone or five seconds
// pass, and returns what it read last.
func awaitWorkerFailure(t *testing.T, worker agents.Worker) agents.Failure {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := agents.CurrentWorkerFailure(worker); got != agents.FailureNone || time.Now().After(deadline) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A stream worker that prints an error result recognized as a credential
// refusal records it with its typed cause, so CurrentWorkerError matches
// ErrCredentialRefused and names the adapter, not only the category. The
// control is an outage line, which availabilityMarkers gives no cause: the
// same category, and nothing that matches ErrCredentialRefused.
func TestAStreamWorkerCarriesTheTypedCredentialCause(t *testing.T) {
	for _, test := range []struct {
		name, line string
		credential bool
	}{
		{"credential", `{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}`, true},
		{"outage", `{"type":"result","subtype":"success","is_error":true,"result":"Service unavailable"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := standin.New(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{
				{Contains: "initial", Recorded: []string{test.line}},
			}}})
			worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "initial"})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			if got := awaitWorkerFailure(t, worker); got != agents.FailureUnavailable {
				t.Fatalf("the worker reads %q, want unavailable", got)
			}
			typed := agents.CurrentWorkerError(worker)
			var protocol *agents.WorkerError
			if !errors.As(typed, &protocol) || protocol.Agent != agents.ClaudeName || protocol.Failure != agents.FailureUnavailable {
				t.Fatalf("the typed failure is %#v, want a *WorkerError naming %s as unavailable", typed, agents.ClaudeName)
			}
			if got := errors.Is(typed, agents.ErrCredentialRefused); got != test.credential {
				t.Fatalf("the typed failure %v matches ErrCredentialRefused: %v, want %v", typed, got, test.credential)
			}
		})
	}
}

// A Codex app-server that answers turn/start with a JSON-RPC error sends no
// turn/completed after it, so the refusal itself is the only place the
// failure can be read. Send returns it typed, and it is recorded as well,
// so CurrentWorkerFailure, which the fallback watcher polls, reads it and
// CurrentWorkerError carries the cause. The control is an error no marker
// recognizes: Send still returns it, and nothing is recorded, on the terms
// a failed turn/completed is held to. Either way the next Send goes
// through and finds nothing recorded.
func TestACodexTurnStartRefusalIsRecordedForTheFallbackWatcher(t *testing.T) {
	for _, test := range []struct {
		name, refusal string
		want          agents.Failure
		credential    bool
	}{
		{"credential", "unexpected status 401 Unauthorized: Missing bearer or basic authentication in header", agents.FailureUnavailable, true},
		{"unrecognized", "invalid params: unknown field", agents.FailureNone, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{
				{Contains: "refuse", Refuse: test.refusal},
				{Contains: "again", Reply: "answered"},
			}}})
			worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "begin"})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			err = worker.Send(ctx, "refuse this turn")
			var protocol *agents.WorkerError
			if !errors.As(err, &protocol) || protocol.Agent != agents.CodexName || protocol.Message != test.refusal {
				t.Fatalf("the refused Send answered %v, want a *WorkerError carrying the refusal", err)
			}
			if got := agents.CurrentWorkerFailure(worker); got != test.want {
				t.Fatalf("after the refusal the worker reads %q, want %q", got, test.want)
			}
			typed := agents.CurrentWorkerError(worker)
			if test.credential {
				if !errors.Is(typed, agents.ErrCredentialRefused) || !agents.FallbackAllowed(typed) {
					t.Fatalf("the recorded failure is %v, want one carrying ErrCredentialRefused", typed)
				}
			} else if typed != nil {
				t.Fatalf("an unrecognized refusal recorded %v", typed)
			}
			if err := worker.Send(ctx, "again"); err != nil {
				t.Fatalf("the Send after a refused turn/start answered %v", err)
			}
			if agents.CurrentWorkerFailure(worker) != agents.FailureNone || agents.CurrentWorkerError(worker) != nil {
				t.Fatal("a new turn did not clear the refused turn's failure")
			}
		})
	}
}

// A Cursor ACP worker whose session/prompt is answered with a JSON-RPC
// error records a recognized one when the answer arrives, before any later
// Send returns it, so the fallback watcher's poll reads it while the worker
// sits idle. The control is an unrecognized error: the next Send returns
// it typed, and nothing is recorded.
func TestACursorPromptRefusalIsRecordedForTheFallbackWatcher(t *testing.T) {
	for _, test := range []struct {
		name, refusal string
		credential    bool
	}{
		{"credential", "Authentication required", true},
		{"unrecognized", "invalid params: unknown field", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := standin.NewCursor(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{
				{Contains: "refuse", Refuse: test.refusal},
				{Contains: "after", Reply: "answered"},
			}}})
			worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "begin"})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := worker.Send(ctx, "refuse this prompt"); err != nil {
				t.Fatalf("writing the prompt answered %v", err)
			}
			if test.credential {
				if got := awaitWorkerFailure(t, worker); got != agents.FailureUnavailable {
					t.Fatalf("the refused prompt was never recorded; the worker reads %q", got)
				}
				if typed := agents.CurrentWorkerError(worker); !errors.Is(typed, agents.ErrCredentialRefused) {
					t.Fatalf("the recorded failure is %v, want one carrying ErrCredentialRefused", typed)
				}
			}
			err = worker.Send(ctx, "after the refusal")
			var protocol *agents.WorkerError
			if !errors.As(err, &protocol) || protocol.Agent != agents.CursorName || protocol.Message != test.refusal {
				t.Fatalf("the Send after the refusal answered %v, want the refusal as a *WorkerError", err)
			}
			if !test.credential && (agents.CurrentWorkerFailure(worker) != agents.FailureNone || agents.CurrentWorkerError(worker) != nil) {
				t.Fatal("an unrecognized refusal was recorded")
			}
		})
	}
}
