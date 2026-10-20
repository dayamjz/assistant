package ipc_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
)

// gatedHandler decides a restricted stream only when a test lets it: it says
// it was asked, waits for the test, and answers with err. It is what lets a
// test publish while the decision is still open.
type gatedHandler struct {
	asked   chan ipc.Method
	release chan struct{}
	err     error
}

func (h *gatedHandler) Serve(ctx context.Context, req ipc.Request) (json.RawMessage, error) {
	h.asked <- req.Method
	select {
	case <-h.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, h.err
}

// wakeEvent is a pushed wake as a test publishes it.
func wakeEvent(revision uint64) ipc.Event {
	return ipc.Event{Type: ipc.TypeWakeQueued, Revision: revision, Payload: json.RawMessage(`{"id":1}`)}
}

// A restricted stream is decided by the access check and then by the handler
// before the caller is told it is open, and an event published while that
// decision is open never reaches a caller the handler refuses. The
// subscription is open at the publisher while the handler is deciding, which
// is what shows the event had somewhere to wait, and it is gone once the
// refusal is answered, so a refused caller costs nothing afterwards.
func TestARestrictedStreamRefusedByItsHandlerDeliversNothing(t *testing.T) {
	requiresIdentifiedPeer(t)
	wakes := publisher(t, ipc.PublisherConfig{})
	refusal := errors.New("this caller is a task's worker")
	handler := &gatedHandler{asked: make(chan ipc.Method, 1), release: make(chan struct{}), err: refusal}
	h := serveOnSocket(t, func(sc *ipc.ServerConfig) {
		sc.Handler = handler
		sc.Streams = map[ipc.Method]*ipc.Publisher{ipc.MethodWakesSubscribe: wakes}
	})
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	opened := make(chan error, 1)
	go func() {
		stream, err := c.SubscribeTo(ctx, ipc.MethodWakesSubscribe, nil)
		if err == nil {
			_, err = stream.Recv(ctx)
			stream.Close()
			err = errors.Join(errors.New("a refused stream was opened and delivered"), err)
		}
		opened <- err
	}()
	select {
	case method := <-handler.asked:
		if method != ipc.MethodWakesSubscribe {
			t.Fatalf("the handler was asked about %q, want %q", method, ipc.MethodWakesSubscribe)
		}
	case <-ctx.Done():
		t.Fatal("the handler was never asked whether the stream may open")
	}
	if n := wakes.Subscribers(); n != 1 {
		t.Fatalf("while the decision is open the publisher holds %d subscriptions, want 1", n)
	}
	publish(t, wakes, wakeEvent(7))
	close(handler.release)

	err := <-opened
	var remote *ipc.Error
	if !errors.As(err, &remote) || errors.Is(err, ipc.ErrStreamClosed) {
		t.Fatalf("SubscribeTo = %v, want the handler's refusal", err)
	}
	if len(h.ancestry.questions()) != 1 {
		t.Errorf("the ancestry was asked %d times, want once, before the handler", len(h.ancestry.questions()))
	}
	deadline := time.Now().Add(5 * time.Second)
	for wakes.Subscribers() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the refused subscription is still held: %d", wakes.Subscribers())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A contained caller is refused a restricted stream by the access check, and
// the handler is never asked.
func TestARestrictedStreamRefusesAContainedCaller(t *testing.T) {
	requiresIdentifiedPeer(t)
	wakes := publisher(t, ipc.PublisherConfig{})
	h := serveOnSocket(t, func(sc *ipc.ServerConfig) {
		sc.Streams = map[ipc.Method]*ipc.Publisher{ipc.MethodWakesSubscribe: wakes}
	})
	h.ancestry.result = ipc.Containment{Contained: true, Run: "r7", Stage: "test"}
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	if _, err := c.SubscribeTo(ctx, ipc.MethodWakesSubscribe, nil); !errors.Is(err, ipc.ErrContained) {
		t.Fatalf("SubscribeTo from a contained caller = %v, want ErrContained", err)
	}
	if h.handler.count() != 0 {
		t.Error("a contained caller reached the handler")
	}
}

// An admitted restricted stream opens gapped, like every stream, and then
// carries what its own publisher publishes and nothing the open event stream
// does. The positive control for the refusal above: the same path with a
// handler that admits delivers.
func TestAnAdmittedRestrictedStreamCarriesOnlyItsOwnPublisher(t *testing.T) {
	requiresIdentifiedPeer(t)
	wakes := publisher(t, ipc.PublisherConfig{})
	h := serveOnSocket(t, func(sc *ipc.ServerConfig) {
		sc.Streams = map[ipc.Method]*ipc.Publisher{ipc.MethodWakesSubscribe: wakes}
	})
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	stream, err := c.SubscribeTo(ctx, ipc.MethodWakesSubscribe, nil)
	if err != nil {
		t.Fatalf("SubscribeTo: %v", err)
	}
	defer stream.Close()
	if h.handler.count() != 1 {
		t.Fatalf("the handler was asked %d times, want once", h.handler.count())
	}
	if first, err := stream.Recv(ctx); err != nil || first.Type != ipc.TypeGap {
		t.Fatalf("first delivery = %+v (%v), want the opening gap marker", first, err)
	}
	publish(t, h.events, state("s1", 4))
	publish(t, wakes, wakeEvent(9))
	got, err := stream.Recv(ctx)
	if err != nil || got.Type != ipc.TypeWakeQueued || got.Revision != 9 {
		t.Fatalf("delivery = %+v (%v), want the pushed wake at revision 9 and nothing from the open stream", got, err)
	}
}

// A streaming method nothing publishes is refused rather than opened on a
// stream that would never deliver, and a method that answers once cannot be
// subscribed to.
func TestAStreamNothingPublishesIsRefused(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := serveOnSocket(t, nil)
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	if _, err := c.SubscribeTo(ctx, ipc.MethodWakesSubscribe, nil); !errors.Is(err, ipc.ErrUnavailable) {
		t.Fatalf("SubscribeTo with no publisher = %v, want ErrUnavailable", err)
	}
	if _, err := c.SubscribeTo(ctx, ipc.MethodWakesList, nil); !errors.Is(err, ipc.ErrInvalidRequest) {
		t.Fatalf("SubscribeTo a request method = %v, want ErrInvalidRequest", err)
	}
}

// A restricted stream's handler may choose the publisher the stream attaches
// to, which is how a subordinate coordinator holds its own domain's wake
// stream: a handler answering a StreamChoice naming a stream moves the
// subscription onto the publisher StreamFor answers for the name, so the
// stream carries that publisher's events and nothing the method's own
// publisher pushes; a handler answering nothing attaches to the method's
// own, the positive control; and a choice this server has no StreamFor for,
// or one StreamFor refuses, closes the stream with the refusal before
// anything is written.
func TestARestrictedStreamsHandlerMayChooseItsPublisher(t *testing.T) {
	requiresIdentifiedPeer(t)
	wakes := publisher(t, ipc.PublisherConfig{})
	alpha := publisher(t, ipc.PublisherConfig{})
	refused := errors.New("no such domain")
	h := serveOnSocket(t, func(sc *ipc.ServerConfig) {
		sc.Streams = map[ipc.Method]*ipc.Publisher{ipc.MethodWakesSubscribe: wakes}
		sc.StreamFor = func(m ipc.Method, name string) (*ipc.Publisher, error) {
			if m != ipc.MethodWakesSubscribe {
				return nil, fmt.Errorf("%w: %s", ipc.ErrUnavailable, m)
			}
			if name == "alpha" {
				return alpha, nil
			}
			return nil, fmt.Errorf("%w: %v", ipc.ErrRefused, refused)
		}
	})
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	h.handler.result = json.RawMessage(`{"stream":"alpha"}`)
	chosen, err := c.SubscribeTo(ctx, ipc.MethodWakesSubscribe, nil)
	if err != nil {
		t.Fatalf("SubscribeTo with a choice: %v", err)
	}
	defer chosen.Close()
	if first, err := chosen.Recv(ctx); err != nil || first.Type != ipc.TypeGap {
		t.Fatalf("first delivery = %+v (%v), want the opening gap marker", first, err)
	}
	publish(t, wakes, wakeEvent(7))
	publish(t, alpha, wakeEvent(9))
	if got, err := chosen.Recv(ctx); err != nil || got.Type != ipc.TypeWakeQueued || got.Revision != 9 {
		t.Fatalf("delivery = %+v (%v), want alpha's wake at revision 9 and nothing from the method's own publisher", got, err)
	}
	if n := alpha.Subscribers(); n != 1 {
		t.Fatalf("alpha has %d subscribers, want the one moved onto it", n)
	}
	if n := wakes.Subscribers(); n != 0 {
		t.Fatalf("the method's own publisher has %d subscribers, want none: the provisional subscription is closed", n)
	}

	h.handler.result = nil
	own, err := c.SubscribeTo(ctx, ipc.MethodWakesSubscribe, nil)
	if err != nil {
		t.Fatalf("SubscribeTo with no choice: %v", err)
	}
	defer own.Close()
	if first, err := own.Recv(ctx); err != nil || first.Type != ipc.TypeGap {
		t.Fatalf("first delivery = %+v (%v), want the opening gap marker", first, err)
	}
	publish(t, wakes, wakeEvent(11))
	if got, err := own.Recv(ctx); err != nil || got.Revision != 11 {
		t.Fatalf("delivery = %+v (%v), want the method's own wake at revision 11", got, err)
	}

	h.handler.result = json.RawMessage(`{"stream":"beta"}`)
	if _, err := c.SubscribeTo(ctx, ipc.MethodWakesSubscribe, nil); !errors.Is(err, ipc.ErrRefused) {
		t.Fatalf("SubscribeTo with a choice StreamFor refuses = %v, want ErrRefused", err)
	}
	if n := wakes.Subscribers(); n != 1 {
		t.Fatalf("after the refusal the method's own publisher has %d subscribers, want the one open stream", n)
	}
}
