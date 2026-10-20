package service

import (
	"errors"
	"testing"

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
