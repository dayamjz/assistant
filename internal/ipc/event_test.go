package ipc_test

import (
	"encoding/json"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
)

// TestClassifyIsWrittenDownHere states the class of every type independently of
// the table the package keeps, so a row edited by accident fails here rather
// than quietly changing what may be discarded.
func TestClassifyIsWrittenDownHere(t *testing.T) {
	expected := map[ipc.Type]ipc.Class{
		"log.line":         ipc.ClassActivity,
		"agent.output":     ipc.ClassActivity,
		"stage.heartbeat":  ipc.ClassActivity,
		"run.state":        ipc.ClassState,
		"stage.state":      ipc.ClassState,
		"run.findings":     ipc.ClassState,
		"hold.state":       ipc.ClassState,
		"task.state":       ipc.ClassState,
		"stream.gap":       ipc.ClassControl,
		"service.stopping": ipc.ClassControl,
	}
	for typ, want := range expected {
		if got := ipc.Classify(typ); got != want {
			t.Errorf("Classify(%q) = %s, want %s", typ, got, want)
		}
	}
	if got, want := len(ipc.Types()), len(expected); got != want {
		t.Errorf("the package classifies %d types, this test states %d; a new type needs a class stated here", got, want)
	}
}

// TestUnrecognizedTypeIsState is the rule that keeps a build from discarding an
// event a newer peer invented.
func TestUnrecognizedTypeIsState(t *testing.T) {
	for _, typ := range []ipc.Type{"", "future.thing", "log.line.v2", "activity"} {
		if got := ipc.Classify(typ); got != ipc.ClassState {
			t.Errorf("Classify(%q) = %s, want state", typ, got)
		}
		if ipc.Classify(typ).Droppable() {
			t.Errorf("Classify(%q) is droppable, so a newer peer's event would be discarded", typ)
		}
	}
}

func TestOnlyActivityIsDroppable(t *testing.T) {
	cases := map[ipc.Class]bool{
		ipc.ClassActivity: true,
		ipc.ClassState:    false,
		ipc.ClassControl:  false,
	}
	for class, want := range cases {
		if got := class.Droppable(); got != want {
			t.Errorf("%s.Droppable() = %v, want %v", class, got, want)
		}
	}
}

func TestGapPayload(t *testing.T) {
	marker := ipc.Event{Type: "stream.gap", Payload: json.RawMessage(`{"dropped":7}`)}
	got, err := ipc.GapPayload(marker)
	if err != nil {
		t.Fatalf("GapPayload: %v", err)
	}
	if got.Dropped != 7 {
		t.Errorf("Dropped = %d, want 7", got.Dropped)
	}
	if _, err := ipc.GapPayload(ipc.Event{Type: "run.state", Revision: 1}); err == nil {
		t.Error("GapPayload read a run.state event as a marker")
	}
}
