package ipc_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dayamjz/assistant/internal/ipc"
)

// ExampleCursor shows the shape every consumer of the stream has: reconcile
// when told to, apply a delta only when it is newer, and treat activity as
// something to show rather than something to hold.
func ExampleCursor() {
	events := ipc.NewPublisher()
	sub, err := events.Subscribe(8)
	if err != nil {
		panic(err)
	}
	defer sub.Close()

	// The producer reports work. Publishing does not wait for anyone.
	must(events.Publish(ipc.Event{Type: ipc.TypeLogLine, Payload: json.RawMessage(`"running tests"`)}))
	must(events.Publish(ipc.Event{Type: ipc.TypeRunState, Revision: 7, Payload: json.RawMessage(`{"status":"testing"}`)}))
	must(events.Publish(ipc.Event{Type: ipc.TypeRunState, Revision: 6, Payload: json.RawMessage(`{"status":"rebasing"}`)}))
	events.Close()

	cursor := ipc.NewCursor()
	ctx := context.Background()
	for {
		e, err := sub.Recv(ctx)
		if err != nil {
			break
		}
		switch cursor.Observe(e) {
		case ipc.DispositionReconcile:
			// Read the state in full, then tell the cursor where that read
			// left it. Here the read is stubbed at revision 5.
			cursor.Reconciled(5)
			fmt.Printf("%s: reconciled to revision %d\n", e.Type, cursor.Revision())
		case ipc.DispositionApply:
			fmt.Printf("%s: applied revision %d\n", e.Type, e.Revision)
		case ipc.DispositionIgnore:
			fmt.Printf("%s: ignored revision %d, already at %d\n", e.Type, e.Revision, cursor.Revision())
		}
	}
	// Output:
	// stream.gap: reconciled to revision 5
	// log.line: applied revision 0
	// run.state: applied revision 7
	// run.state: ignored revision 6, already at 7
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// ExampleClassify shows why an event type this build does not know is retained:
// it is state, and state is never discarded without a marker.
func ExampleClassify() {
	fmt.Println(ipc.Classify(ipc.TypeLogLine))
	fmt.Println(ipc.Classify(ipc.TypeRunState))
	fmt.Println(ipc.Classify(ipc.TypeGap))
	fmt.Println(ipc.Classify("custody.state"))
	// Output:
	// activity
	// state
	// control
	// state
}
