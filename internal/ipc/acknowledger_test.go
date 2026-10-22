package ipc_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/store"
)

// A wake acknowledged through this protocol is recorded as the
// coordinator's, whatever the request says: the derivation reads nothing out
// of the request, there is no frame field for who acknowledged, and a body
// that carries one is decoded into a shape that has none. The row read back
// is the store's own, over a wake a real incarnation appended, so what is
// asserted is the record and not the value the handler carried.
func TestWakeAcknowledgerIsDerivedFromTheSurfaceAndNeverRead(t *testing.T) {
	records := recordingStore(t)
	ctx := context.Background()
	if _, err := records.CreateTask(ctx, store.Task{ID: "abcdefghijkl", Shape: store.TaskInvestigation, Project: "one", Mode: "m", WorktreePath: "/w"}, store.Disposition{}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	inc, err := records.BeginIncarnation(ctx)
	if err != nil {
		t.Fatalf("BeginIncarnation: %v", err)
	}
	appended, err := records.AppendWakeWithMarkers(ctx, inc, store.WakeRequest{TaskID: "abcdefghijkl", Kind: "k", Marker: "m1"}, store.MarkerAdvance{})
	if err != nil {
		t.Fatalf("AppendWakeWithMarkers: %v", err)
	}

	claims := []ipc.Request{
		{Method: ipc.MethodWakesAcknowledge},
		{Method: ipc.MethodWakesAcknowledge, Params: json.RawMessage(`{"acknowledged_by":"person","by":"program"}`)},
		{Method: ipc.MethodWakesList, Peer: ipc.Peer{}},
	}
	for _, req := range claims {
		if got := req.WakeAcknowledger(); got != store.AcknowledgedByCoordinator() {
			t.Fatalf("a request with method %q and body %s derives %s, want the coordinator", req.Method, req.Params, got)
		}
	}
	if err := records.AcknowledgeWakes(ctx, []int64{appended.Wake.ID}, claims[1].WakeAcknowledger()); err != nil {
		t.Fatalf("AcknowledgeWakes: %v", err)
	}
	wakes, err := records.TaskWakes(ctx, "abcdefghijkl")
	if err != nil || len(wakes) != 1 {
		t.Fatalf("TaskWakes = %+v, %v", wakes, err)
	}
	by, known := wakes[0].AcknowledgedBy.Get()
	if !known || by != store.AcknowledgedByCoordinator() {
		t.Fatalf("the row says %s acknowledged (known %v), want the coordinator", by, known)
	}
	// The control: the zero value the derivation never produces is what the
	// store refuses, so a surface that stopped deriving would be refused
	// there rather than recorded as nobody.
	if err := records.AcknowledgeWakes(ctx, []int64{appended.Wake.ID}, store.Acknowledger{}); !errors.Is(err, store.ErrNoAcknowledger) {
		t.Fatalf("acknowledging as nobody = %v, want ErrNoAcknowledger", err)
	}
}

// A message sent through this protocol is recorded as the coordinator's,
// whatever the request says, on WakeAcknowledger's terms: the record read
// back is the store's own, and the zero sender the derivation never produces
// is what the store refuses, the control that a surface which stopped
// deriving would be refused rather than recorded as nobody.
func TestMessageSenderIsDerivedFromTheSurfaceAndNeverRead(t *testing.T) {
	records := recordingStore(t)
	ctx := context.Background()
	if _, err := records.CreateTask(ctx, store.Task{ID: "abcdefghijkm", Shape: store.TaskInvestigation, Project: "one", Mode: "m", WorktreePath: "/w"}, store.Disposition{}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	attempt, err := records.BeginFirstTaskAttempt(ctx, "abcdefghijkm", store.OnThisMachine())
	if err != nil {
		t.Fatalf("BeginTaskAttempt: %v", err)
	}
	claims := []ipc.Request{
		{Method: ipc.MethodTaskMessage},
		{Method: ipc.MethodTaskMessage, Params: json.RawMessage(`{"sent_by":"person","by":"person"}`)},
	}
	for _, req := range claims {
		if got := req.MessageSender(); got != store.SentByCoordinator() {
			t.Fatalf("a request with body %s derives %s, want the coordinator", req.Params, got)
		}
	}
	m, err := records.AppendMessage(ctx, "abcdefghijkm", attempt.Sequence, "stay on scope", claims[1].MessageSender())
	if err != nil || m.SentBy != store.SentByCoordinator() {
		t.Fatalf("the record says %s sent it (%v), want the coordinator", m.SentBy, err)
	}
	if _, err := records.AppendMessage(ctx, "abcdefghijkm", attempt.Sequence, "x", store.Sender{}); !errors.Is(err, store.ErrNoSender) {
		t.Fatalf("a message from nobody = %v, want ErrNoSender", err)
	}
}
