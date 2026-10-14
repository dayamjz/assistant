package service

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// usage.read answers over the store's history as it stands at the read, and
// refuses a window it cannot read as an invalid request rather than reading
// the default in its place. The call before the window is the control that
// the window bounds what the store is asked for.
func TestTheUsageReadTotalsTheHistoryOverTheWindowAndRefusesABadOne(t *testing.T) {
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Service{store: st}
	now := time.Now().UTC()
	for _, started := range []time.Time{now.Add(-3 * time.Hour), now.Add(-time.Hour)} {
		if _, err := st.AppendAgentInvocation(ctx, store.AgentInvocation{Purpose: "review", Agent: "claude",
			SessionUse: "none", Started: started}); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}

	got, err := s.usage(ctx, machine.UsageRequest{Window: "2h"})
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if got.Window != "2h" || got.Total.Calls != 1 {
		t.Errorf("the read covers %q with %d calls, want 2h with one", got.Window, got.Total.Calls)
	}
	if all, err := s.usage(ctx, machine.UsageRequest{}); err != nil || all.Window != machine.DefaultUsageWindow || all.Total.Calls != 2 {
		t.Errorf("the default read covers %q with %d calls (%v), want the default with both", all.Window, all.Total.Calls, err)
	}
	if _, err := s.usage(ctx, machine.UsageRequest{Window: "soon"}); !errors.Is(err, ipc.ErrInvalidRequest) ||
		!errors.Is(err, machine.ErrUsageWindow) {
		t.Errorf("a window that does not parse answered %v, want an invalid request naming the window", err)
	}
}
