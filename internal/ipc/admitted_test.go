package ipc_test

import (
	"context"
	"encoding/json"
	"errors"
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
