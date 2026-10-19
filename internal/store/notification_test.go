package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// appendWake appends one wake of the task under the store's incarnation, the
// way the supervisor loop does, and returns the row.
func appendWake(t *testing.T, s *Store, task, marker string) Wake {
	t.Helper()
	out, err := s.AppendWakeWithMarkers(context.Background(), incarnationOf(t, s),
		WakeRequest{TaskID: task, Kind: "hold-opened", Marker: marker}, MarkerAdvance{})
	if err != nil || out.Disposition != WakeAppended {
		t.Fatalf("appending wake %s: %v (%s)", marker, err, out.Disposition)
	}
	return out.Wake
}

// pendingIDs is PendingNotifications as wake identifiers.
func pendingIDs(t *testing.T, s *Store) []int64 {
	t.Helper()
	pending, err := s.PendingNotifications(context.Background(), 100)
	if err != nil {
		t.Fatalf("PendingNotifications: %v", err)
	}
	ids := make([]int64, len(pending))
	for i, w := range pending {
		ids[i] = w.ID
	}
	return ids
}

// A wake is offered once. Every queued wake with no record is pending,
// oldest first, and an acknowledged one never is. A claim takes the wake out
// of the pending set before anything settles it, a second claim of the same
// wake is refused, and a claim of an acknowledged wake is refused, so a wake
// two senders both read as pending is run by one. The claimed row reads as
// unsettled until it is settled, and then reads back with its outcome, its
// exit status and its detail redacted; a second settlement is refused and
// the first stands, and the database refuses a direct rewrite or a delete.
func TestAWakeIsClaimedOnceAndSettledOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "notifytaskaa").ID
	first := appendWake(t, s, task, "hold:one")
	second := appendWake(t, s, task, "hold:two")
	closed := appendWake(t, s, task, "hold:three")
	if err := s.AcknowledgeWakes(ctx, []int64{closed.ID}, AcknowledgedByCoordinator()); err != nil {
		t.Fatal(err)
	}
	if got := pendingIDs(t, s); len(got) != 2 || got[0] != first.ID || got[1] != second.ID {
		t.Fatalf("pending = %v, want the two queued wakes %d and %d, oldest first", got, first.ID, second.ID)
	}

	claimed, err := s.ClaimNotification(ctx, first.ID)
	if err != nil || claimed.Settled() || claimed.WakeID != first.ID {
		t.Fatalf("claiming wake %d = %+v, %v; want an unsettled claim", first.ID, claimed, err)
	}
	if got := pendingIDs(t, s); len(got) != 1 || got[0] != second.ID {
		t.Fatalf("after the claim pending = %v, want only %d", got, second.ID)
	}
	if _, err := s.ClaimNotification(ctx, first.ID); !errors.Is(err, ErrNotificationClaimed) {
		t.Fatalf("a second claim = %v, want ErrNotificationClaimed", err)
	}
	if _, err := s.ClaimNotification(ctx, closed.ID); !errors.Is(err, ErrNotificationNotOffered) {
		t.Fatalf("claiming an acknowledged wake = %v, want ErrNotificationNotOffered", err)
	}
	if _, err := s.ClaimNotification(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claiming a wake that does not exist = %v, want ErrNotFound", err)
	}
	if _, err := s.SettleNotification(ctx, second.ID, NotificationSettlement{Outcome: NotificationSent, ExitCode: Known(0)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("settling an unclaimed wake = %v, want ErrNotFound", err)
	}

	settled, err := s.SettleNotification(ctx, first.ID, NotificationSettlement{
		Outcome: NotificationFailed, ExitCode: Known(3), Detail: "posting to https://user:hunter2@example.test/hook failed",
	})
	if err != nil {
		t.Fatalf("settling: %v", err)
	}
	if settled.Outcome != Known(NotificationFailed) || settled.ExitCode != Known(3) || !settled.SettledAt.IsKnown() {
		t.Fatalf("the settled record is %+v, want failed with exit status 3", settled)
	}
	if detail, _ := settled.Detail.Get(); strings.Contains(detail, "hunter2") || !strings.Contains(detail, "failed") {
		t.Fatalf("the stored detail is %q, want the credential removed and the rest kept", detail)
	}
	if _, err := s.SettleNotification(ctx, first.ID, NotificationSettlement{Outcome: NotificationSent, ExitCode: Known(0)}); !errors.Is(err, ErrNotificationSettled) {
		t.Fatalf("a second settlement = %v, want ErrNotificationSettled", err)
	}
	if again, _ := s.Notification(ctx, first.ID); again.Outcome != Known(NotificationFailed) {
		t.Fatalf("after a refused second settlement the record is %+v, want the first outcome", again)
	}
	for _, stmt := range []string{
		`UPDATE notification SET outcome = 'sent', exit_code = 0 WHERE wake_id = ?`,
		`UPDATE notification SET claimed_at = 'x' WHERE wake_id = ?`,
		`DELETE FROM notification WHERE wake_id = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, stmt, first.ID); err == nil {
			t.Errorf("the database accepted %s", stmt)
		}
	}
}

// A settlement outside the closed set is refused before anything is
// written: an outcome this package does not define, and a sent record whose
// exit status is not zero or is not known.
func TestASettlementOutsideTheSetIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "notifytaskab").ID
	w := appendWake(t, s, task, "hold:one")
	if _, err := s.ClaimNotification(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []NotificationSettlement{
		{Outcome: "delivered", ExitCode: Known(0)},
		{Outcome: NotificationSent, ExitCode: Known(1)},
		{Outcome: NotificationSent},
	} {
		if _, err := s.SettleNotification(ctx, w.ID, bad); !errors.Is(err, ErrUnknownNotification) {
			t.Errorf("settling %+v = %v, want ErrUnknownNotification", bad, err)
		}
	}
	if n, err := s.Notification(ctx, w.ID); err != nil || n.Settled() {
		t.Fatalf("after refused settlements the record is %+v, %v; want it still unsettled", n, err)
	}
	if _, err := s.SettleNotification(ctx, w.ID, NotificationSettlement{Outcome: NotificationFailed}); err != nil {
		t.Fatalf("a failure with no exit status of its own is a settlement: %v", err)
	}
}
