package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
)

// seedDelegation delegates a domain and begins its first attempt.
func seedDelegation(t *testing.T, s *Store, domain string) Delegation {
	t.Helper()
	d, err := s.Delegate(context.Background(), DelegationRequest{Domain: domain, Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatalf("Delegate(%s): %v", domain, err)
	}
	if _, err := s.BeginDelegationAttempt(context.Background(), d.ID, 0); err != nil {
		t.Fatal(err)
	}
	return d
}

// A domain wake is the wake table's shape on the delegation: appended with
// the delegation's markers in one transaction under the incarnation fence,
// a replay against a queued row is one wake with the markers advanced, the
// same key against an acknowledged row is refused with nothing written, a
// delegation that does not exist is refused, and the domain reads off the
// delegation's row (P8: the marker row and the wake row are read, never a
// newest-line reading of either).
func TestADomainWakeIsAppendedWithTheDelegationsMarkersUnderTheFence(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	ctx := context.Background()
	s := openStore(t)
	d := seedDelegation(t, s, "alpha")
	since := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	inc := incarnationOf(t, s)
	if _, err := s.AppendDomainWakeWithMarkers(ctx, inc, DomainWakeRequest{Delegation: 99, Kind: "subordinate-gone", Marker: "m"},
		DelegationMarkerAdvance{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a wake about no delegation = %v, want ErrNotFound", err)
	}
	first, err := s.AppendDomainWakeWithMarkers(ctx, inc, DomainWakeRequest{Delegation: d.ID, Kind: "subordinate-gone", Marker: "gone:1"},
		DelegationMarkerAdvance{UnobservedSince: Known(since), GoneSurfacedAt: Known(since)})
	if err != nil || first.Disposition != WakeAppended || first.Wake.Domain != "alpha" || first.Wake.Delegation != d.ID || !first.Wake.Queued() {
		t.Fatalf("the first append = %+v, %v", first, err)
	}
	if got, _ := first.Markers.GoneSurfacedAt.Get(); !got.Equal(since) || first.Markers.Delegation != d.ID {
		t.Fatalf("the markers after the append are %+v, want the surface recorded", first.Markers)
	}
	m, has, err := s.DelegationMarkers(ctx, d.ID)
	if err != nil || !has || m.GoneSurfacedAt != Known(since) {
		t.Fatalf("DelegationMarkers = %+v, %v, %v", m, has, err)
	}
	again, err := s.AppendDomainWakeWithMarkers(ctx, inc, DomainWakeRequest{Delegation: d.ID, Kind: "subordinate-gone", Marker: "gone:1"},
		DelegationMarkerAdvance{UnobservedSince: Known(since)})
	if err != nil || again.Disposition != WakeAlreadyQueued || again.Wake.ID != first.Wake.ID || again.Markers.GoneSurfacedAt.IsKnown() {
		t.Fatalf("the replay = %+v, %v; want already queued with the markers advanced", again, err)
	}
	stale := inc
	inc = incarnationOf(t, s)
	if _, err := s.AppendDomainWakeWithMarkers(ctx, stale, DomainWakeRequest{Delegation: d.ID, Kind: "subordinate-gone", Marker: "gone:2"},
		DelegationMarkerAdvance{}); !errors.Is(err, ErrStaleIncarnation) {
		t.Fatalf("a write by a replaced incarnation = %v, want ErrStaleIncarnation", err)
	}
	if _, err := s.AdvanceDelegationMarkers(ctx, stale, d.ID, DelegationMarkerAdvance{}); !errors.Is(err, ErrStaleIncarnation) {
		t.Fatalf("a marker advance by a replaced incarnation = %v, want ErrStaleIncarnation", err)
	}
	if err := s.AcknowledgeDomainWakes(ctx, []int64{first.Wake.ID}, AcknowledgedByCoordinator()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendDomainWakeWithMarkers(ctx, inc, DomainWakeRequest{Delegation: d.ID, Kind: "subordinate-gone", Marker: "gone:1"},
		DelegationMarkerAdvance{}); !errors.Is(err, ErrWakeHandled) {
		t.Fatalf("the same key against an acknowledged row = %v, want ErrWakeHandled", err)
	}
	wakes, err := s.DelegationWakes(ctx, d.ID)
	if err != nil || len(wakes) != 1 || wakes[0].Queued() || wakes[0].AcknowledgedBy != Known(AcknowledgedByCoordinator()) {
		t.Fatalf("DelegationWakes = %+v, %v", wakes, err)
	}
	cleared, err := s.AdvanceDelegationMarkers(ctx, inc, d.ID, DelegationMarkerAdvance{})
	if err != nil || cleared.UnobservedSince.IsKnown() || cleared.GoneSurfacedAt.IsKnown() {
		t.Fatalf("the cleared markers are %+v, %v", cleared, err)
	}
}

// The second shape drains, is offered and is listed on the wake table's
// terms, numbered apart from it, and reaches the first coordinator alone: a
// drain, a count or a listing on a subordinate's route carries none, so a
// subordinate never receives a wake about itself; an acknowledgement closes
// a row once and refuses an unknown or a closed one with nothing written;
// and the queued count is what the in-flight reading adds.
func TestADomainWakeReachesTheFirstCoordinatorAloneAndClosesOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	d := seedDelegation(t, s, "alpha")
	inc := incarnationOf(t, s)
	var ids []int64
	for _, marker := range []string{"a", "b", "c"} {
		out, err := s.AppendDomainWakeWithMarkers(ctx, inc, DomainWakeRequest{Delegation: d.ID, Kind: "subordinate-gone", Marker: marker}, DelegationMarkerAdvance{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, out.Wake.ID)
	}
	taskRow := seedTask(t, s, "taskdomwakea")
	if out, err := s.AppendWakeWithMarkers(ctx, inc, WakeRequest{TaskID: taskRow.ID, Kind: "terminal-gone", Marker: "x"}, MarkerAdvance{}); err != nil || out.Wake.ID != 1 {
		t.Fatalf("the task wake numbers from its own sequence: %+v, %v", out, err)
	}
	coordinator := WakeHandout{To: AcknowledgedByCoordinator(), Under: "u"}
	subordinate := WakeHandout{To: AcknowledgedBySubordinate("alpha"), Under: "u", Route: WakeRoute{Subordinate: "alpha", Actable: []string{"subordinate-gone"}}}
	if n, err := s.UnacknowledgedDomainWakes(ctx); err != nil || n != 3 {
		t.Fatalf("UnacknowledgedDomainWakes = %d, %v; want 3", n, err)
	}
	if n, err := s.OfferedDomainWakes(ctx, nil, subordinate.Route); err != nil || n != 0 {
		t.Fatalf("OfferedDomainWakes on the subordinate's route = %d, %v; want 0", n, err)
	}
	if got, err := s.DrainDomainWakes(ctx, 10, subordinate); err != nil || len(got) != 0 {
		t.Fatalf("DrainDomainWakes on the subordinate's route = %+v, %v; want none", got, err)
	}
	if n, err := s.OfferedDomainWakes(ctx, nil, coordinator.Route); err != nil || n != 3 {
		t.Fatalf("OfferedDomainWakes on the coordinator's route = %d, %v; want 3", n, err)
	}
	drained, err := s.DrainDomainWakes(ctx, 2, coordinator)
	if err != nil || len(drained) != 2 || drained[0].ID != ids[0] || drained[1].ID != ids[1] || !drained[0].HandedOut() ||
		drained[0].HandedOutTo != Known(AcknowledgedByCoordinator()) || drained[0].Handouts != Known(int64(1)) {
		t.Fatalf("the bounded drain = %+v, %v", drained, err)
	}
	held := WakeHandout{To: AcknowledgedByCoordinator(), Under: "u", Held: []string{"u"}}
	if n, err := s.OfferedDomainWakes(ctx, held.Held, held.Route); err != nil || n != 1 {
		t.Fatalf("OfferedDomainWakes while u is held = %d, %v; want the one not yet handed out", n, err)
	}
	if rest, err := s.DrainDomainWakes(ctx, 10, held); err != nil || len(rest) != 1 || rest[0].ID != ids[2] {
		t.Fatalf("the drain while u is held = %+v, %v; want only the third row", rest, err)
	}
	if listed, err := s.QueuedDomainWakes(ctx, 10); err != nil || len(listed) != 3 {
		t.Fatalf("QueuedDomainWakes = %+v, %v; want all three still queued", listed, err)
	}
	if err := s.AcknowledgeDomainWakes(ctx, []int64{ids[0], 99}, AcknowledgedByCoordinator()); !errors.Is(err, ErrWakeUnknown) {
		t.Fatalf("a batch naming no row = %v, want ErrWakeUnknown", err)
	}
	if listed, _ := s.QueuedDomainWakes(ctx, 10); len(listed) != 3 {
		t.Fatalf("a refused batch closed a row: %+v", listed)
	}
	if err := s.AcknowledgeDomainWakes(ctx, []int64{ids[0]}, Acknowledger{}); !errors.Is(err, ErrNoAcknowledger) {
		t.Fatalf("an acknowledgement by nobody = %v, want ErrNoAcknowledger", err)
	}
	if err := s.AcknowledgeDomainWakes(ctx, []int64{ids[0], ids[1]}, AcknowledgedByProgram()); err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeDomainWakes(ctx, []int64{ids[0]}, AcknowledgedByCoordinator()); !errors.Is(err, ErrWakeAcknowledged) {
		t.Fatalf("acknowledging twice = %v, want ErrWakeAcknowledged", err)
	}
	listed, err := s.QueuedDomainWakes(ctx, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != ids[2] {
		t.Fatalf("after the acknowledgement the queue holds %+v, %v", listed, err)
	}
	if n, _ := s.UnacknowledgedDomainWakes(ctx); n != 1 {
		t.Fatalf("UnacknowledgedDomainWakes = %d, want 1", n)
	}
	remaining := make([]int64, 0, 1)
	for _, w := range listed {
		remaining = append(remaining, w.ID)
	}
	if !slices.Equal(remaining, []int64{ids[2]}) {
		t.Fatalf("remaining = %v", remaining)
	}
	// The seals, against a direct rewrite.
	for _, statement := range []string{
		`UPDATE domain_wake SET kind = 'x' WHERE id = ?`,
		`UPDATE domain_wake SET handled_at = '2026-01-01T00:00:00.000000000Z', acknowledged_by = 'coordinator' WHERE id = ?`,
		`UPDATE domain_wake SET handouts = 5 WHERE id = ?`,
		`DELETE FROM domain_wake WHERE id = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, statement, ids[0]); err == nil {
			t.Errorf("%s was accepted by the database", statement)
		}
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE domain_wake SET handed_out_at = '2026-01-01T00:00:00.000000000Z', handed_out_to = 'coordinator', handed_out_under = 'v', handouts = coalesce(handouts, 0) + 1 WHERE id = ?`, ids[0]); err == nil {
		t.Error("a handout to an acknowledged row was accepted by the database")
	}
}

// The answer member of the closed set: a delegation ended by an answer reads
// back as it, apart from one the coordinator ended.
func TestADelegationEndedByAnAnswerReadsBackAsSuch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	d := seedDelegation(t, s, "beta")
	ended, _, err := s.EndDelegation(ctx, d.ID, EndedByAnswer(), DelegationEndClosure{})
	if err != nil || ended.EndedBy != Known(EndedByAnswer()) || ended.Standing() {
		t.Fatalf("EndDelegation = %+v, %v", ended, err)
	}
	if EndedByAnswer() == EndedByCoordinator() || EndedByAnswer().String() != "answer" {
		t.Fatal("the answer member is not its own")
	}
	if _, err := s.BeginDelegationAttempt(ctx, d.ID, 1); !errors.Is(err, ErrDelegationEnded) {
		t.Fatalf("an attempt after the answer ended the delegation = %v, want ErrDelegationEnded", err)
	}
}

// AcknowledgeAll is one batch over both shapes: a valid task wake beside a
// domain wake no row has is refused and left queued, and the batch less
// the bad row closes it.
func TestAcknowledgeAllRefusesAMixedBatchWhole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedTask(t, s, "abcdefghij23")
	inc, err := s.BeginIncarnation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	appended, err := s.AppendWakeWithMarkers(ctx, inc, WakeRequest{TaskID: "abcdefghij23", Kind: "terminal-gone", Marker: "m"}, MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	id := appended.Wake.ID
	if err := s.AcknowledgeAll(ctx, []int64{id}, []int64{99}, nil, AcknowledgedByCoordinator()); !errors.Is(err, ErrWakeUnknown) {
		t.Fatalf("the mixed batch = %v, want ErrWakeUnknown", err)
	}
	if rows, err := s.TaskWakes(ctx, "abcdefghij23"); err != nil || len(rows) != 1 || !rows[0].Queued() {
		t.Fatalf("after the refusal the task's wakes are %+v (%v), want the one still queued", rows, err)
	}
	if err := s.AcknowledgeAll(ctx, nil, nil, nil, AcknowledgedByCoordinator()); err == nil {
		t.Fatal("an empty batch was accepted")
	}
	if err := s.AcknowledgeAll(ctx, []int64{id}, nil, nil, AcknowledgedByCoordinator()); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.TaskWakes(ctx, "abcdefghij23"); err != nil || rows[0].Queued() {
		t.Fatalf("after the corrected batch the task's wakes are %+v (%v), want it closed", rows, err)
	}
}

// A domain wake's notification record is the task wake's on the second
// shape: pending until claimed, claimed once, settled once, refused once
// acknowledged, sealed against a delete, and kept apart from the task
// wake's record of the same identifier.
func TestADomainWakesNotificationIsClaimedAndSettledOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	d, err := s.Delegate(ctx, DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatal(err)
	}
	inc := incarnationOf(t, s)
	var ids []int64
	for _, marker := range []string{"gone:1", "gone:2"} {
		w, err := s.AppendDomainWakeWithMarkers(ctx, inc, DomainWakeRequest{Delegation: d.ID, Kind: "subordinate-gone", Marker: marker}, DelegationMarkerAdvance{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, w.Wake.ID)
	}
	pending, err := s.PendingDomainNotifications(ctx, 10)
	if err != nil || len(pending) != 2 || pending[0].ID != ids[0] || pending[0].Domain != "alpha" {
		t.Fatalf("PendingDomainNotifications = %+v, %v; want both, oldest first", pending, err)
	}
	if _, err := s.ClaimDomainNotification(ctx, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDomainNotification(ctx, ids[0]); !errors.Is(err, ErrNotificationClaimed) {
		t.Fatalf("a second claim = %v, want ErrNotificationClaimed", err)
	}
	if _, err := s.Notification(ctx, ids[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the task wake record of the same identifier = %v, want none", err)
	}
	n, err := s.SettleDomainNotification(ctx, ids[0], NotificationSettlement{Outcome: NotificationSent, ExitCode: Known(0)})
	if err != nil || !n.Settled() || n.WakeID != ids[0] {
		t.Fatalf("SettleDomainNotification = %+v, %v", n, err)
	}
	if _, err := s.SettleDomainNotification(ctx, ids[0], NotificationSettlement{Outcome: NotificationFailed}); !errors.Is(err, ErrNotificationSettled) {
		t.Fatalf("a second settlement = %v, want ErrNotificationSettled", err)
	}
	if err := s.AcknowledgeDomainWakes(ctx, []int64{ids[1]}, AcknowledgedByCoordinator()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDomainNotification(ctx, ids[1]); !errors.Is(err, ErrNotificationNotOffered) {
		t.Fatalf("claiming an acknowledged domain wake = %v, want ErrNotificationNotOffered", err)
	}
	if pending, err := s.PendingDomainNotifications(ctx, 10); err != nil || len(pending) != 0 {
		t.Fatalf("after the claim and the acknowledgement the pending are %+v, %v; want none", pending, err)
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM domain_notification`); err == nil {
		t.Fatal("a domain notification record was deleted")
	}
}
