package ipc_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
)

// activity, state, and control build the three classes of event with a payload
// that names the caller's own label, so a test can say which event it got back
// without asking the package anything.
func activity(label string) ipc.Event {
	return ipc.Event{Type: "log.line", Payload: json.RawMessage(`{"label":"` + label + `"}`)}
}

func state(label string, revision uint64) ipc.Event {
	return ipc.Event{Type: "run.state", Revision: revision, Payload: json.RawMessage(`{"label":"` + label + `"}`)}
}

func control(label string) ipc.Event {
	return ipc.Event{Type: "service.stopping", Payload: json.RawMessage(`{"label":"` + label + `"}`)}
}

func label(t *testing.T, e ipc.Event) string {
	t.Helper()
	var body struct {
		Label string `json:"label"`
	}
	if err := json.Unmarshal(e.Payload, &body); err != nil {
		t.Fatalf("event %q has no label: %v", e.Type, err)
	}
	return body.Label
}

// drain reads exactly n events, failing if the stream ends or stalls first.
func drain(t *testing.T, sub *ipc.Subscription, n int) []ipc.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := make([]ipc.Event, 0, n)
	for len(out) < n {
		e, err := sub.Recv(ctx)
		if err != nil {
			t.Fatalf("after %v, Recv: %v", types(out), err)
		}
		out = append(out, e)
	}
	return out
}

// exhausted asserts the stream has nothing more waiting.
func exhausted(t *testing.T, sub *ipc.Subscription) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	e, err := sub.Recv(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stream still holds %q (err %v), want nothing more", e.Type, err)
	}
}

func subscribe(t *testing.T, p *ipc.Publisher, backlog int) *ipc.Subscription {
	t.Helper()
	sub, err := p.Subscribe(backlog)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { sub.Close() })
	return sub
}

func publish(t *testing.T, p *ipc.Publisher, events ...ipc.Event) {
	t.Helper()
	for _, e := range events {
		if err := p.Publish(e); err != nil {
			t.Fatalf("Publish(%q): %v", e.Type, err)
		}
	}
}

// TestSubscriberThatNeverReadsCannotBlockThePublisher is the first acceptance
// criterion. The subscriber is not slow, it is absent: nothing ever calls Recv.
func TestSubscriberThatNeverReadsCannotBlockThePublisher(t *testing.T) {
	p := ipc.NewPublisher()
	subscribe(t, p, 4) // never read from

	const published = 20000
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < published; i++ {
			if err := p.Publish(activity("line")); err != nil {
				t.Errorf("Publish: %v", err)
				return
			}
			if err := p.Publish(state("run", uint64(i+1))); err != nil {
				t.Errorf("Publish: %v", err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("publishing stalled behind a subscriber that never read")
	}
}

// TestOneSlowSubscriberDoesNotGapAnother proves the queues are per subscriber:
// the absent one gaps itself and the attentive one misses nothing.
func TestOneSlowSubscriberDoesNotGapAnother(t *testing.T) {
	p := ipc.NewPublisher()
	absent := subscribe(t, p, 2)
	attentive := subscribe(t, p, 64)

	if first := drain(t, attentive, 1); len(first) != 1 || first[0].Type != "stream.gap" {
		t.Fatalf("first delivery = %v, want the opening gap marker", first)
	}
	for i := 1; i <= 20; i++ {
		publish(t, p, state("run", uint64(i)))
	}
	got := drain(t, attentive, 20)
	if len(got) != 20 {
		t.Fatalf("attentive subscriber received %d events, want 20", len(got))
	}
	for i, e := range got {
		if e.Revision != uint64(i+1) {
			t.Fatalf("event %d has revision %d, want %d", i, e.Revision, i+1)
		}
	}
	if !absent.Gapped() {
		t.Error("the absent subscriber was not gapped, so its queue silently held everything")
	}
}

// TestSubscriptionOpensGapped is the acceptance criterion that a consumer
// cannot apply a delta to state it never reconciled.
func TestSubscriptionOpensGapped(t *testing.T) {
	p := ipc.NewPublisher()
	sub := subscribe(t, p, 8)
	publish(t, p, state("run", 1))

	got := drain(t, sub, 2)
	if len(got) != 2 {
		t.Fatalf("received %d events, want 2", len(got))
	}
	if got[0].Type != "stream.gap" {
		t.Fatalf("first delivery is %q, want stream.gap", got[0].Type)
	}
	gap, err := ipc.GapPayload(got[0])
	if err != nil {
		t.Fatalf("GapPayload: %v", err)
	}
	if gap.Dropped != 0 {
		t.Errorf("the opening marker reports %d dropped, want 0", gap.Dropped)
	}
	if got[1].Revision != 1 {
		t.Errorf("second delivery has revision %d, want the published 1", got[1].Revision)
	}
}

// TestOverflowDropsOnlyActivity is the second acceptance criterion: every state
// event survives, exactly one marker is delivered, and it arrives first.
func TestOverflowDropsOnlyActivity(t *testing.T) {
	p := ipc.NewPublisher()
	sub := subscribe(t, p, 4)
	if opening := drain(t, sub, 1); len(opening) != 1 || opening[0].Type != "stream.gap" {
		t.Fatalf("opening delivery = %v, want the gap marker", opening)
	}

	publish(t, p,
		state("s1", 1), activity("a1"), activity("a2"), state("s2", 2), // fills the queue
		activity("a3"), activity("a4"), activity("a5"), // overflow, three times
	)

	got := drain(t, sub, 5)
	exhausted(t, sub)
	if got[0].Type != "stream.gap" {
		t.Fatalf("delivery order is %v; the marker must arrive ahead of queued payload", types(got))
	}
	gap, err := ipc.GapPayload(got[0])
	if err != nil {
		t.Fatalf("GapPayload: %v", err)
	}
	if gap.Dropped != 3 {
		t.Errorf("the marker reports %d dropped, want the 3 discarded activity events", gap.Dropped)
	}
	markers := 0
	labels := map[string]bool{}
	for _, e := range got {
		if e.Type == "stream.gap" {
			markers++
			continue
		}
		labels[label(t, e)] = true
	}
	if markers != 1 {
		t.Errorf("received %d gap markers, want exactly one sticky marker", markers)
	}
	for _, want := range []string{"s1", "s2"} {
		if !labels[want] {
			t.Errorf("state event %q was discarded; only activity may be dropped", want)
		}
	}
}

// TestArrivingActivityNeverDisplacesADelta covers the case the eviction order
// exists for: a queue with no activity in it must not lose state to make room
// for a log line.
func TestArrivingActivityNeverDisplacesADelta(t *testing.T) {
	p := ipc.NewPublisher()
	sub := subscribe(t, p, 2)
	drain(t, sub, 1)

	publish(t, p, state("s1", 1), state("s2", 2), activity("a1"))

	got := drain(t, sub, 3)
	exhausted(t, sub)
	labels := map[string]bool{}
	for _, e := range got {
		if e.Type == "stream.gap" {
			continue
		}
		labels[label(t, e)] = true
	}
	if !labels["s1"] || !labels["s2"] {
		t.Errorf("delivered %v, want both state events retained", types(got))
	}
	if labels["a1"] {
		t.Error("the arriving activity event displaced a delta")
	}
}

// TestStateCollapsesIntoTheMarkerWhenNothingElseCanGo covers the tier below
// activity: state may be discarded only because the marker forces the consumer
// to read the state back in full, which supersedes whatever was queued.
func TestStateCollapsesIntoTheMarkerWhenNothingElseCanGo(t *testing.T) {
	p := ipc.NewPublisher()
	sub := subscribe(t, p, 2)
	drain(t, sub, 1)

	publish(t, p, state("s1", 1), state("s2", 2), state("s3", 3))

	got := drain(t, sub, 3)
	exhausted(t, sub)
	if got[0].Type != "stream.gap" {
		t.Fatalf("delivered %v, want a marker followed by two events", types(got))
	}
	gap, err := ipc.GapPayload(got[0])
	if err != nil {
		t.Fatalf("GapPayload: %v", err)
	}
	if gap.Dropped != 1 {
		t.Errorf("the marker reports %d dropped, want 1", gap.Dropped)
	}
	if l := label(t, got[1]); l != "s2" {
		t.Errorf("oldest retained delta is %q, want s2", l)
	}
	if l := label(t, got[2]); l != "s3" {
		t.Errorf("newest retained delta is %q, want s3", l)
	}
}

// TestControlIsNeverDiscarded is the bottom of the policy. A queue holding only
// control events has nothing that may go, so the stream ends loudly instead of
// discarding one or growing without bound.
func TestControlIsNeverDiscarded(t *testing.T) {
	p := ipc.NewPublisher()
	sub := subscribe(t, p, 2)
	drain(t, sub, 1)

	publish(t, p, control("c1"), control("c2"), state("s1", 1))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []ipc.Event
	var err error
	for {
		var e ipc.Event
		e, err = sub.Recv(ctx)
		if err != nil {
			break
		}
		got = append(got, e)
	}
	if !errors.Is(err, ipc.ErrSubscriberStalled) {
		t.Fatalf("stream ended with %v, want ErrSubscriberStalled", err)
	}
	if len(got) != 3 || got[0].Type != "stream.gap" {
		t.Fatalf("delivered %v, want the marker and both control events", types(got))
	}
	if l := label(t, got[1]); l != "c1" {
		t.Errorf("first control event is %q, want c1", l)
	}
	if l := label(t, got[2]); l != "c2" {
		t.Errorf("second control event is %q, want c2", l)
	}
}

// TestAnUnrecognizedTypeIsRetainedUnderOverflow is the third acceptance
// criterion, exercised where the answer actually differs: a full queue holding
// no activity. An event this build classified as activity would be discarded on
// arrival here, so the unrecognized event survives only because it is treated
// as state.
func TestAnUnrecognizedTypeIsRetainedUnderOverflow(t *testing.T) {
	p := ipc.NewPublisher()
	sub := subscribe(t, p, 2)
	drain(t, sub, 1)

	publish(t, p, state("s1", 1), state("s2", 2))
	future := ipc.Event{Type: "future.thing", Revision: 9, Payload: json.RawMessage(`{"label":"future"}`)}
	publish(t, p, future)

	got := drain(t, sub, 3)
	exhausted(t, sub)
	found := false
	for _, e := range got {
		if e.Type == "future.thing" {
			found = true
			if l := label(t, e); l != "future" {
				t.Errorf("the retained event carries %q, want its own payload", l)
			}
		}
	}
	if !found {
		t.Fatalf("delivered %v, want the unrecognized event retained", types(got))
	}
}

func TestPublishRefusesWhatCannotBeDelivered(t *testing.T) {
	p := ipc.NewPublisher()
	subscribe(t, p, 4)

	cases := map[string]ipc.Event{
		"no type":                {Payload: json.RawMessage(`{}`)},
		"state with no revision": {Type: "run.state"},
		"unknown with no revision because unknown is state": {Type: "future.thing"},
		"a gap marker, which the stream produces":           {Type: "stream.gap"},
	}
	for name, e := range cases {
		if err := p.Publish(e); err == nil {
			t.Errorf("Publish(%s) was accepted", name)
		}
	}
	if err := p.Publish(ipc.Event{Type: "log.line"}); err != nil {
		t.Errorf("Publish(activity with no revision): %v", err)
	}
	if err := p.Publish(ipc.Event{Type: "service.stopping"}); err != nil {
		t.Errorf("Publish(control with no revision): %v", err)
	}
}

func TestClosePublisherEndsSubscriptionsAfterTheQueue(t *testing.T) {
	p := ipc.NewPublisher()
	sub := subscribe(t, p, 8)
	drain(t, sub, 1)
	publish(t, p, state("s1", 1))
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e, err := sub.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv after Close: %v, want the queued event", err)
	}
	if l := label(t, e); l != "s1" {
		t.Errorf("received %q, want the queued s1", l)
	}
	if _, err := sub.Recv(ctx); !errors.Is(err, ipc.ErrStreamClosed) {
		t.Errorf("Recv after the queue drained = %v, want ErrStreamClosed", err)
	}
	if _, err := p.Subscribe(4); !errors.Is(err, ipc.ErrStreamClosed) {
		t.Errorf("Subscribe after Close = %v, want ErrStreamClosed", err)
	}
}

func TestCloseSubscriptionDetachesIt(t *testing.T) {
	p := ipc.NewPublisher()
	sub, err := p.Subscribe(4)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if got := p.Subscribers(); got != 1 {
		t.Fatalf("Subscribers = %d, want 1", got)
	}
	sub.Close()
	if got := p.Subscribers(); got != 0 {
		t.Errorf("Subscribers after Close = %d, want 0", got)
	}
	publish(t, p, state("s1", 1))
}

func TestSubscribeRefusesANegativeBacklog(t *testing.T) {
	p := ipc.NewPublisher()
	if _, err := p.Subscribe(-1); err == nil {
		t.Error("Subscribe(-1) was accepted")
	}
}

func TestRecvRespectsContext(t *testing.T) {
	p := ipc.NewPublisher()
	sub := subscribe(t, p, 4)
	drain(t, sub, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sub.Recv(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Recv with a cancelled context = %v, want context.Canceled", err)
	}
}

func types(events []ipc.Event) []ipc.Type {
	out := make([]ipc.Type, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}
