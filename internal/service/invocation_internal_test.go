package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// The recorder the default catalog is built with lands each record in the
// store's agent invocation history, carrying every field across and keeping
// a count the agent reported apart from one it did not. A recorder that
// collapsed the two would fabricate zeros for exactly the invocations whose
// cost is least visible elsewhere.
func TestInvocationRecorderFeedsTheStoreHistory(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"),
		store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rec := invocationRecorder{s: &Service{store: st, stopCtx: ctx}}
	started := time.Date(2026, 9, 19, 19, 0, 0, 0, time.UTC)
	rec.RecordInvocation(agents.Record{
		Purpose:  agents.PurposeReview,
		Agent:    "claude",
		Model:    "some-model",
		Session:  agents.SessionNone,
		Started:  started,
		Duration: 42 * time.Second,
		Failure:  agents.FailureCancelled,
		Usage: agents.Usage{
			InputTokens:  agents.ReportedCount(1500),
			OutputTokens: agents.ReportedCount(0),
		},
	})

	rows, err := st.AgentInvocations(ctx)
	if err != nil {
		t.Fatalf("AgentInvocations: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("history holds %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.Purpose != string(agents.PurposeReview) || got.Agent != "claude" ||
		got.Model != "some-model" || got.SessionUse != string(agents.SessionNone) ||
		got.Failure != string(agents.FailureCancelled) ||
		!got.Started.Equal(started) || got.Duration != 42*time.Second {
		t.Errorf("record landed as %+v", got)
	}
	if n, ok := got.InputTokens.Get(); !ok || n != 1500 {
		t.Errorf("input tokens landed as (%d, %v), want 1500 reported", n, ok)
	}
	if n, ok := got.OutputTokens.Get(); !ok || n != 0 {
		t.Errorf("a reported zero landed as (%d, %v), want a known zero", n, ok)
	}
	if got.CacheReadTokens.IsKnown() || got.CacheCreationTokens.IsKnown() || got.Turns.IsKnown() {
		t.Errorf("counts the agent never reported landed as known: %+v", got)
	}
}

// A recorder can be handed a record before the service's store is open, and
// during shutdown after it is gone; neither may take the invocation down.
// The row is dropped, which is the understatement invocation.go states, and
// nothing else happens.
func TestInvocationRecorderWithoutAStoreDropsTheRecord(t *testing.T) {
	rec := invocationRecorder{s: &Service{}}
	rec.RecordInvocation(agents.Record{Purpose: agents.PurposeFix, Agent: "claude"})
}
