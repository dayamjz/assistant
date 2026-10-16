package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
)

// The one hold closure the program makes, the checks hold resolved on a
// re-read of the run's pull request, is published as hold.state over the
// socket as the record then stands: resolved, by the program, under the key
// the service files it under. The run's own state travels ahead of it, so a
// consumer sees the run end and then the hold close, in that order of
// revision.
func TestTheChecksHoldClosureIsPublished(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	c := newChecksHoldSubject(t)

	withServiceOptions(t, c.options(t, false), func(running serviceUnderTest) {
		runID := startAtChecksHold(t, c, running.client).Record.ID
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		stream, err := running.client.Subscribe(ctx, machine.SubscribeRequest{})
		if err != nil {
			t.Fatalf("subscribing: %v", err)
		}
		defer stream.Close()
		c.provider.set(forge.PullRequestStateClosed)

		run := answer(t, running.client, runID, string(pipeline.OutcomeApproved))
		if run.Outcome != machine.OutcomeFailed {
			t.Fatalf("answering the hold of a closed pull request answered %s, want failed", run.Outcome)
		}

		var runSeen uint64
		for {
			event, err := stream.Recv(ctx)
			if err != nil {
				t.Fatalf("waiting for the hold's closure on the stream: %v", err)
			}
			switch event.Type {
			case ipc.TypeRunState:
				var record store.Run
				if err := json.Unmarshal(event.Payload, &record); err != nil {
					t.Fatal(err)
				}
				if record.ID == runID && record.Status == store.RunFailed {
					runSeen = event.Revision
				}
			case ipc.TypeHoldState:
				var hold store.Hold
				if err := json.Unmarshal(event.Payload, &hold); err != nil {
					t.Fatalf("a hold.state payload does not read as a hold: %v\n%s", err, event.Payload)
				}
				if hold.Key != service.ChecksHoldKey(runID) {
					t.Fatalf("hold.state for %q, want the checks hold of run %s", hold.Key, runID)
				}
				if hold.Open() {
					t.Fatalf("the checks hold was published open after its closure: %+v", hold)
				}
				// Who resolved it does not travel: store.Resolver decodes
				// from no text, by internal/store's design, so the wire
				// carries the resolution and understates the resolver.
				// What is read back is the record's own resolution text.
				if resolution, known := hold.Resolution.Get(); !known || !strings.Contains(resolution, "read as closed") {
					t.Fatalf("the published hold's resolution is %v, want the state read", hold.Resolution)
				}
				if _, known := hold.ResolvedBy.Get(); known && hold.ResolvedBy.Or(store.Resolver{}) == store.ResolvedByProgram() {
					t.Fatal("a resolver decoded off the wire, which internal/store says no text decodes into")
				}
				if runSeen == 0 || event.Revision <= runSeen {
					t.Fatalf("the hold's closure (revision %d) did not follow the run's end (revision %d)", event.Revision, runSeen)
				}
				return
			}
		}
	})
}
