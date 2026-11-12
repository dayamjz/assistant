package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// A command ended at notifyBound that still reports exit zero — a shell whose
// child was killed with the group while the shell raced to exit — settles as
// failed at the bound with no exit status, not as sent. CI macOS observed
// Outcome:sent ExitCode:0 with "Killed: 9" for sleep in the detail when
// settleNotify preferred the exit status over the deadline (#899).
func TestSettleNotifyPrefersTheBoundOverARacedZeroExit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runCtx, cancel := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel()
	<-runCtx.Done()
	got := settleNotify(ctx, runCtx, time.Second, stages.OperatorResult{
		Exited: true, Code: 0, Tail: "/bin/sh: line 1: 1 Killed: 9               sleep 60",
	})
	detail := got.Detail
	if got.Outcome != store.NotificationFailed || got.ExitCode.IsKnown() || !strings.Contains(detail, "did not exit within") {
		t.Fatalf("settlement is %+v, want failed at its bound with no exit status", got)
	}
}

// A command that exits zero while the bound still holds is sent, which is
// the positive control for preferring the deadline only when it has passed.
func TestSettleNotifyCountsAZeroExitInsideTheBoundAsSent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	got := settleNotify(ctx, runCtx, time.Minute, stages.OperatorResult{Exited: true, Code: 0, Tail: "ok"})
	if got.Outcome != store.NotificationSent || got.ExitCode != store.Known(0) || got.Detail != "ok" {
		t.Fatalf("settlement is %+v, want sent with exit status 0", got)
	}
}
