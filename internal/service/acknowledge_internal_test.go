package service

import (
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// An acknowledgement naming task wakes and domain wakes is one batch, all
// or nothing: a valid task wake named beside a domain wake no row has is
// refused with the domain wake's refusal and the task wake stays queued,
// so the same batch less the bad row closes it afterwards. Two batches in
// sequence would have closed the task wake before reading the domain wake,
// which this fails on; the positive control is the corrected batch.
func TestAMixedAcknowledgementIsRefusedWholeAndClosesNothing(t *testing.T) {
	t.Parallel()
	sub := newDispatchSubject(t)
	ctx := t.Context()
	inc, err := sub.records.BeginIncarnation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	appended, err := sub.records.AppendWakeWithMarkers(ctx, inc, store.WakeRequest{
		TaskID: sub.task, Kind: string(supervisor.KindTerminalGone), Marker: "mixed-ack",
	}, store.MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	id := appended.Wake.ID
	queued := func() bool {
		t.Helper()
		rows, err := sub.records.TaskWakes(ctx, sub.task)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == id {
				return row.Queued()
			}
		}
		t.Fatalf("wake %d is not among the task's wakes %+v", id, rows)
		return false
	}

	_, err = sub.service.acknowledgeWakes(ctx, ipc.Request{}, machine.AcknowledgeRequest{Wakes: []int64{id}, DomainWakes: []int64{9999}})
	if !errors.Is(err, ipc.ErrInvalidRequest) || !errors.Is(err, store.ErrWakeUnknown) {
		t.Fatalf("the mixed acknowledgement = %v, want ErrInvalidRequest naming the unknown domain wake", err)
	}
	if !queued() {
		t.Fatal("the refused batch closed the valid task wake")
	}
	acknowledged, err := sub.service.acknowledgeWakes(ctx, ipc.Request{}, machine.AcknowledgeRequest{Wakes: []int64{id}})
	if err != nil || len(acknowledged.Wakes) != 1 {
		t.Fatalf("the batch less the bad row = %+v, %v; want the task wake closed", acknowledged, err)
	}
	if queued() {
		t.Fatal("the task wake is still queued after the corrected batch")
	}
}

// A combined drain hands out both shapes or neither: with the domain half
// made to fail inside the store, a coordinator's drain over a queued task
// wake and a queued domain wake is refused, and the task wake reads as
// never handed out, so the next drain under the same attachment offers it
// again rather than leaving it handed out to a caller that was given an
// error. With the fault lifted the same drain hands out both, the positive
// control. Two transactions in sequence would leave the task wake handed
// out here.
func TestACombinedDrainThatFailsHandsOutNothing(t *testing.T) {
	t.Parallel()
	sub := newDispatchSubject(t)
	ctx := t.Context()
	inc, err := sub.records.BeginIncarnation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, err := sub.records.AppendWakeWithMarkers(ctx, inc, store.WakeRequest{
		TaskID: sub.task, Kind: string(supervisor.KindTerminalGone), Marker: "combined",
	}, store.MarkerAdvance{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := sub.records.Delegate(ctx, store.DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub.records.AppendDomainWakeWithMarkers(ctx, inc,
		store.DomainWakeRequest{Delegation: d.ID, Kind: string(supervisor.KindSubordinateGone), Marker: "gone:1"},
		store.DelegationMarkerAdvance{}); err != nil {
		t.Fatal(err)
	}
	handedOut := func() bool {
		t.Helper()
		rows, err := sub.records.TaskWakes(ctx, sub.task)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == task.Wake.ID {
				return row.HandedOutAt.IsKnown()
			}
		}
		t.Fatalf("wake %d is not among the task's wakes", task.Wake.ID)
		return false
	}
	lift, err := sub.records.RefuseDomainDrainsForTest(ctx, t)
	if err != nil {
		t.Fatal(err)
	}
	handout := store.WakeHandout{To: store.AcknowledgedByCoordinator(), Under: "drain-1", Held: []string{"drain-1"}, Route: wakeRouteOf(placedPeer{})}
	if _, err := sub.service.drainAll(ctx, 10, handout); err == nil {
		t.Fatal("the drain succeeded with its domain half failing")
	}
	if handedOut() {
		t.Fatal("the failed drain left the task wake handed out")
	}
	if err := lift(); err != nil {
		t.Fatal(err)
	}
	drained, err := sub.service.drainAll(ctx, 10, handout)
	if err != nil || len(drained.Wakes) != 1 || len(drained.DomainWakes) != 1 {
		t.Fatalf("the drain after the fault = %+v, %v; want one of each shape", drained, err)
	}
	if !handedOut() {
		t.Fatal("the task wake the drain returned is not recorded as handed out")
	}
}
