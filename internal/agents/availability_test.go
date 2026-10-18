package agents_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/principles"
)

func TestWorkerAvailabilityNeedsAnExplicitError(t *testing.T) {
	for _, test := range []struct {
		name, text string
		want       agents.Failure
	}{
		{"quota", `{"type":"result","is_error":true,"errors":["You've hit your limit. Resets tomorrow."]}` + "\n", agents.FailureQuota},
		{"spend", `{"type":"result","is_error":true,"result":"You've hit your monthly spend limit. Resets Monday."}` + "\n", agents.FailureQuota},
		{"spend-later", `{"type":"result","is_error":true,"result":"see you've hit your monthly spend limit in the notes"}` + "\n", agents.FailureNone},
		{"rejected", `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}` + "\n" + `{"type":"result","is_error":true,"result":"error_during_execution"}` + "\n", agents.FailureQuota},
		{"success", `{"type":"result","is_error":false,"result":"quota exceeded"}` + "\n", agents.FailureNone},
		{"unrelated-error", `{"type":"result","is_error":true,"result":"could not read quota exceeded.md"}` + "\n", agents.FailureNone},
		{"warning", `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning"}}` + "\n", agents.FailureNone},
		{"partial", `{"type":"result","is_error":true,"result":"quota exceeded"}`, agents.FailureNone},
		{"context", `{"type":"result","is_error":true,"result":"context window exceeded"}` + "\n", agents.FailureNone},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := agents.WorkerAvailability(agents.Capture{Text: test.text, Format: agents.OutputStreamJSON}); got != test.want {
				t.Fatalf("got %s want %s", got, test.want)
			}
		})
	}
	if agents.FallbackAllowed(errors.New("quota exceeded")) {
		t.Fatal("untyped error triggered fallback")
	}
}

// P8: an old quota result retained in the output is not current state after
// a new message is queued. The adapter, not a tail reader, owns that state.
func TestWorkerAvailabilityIsIndependentOfRetainedOutput(t *testing.T) {
	principles.Cite(t, principles.P8)
	agent := standin.New(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{{Contains: "initial", Recorded: []string{`{"type":"result","is_error":true,"result":"quota exceeded"}`}}}}})
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	deadline := time.Now().Add(3 * time.Second)
	for worker.Capture().Failure != agents.FailureQuota {
		if time.Now().After(deadline) {
			t.Fatal("quota was not observed")
		}
		time.Sleep(time.Millisecond)
	}
	if err = worker.Send(t.Context(), "a new message with no scripted reply"); err != nil {
		t.Fatal(err)
	}
	capture := worker.Capture()
	if capture.Failure != agents.FailureNone || !strings.Contains(capture.Text, "quota exceeded") {
		t.Fatalf("state inferred from retained output: %+v", capture)
	}
}
