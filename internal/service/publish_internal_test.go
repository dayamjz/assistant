package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/store"
)

// Storing an investigation's report registers a hold per ask finding, and
// each is published as hold.state through the service's own publisher, open,
// with the key the store returned. The publisher is the one Open built, so
// what is read here is what a subscriber on the socket is pumped; the
// socket itself is internal/cli's watch tests' subject.
func TestStoringAReportPublishesEachHoldItRegistered(t *testing.T) {
	h, records := loopHome(t)
	ctx := t.Context()
	copyPath, _ := h.TaskCopy("reportedaaaa")
	if _, err := records.CreateTask(ctx, store.Task{
		ID: "reportedaaaa", Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: copyPath,
	}, store.Disposition{}); err != nil {
		t.Fatalf("recording the task: %v", err)
	}
	l := openLoop(t, h, records, nil, nil)
	defer func() { _ = l.service.Close() }()
	sub, err := l.service.events.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	stored, err := l.service.storeTaskReport(ctx, "reportedaaaa",
		`{"summary":"looked","findings":[{"action":"ask","description":"should this be kept?"},{"action":"ask","description":"and this?"}]}`)
	if err != nil {
		t.Fatalf("storeTaskReport: %v", err)
	}
	if len(stored.Holds) != 2 {
		t.Fatalf("the report registered %d holds, want two", len(stored.Holds))
	}
	want := map[string]bool{}
	for _, h := range stored.Holds {
		want[h.Key] = true
	}
	bounded, cancel := context.WithTimeout(ctx, loopWait)
	defer cancel()
	var last uint64
	for len(want) > 0 {
		event, err := sub.Recv(bounded)
		if err != nil {
			t.Fatalf("waiting for %v: %v", want, err)
		}
		if event.Type != ipc.TypeHoldState {
			continue
		}
		var hold store.Hold
		if err := json.Unmarshal(event.Payload, &hold); err != nil {
			t.Fatalf("a hold.state payload does not read as a hold: %v\n%s", err, event.Payload)
		}
		if !want[hold.Key] {
			t.Fatalf("hold.state for %q, which the report did not register", hold.Key)
		}
		if !hold.Open() {
			t.Fatalf("hold %s was published resolved: %+v", hold.Key, hold)
		}
		if event.Revision <= last {
			t.Fatalf("hold.state revisions do not increase: %d after %d", event.Revision, last)
		}
		last = event.Revision
		delete(want, hold.Key)
	}
	// The hold events are state, so they are the class a bounded stream
	// retains, which is the stream's own promise rather than this
	// package's; it is read off the class table here so a reclassification
	// fails where the publisher is.
	if ipc.Classify(ipc.TypeHoldState) != ipc.ClassState || ipc.Classify(ipc.TypeTaskState) != ipc.ClassState {
		t.Fatal("hold.state or task.state is no longer state, so an overflowing stream may drop it")
	}
}
