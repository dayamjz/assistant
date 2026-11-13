package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// A command ended at notifyBound that still reports exit zero - a shell whose
// child was killed with the group while the shell raced to exit - settles as
// failed at the bound with no exit status, not as sent. CI macOS observed
// Outcome:sent ExitCode:0 with "Killed: 9" for sleep in the detail when
// settleNotify preferred the exit status over the bound (#899).
func TestSettleNotifyPrefersTheBoundOverARacedZeroExit(t *testing.T) {
	t.Parallel()
	got := settleNotify(context.Background(), time.Second, stages.OperatorResult{
		Exited: true, Code: 0, TimedOut: true,
		Tail: "/bin/sh: line 1: 1 Killed: 9               sleep 60",
	})
	detail := got.Detail
	if got.Outcome != store.NotificationFailed || got.ExitCode.IsKnown() || !strings.Contains(detail, "did not exit within") {
		t.Fatalf("settlement is %+v, want failed at its bound with no exit status", got)
	}
}

// A command that exits zero while the bound still holds is sent, which is
// the positive control for preferring TimedOut only when stages set it.
func TestSettleNotifyCountsAZeroExitInsideTheBoundAsSent(t *testing.T) {
	t.Parallel()
	got := settleNotify(context.Background(), time.Minute, stages.OperatorResult{Exited: true, Code: 0, Tail: "ok"})
	if got.Outcome != store.NotificationSent || got.ExitCode != store.Known(0) || got.Detail != "ok" {
		t.Fatalf("settlement is %+v, want sent with exit status 0", got)
	}
}

// A zero exit inside the bound stays sent even when the caller's context has
// already expired by the time settlement runs (output drain past the bound,
// or the service closing during the drain). TimedOut is false because stages
// read the bound when the command ended, not after the drain.
func TestSettleNotifyKeepsAZeroExitWhenTheDeadlinePassesDuringTheDrain(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := settleNotify(ctx, time.Second, stages.OperatorResult{Exited: true, Code: 0, Tail: "delivered"})
	if got.Outcome != store.NotificationSent || got.ExitCode != store.Known(0) || got.Detail != "delivered" {
		t.Fatalf("settlement is %+v, want sent; a late context must not rewrite a zero exit inside the bound", got)
	}
}
