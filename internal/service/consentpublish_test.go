package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// consentHoldEvent is one hold.state event about a hold of the run under
// test, as a subscriber on the socket read it.
type consentHoldEvent struct {
	revision uint64
	hold     store.Hold
}

// A standing-consent hold is published as hold.state when it is registered,
// open, and again when it is resolved, closed, over the socket a subscriber
// reads. Every hold row consent recorded for the run reaches the stream as
// exactly one opening event followed by exactly one closing event, in that
// order of revision, so the record and the stream agree on every row.
//
// The stopped-between case is the moment after a row is registered and
// before its answer is given, where a service that stopped leaves the row
// open until its next open finishes it. The hook holds the service there and
// reads the stream: the opening event has already arrived, so a subscriber
// attached then learns the durable open hold existed whatever happens to the
// service next. Before registration published, the hook waited out its bound
// with nothing on the stream.
func TestAStandingConsentHoldIsPublishedOpenAndClosed(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := consentingSubject(t, h, allowsConsent)
	agent := reviewWithFindings(t)

	withServiceOptions(t, consentOptions(t, h, agent, mergeableProvider(t), nil), func(running serviceUnderTest) {
		ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
		defer cancel()
		stream, err := running.client.Subscribe(ctx, machine.SubscribeRequest{})
		if err != nil {
			t.Fatalf("subscribing: %v", err)
		}
		defer stream.Close()
		events := make(chan consentHoldEvent, 64)
		go func() {
			defer close(events)
			for {
				event, err := stream.Recv(ctx)
				if err != nil {
					return
				}
				if event.Type != ipc.TypeHoldState {
					continue
				}
				var hold store.Hold
				if err := json.Unmarshal(event.Payload, &hold); err != nil {
					t.Errorf("a hold.state payload does not read as a hold: %v\n%s", err, event.Payload)
					return
				}
				events <- consentHoldEvent{revision: event.Revision, hold: hold}
			}
		}()

		// seen is every hold event read, by the hook or the drain, and mu
		// guards it and stoppedBetween, which the hook writes on the
		// service's goroutine.
		var mu sync.Mutex
		var seen []consentHoldEvent
		// stoppedBetween is whether the opening event had arrived at each
		// registration, read while the service is held before the answer.
		var stoppedBetween []bool
		running.service.SetAfterConsentRegisteredForTest(func() {
			mu.Lock()
			defer mu.Unlock()
			bound := time.After(5 * time.Second)
			for {
				select {
				case e, ok := <-events:
					if !ok {
						stoppedBetween = append(stoppedBetween, false)
						return
					}
					seen = append(seen, e)
					if e.hold.Open() && strings.Contains(e.hold.Key, "/standing-consent/") {
						stoppedBetween = append(stoppedBetween, true)
						return
					}
				case <-bound:
					stoppedBetween = append(stoppedBetween, false)
					return
				}
			}
		})

		run, err := startUnderConsent(t, running.client, subject)
		if err != nil {
			t.Fatalf("starting a run under standing consent: %v", err)
		}
		if run.Outcome != machine.OutcomeChecksPassed {
			t.Fatalf("the run under consent ended %s: %s; want checks-passed", run.Outcome, run.Reason)
		}
		// Every hold event the run caused was published before the call
		// returned; this reads until the stream has been quiet a while.
		mu.Lock()
		defer mu.Unlock()
	drain:
		for {
			select {
			case e, ok := <-events:
				if !ok {
					break drain
				}
				seen = append(seen, e)
			case <-time.After(2 * time.Second):
				break drain
			}
		}

		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		rows := consentAnswers(t, records, run.Record.ID)
		if len(rows) == 0 {
			t.Fatal("standing consent recorded no answer, so this run shows nothing about its publication")
		}
		if len(stoppedBetween) != len(rows) {
			t.Fatalf("the service was held after %d registration(s), want one per recorded row (%d)",
				len(stoppedBetween), len(rows))
		}
		for i, arrived := range stoppedBetween {
			if !arrived {
				t.Errorf("registration %d: no opening hold.state had arrived while the service stood between "+
					"registering the hold and answering it, so a service stopping there leaves a subscriber "+
					"never told the open hold existed", i+1)
			}
		}
		recorded := map[string]bool{}
		for _, row := range rows {
			recorded[row.Key] = true
			if row.Open() {
				t.Errorf("row %s is still open after the run ended", row.Key)
			}
			var opened, closed []consentHoldEvent
			for _, e := range seen {
				switch {
				case e.hold.Key != row.Key:
				case e.hold.Open():
					opened = append(opened, e)
				default:
					closed = append(closed, e)
				}
			}
			if len(opened) != 1 || len(closed) != 1 {
				t.Errorf("row %s: %d opening and %d closing hold.state event(s), want exactly one of each",
					row.Key, len(opened), len(closed))
				continue
			}
			if opened[0].revision >= closed[0].revision {
				t.Errorf("row %s: opened at revision %d, not before its closure at %d",
					row.Key, opened[0].revision, closed[0].revision)
			}
		}
		for _, e := range seen {
			if !recorded[e.hold.Key] {
				t.Errorf("hold.state for %s, which no standing-consent row of run %s names", e.hold.Key, run.Record.ID)
			}
		}
	})
}
