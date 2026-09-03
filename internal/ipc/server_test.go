package ipc_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
)

// recordingAncestry stands in for the ancestry resolver this repository does
// not have yet. It answers what a test told it to answer and records what it
// was asked, which is how a test shows the server asked at all.
type recordingAncestry struct {
	mu     sync.Mutex
	asked  []ipc.Credentials
	result ipc.Containment
	err    error
}

func (a *recordingAncestry) Contained(_ context.Context, c ipc.Credentials) (ipc.Containment, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, c)
	return a.result, a.err
}

func (a *recordingAncestry) questions() []ipc.Credentials {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ipc.Credentials(nil), a.asked...)
}

// recordingHandler answers every request with the request it was given, so a
// test can see what reached it, and records whether it was reached at all.
type recordingHandler struct {
	mu       sync.Mutex
	requests []ipc.Request
	result   json.RawMessage
	err      error
}

type served struct {
	Method string `json:"method"`
	Params string `json:"params"`
	Marker string `json:"marker"`
	PID    int    `json:"pid"`
	UID    int    `json:"uid"`
	PeerOK bool   `json:"peer_ok"`
}

func (h *recordingHandler) Serve(_ context.Context, req ipc.Request) (json.RawMessage, error) {
	h.mu.Lock()
	h.requests = append(h.requests, req)
	h.mu.Unlock()
	if h.err != nil {
		return nil, h.err
	}
	if h.result != nil {
		return h.result, nil
	}
	out := served{Method: string(req.Method), Params: string(req.Params), Marker: string(req.Peer.Marker())}
	if creds, err := req.Peer.Credentials(); err == nil {
		out.PeerOK, out.PID, out.UID = true, creds.PID, creds.UID
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (h *recordingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

type harness struct {
	socket   string
	handler  *recordingHandler
	ancestry *recordingAncestry
	events   *ipc.Publisher
	server   *ipc.Server
}

// serveOnSocket starts a server on a local socket, which is the transport the
// service actually uses and the only one a peer can be identified over.
func serveOnSocket(t *testing.T, cfg func(*ipc.ServerConfig)) *harness {
	t.Helper()
	dir, err := os.MkdirTemp("", "ipc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h := &harness{
		socket:   filepath.Join(dir, "s"),
		handler:  &recordingHandler{},
		ancestry: &recordingAncestry{},
		events:   ipc.NewPublisher(),
	}
	sc := ipc.ServerConfig{Handler: h.handler, Ancestry: h.ancestry, Events: h.events, Backlog: 8}
	if cfg != nil {
		cfg(&sc)
	}
	srv, err := ipc.NewServer(sc)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	h.server = srv
	l := listenLocal(t, h.socket)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	t.Cleanup(func() {
		srv.Close()
		l.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after Close")
		}
	})
	return h
}

// listenLocal opens the local socket the service is specified to use. A
// platform with no such transport cannot serve this protocol or identify a peer
// over it, so the tests that need one skip loudly there rather than passing;
// on the platforms the product runs on, a failure here is a failure.
func listenLocal(t *testing.T, path string) net.Listener {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		if !platformIdentifiesPeers() {
			t.Skipf("%s has no local socket transport to serve this protocol over: %v", runtime.GOOS, err)
		}
		t.Fatalf("listen: %v", err)
	}
	return l
}

func (h *harness) dial(t *testing.T, cfg ipc.ClientConfig) *ipc.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := ipc.Dial(ctx, h.socket, cfg)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func callCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func TestNewServerRefusesAnIncompleteConfiguration(t *testing.T) {
	full := ipc.ServerConfig{Handler: &recordingHandler{}, Ancestry: &recordingAncestry{}, Events: ipc.NewPublisher()}
	cases := map[string]func(*ipc.ServerConfig){
		"no handler":                          func(c *ipc.ServerConfig) { c.Handler = nil },
		"no ancestry":                         func(c *ipc.ServerConfig) { c.Ancestry = nil },
		"no publisher":                        func(c *ipc.ServerConfig) { c.Events = nil },
		"a backlog that is not a queue depth": func(c *ipc.ServerConfig) { c.Backlog = -1 },
		"a negative stream limit":             func(c *ipc.ServerConfig) { c.MaxStreams = -1 },
		"a negative in-flight limit":          func(c *ipc.ServerConfig) { c.MaxInFlight = -1 },
	}
	for name, breakIt := range cases {
		cfg := full
		breakIt(&cfg)
		if _, err := ipc.NewServer(cfg); err == nil {
			t.Errorf("NewServer with %s was accepted", name)
		}
	}
	if _, err := ipc.NewServer(full); err != nil {
		t.Errorf("NewServer with everything: %v", err)
	}
}

func TestCallRoundTrip(t *testing.T) {
	h := serveOnSocket(t, nil)
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	var got served
	if err := c.Call(ctx, "status", map[string]string{"repo": "here"}, &got); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got.Method != "status" {
		t.Errorf("handler saw method %q, want status", got.Method)
	}
	if got.Params != `{"repo":"here"}` {
		t.Errorf("handler saw params %q", got.Params)
	}
}

func TestCallReportsAHandlerRefusalWithItsSentinel(t *testing.T) {
	h := serveOnSocket(t, nil)
	h.handler.err = ipc.ErrUnavailable
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	err := c.Call(ctx, "status", nil, nil)
	if !errors.Is(err, ipc.ErrUnavailable) {
		t.Fatalf("Call = %v, want ErrUnavailable", err)
	}
	var wire *ipc.Error
	if !errors.As(err, &wire) {
		t.Fatalf("Call error is %T, want *ipc.Error", err)
	}
	if wire.Method != "status" {
		t.Errorf("the refusal names method %q, want status", wire.Method)
	}
}

// TestUnknownMethodNeverReachesTheHandler sends the frame directly, because a
// client refuses an unknown method before it sends one and the service must
// refuse it as well.
func TestUnknownMethodNeverReachesTheHandler(t *testing.T) {
	h := serveOnSocket(t, nil)
	conn, err := net.Dial("unix", h.socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"id":1,"method":"run.obliterate"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var reply struct {
		ID    uint64     `json:"id"`
		Error *ipc.Error `json:"error"`
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(line, &reply); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if reply.Error == nil || reply.Error.Code != ipc.CodeUnknownMethod {
		t.Fatalf("reply = %+v, want an unknown-method error", reply.Error)
	}
	if h.handler.count() != 0 {
		t.Error("an unknown method reached the handler")
	}
}

func TestClientRefusesAMethodItCannotSend(t *testing.T) {
	h := serveOnSocket(t, nil)
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	if err := c.Call(ctx, "run.obliterate", nil, nil); !errors.Is(err, ipc.ErrUnknownMethod) {
		t.Errorf("Call = %v, want ErrUnknownMethod", err)
	}
	if err := c.Call(ctx, "events.subscribe", nil, nil); !errors.Is(err, ipc.ErrInvalidRequest) {
		t.Errorf("Call(events.subscribe) = %v, want ErrInvalidRequest", err)
	}
}

func TestRestrictedMethodRefusesAContainedCaller(t *testing.T) {
	h := serveOnSocket(t, nil)
	h.ancestry.result = ipc.Containment{Contained: true, Run: "r7", Stage: "test"}
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	err := c.Call(ctx, "service.stop", nil, nil)
	if !errors.Is(err, ipc.ErrContained) {
		t.Fatalf("Call = %v, want ErrContained", err)
	}
	if h.handler.count() != 0 {
		t.Error("a contained caller reached the handler")
	}
	if len(h.ancestry.questions()) != 1 {
		t.Errorf("the ancestry was asked %d times, want once", len(h.ancestry.questions()))
	}
}

func TestRestrictedMethodRefusesWhenContainmentCannotBeDetermined(t *testing.T) {
	h := serveOnSocket(t, nil)
	h.ancestry.err = errors.New("the process table could not be read")
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	err := c.Call(ctx, "run.start", nil, nil)
	if !errors.Is(err, ipc.ErrUnavailable) {
		t.Fatalf("Call = %v, want ErrUnavailable: an unanswered containment question is not a no", err)
	}
	if h.handler.count() != 0 {
		t.Error("a request whose containment was unknown reached the handler")
	}
}

func TestOpenMethodIsServedWithoutAskingAboutContainment(t *testing.T) {
	h := serveOnSocket(t, nil)
	h.ancestry.result = ipc.Containment{Contained: true, Run: "r7", Stage: "test"}
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	var got served
	if err := c.Call(ctx, "stage.report", map[string]string{"stage": "test"}, &got); err != nil {
		t.Fatalf("Call = %v, want a contained caller to be able to return its own stage", err)
	}
	if len(h.ancestry.questions()) != 0 {
		t.Error("an open method consulted the ancestry")
	}
}

// TestTheMarkerNeitherAuthorizesNorRefuses is the rule that keeps an
// environment variable from becoming authority. The claim is carried and
// reported, and the decision is taken on what the kernel said instead.
func TestTheMarkerNeitherAuthorizesNorRefuses(t *testing.T) {
	h := serveOnSocket(t, nil)
	c := h.dial(t, ipc.ClientConfig{Marker: "run=r7 stage=test"})
	ctx, cancel := callCtx(t)
	defer cancel()

	// Claiming to be inside a stage does not refuse the call.
	var got served
	if err := c.Call(ctx, "run.start", nil, &got); err != nil {
		t.Fatalf("Call = %v, want the call served: the ancestry said the caller is not contained", err)
	}
	if got.Marker != "run=r7 stage=test" {
		t.Errorf("the handler saw marker %q, want the claim carried through as evidence", got.Marker)
	}

	// Claiming nothing does not allow it either.
	h.ancestry.result = ipc.Containment{Contained: true, Run: "r7", Stage: "test"}
	plain := h.dial(t, ipc.ClientConfig{Marker: ""})
	if err := plain.Call(ctx, "run.start", nil, nil); !errors.Is(err, ipc.ErrContained) {
		t.Errorf("Call = %v, want ErrContained for a caller that claimed no marker", err)
	}
}

// pipeListener hands out one connection that is not a local socket, which is
// the shape the transport really produces when the protocol is carried over
// something the kernel cannot be asked about.
type pipeListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = l.conn })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *pipeListener) Close() error {
	close(l.done)
	return nil
}

func (l *pipeListener) Addr() net.Addr { return dummyAddr{} }

type dummyAddr struct{}

func (dummyAddr) Network() string { return "pipe" }
func (dummyAddr) String() string  { return "pipe" }

// TestUnidentifiedPeerIsRefusedForRestrictedMethods covers a connection the
// kernel cannot be asked about. Authority comes from the identification, so
// there is nothing to decide on and the request is refused rather than served.
func TestUnidentifiedPeerIsRefusedForRestrictedMethods(t *testing.T) {
	handler := &recordingHandler{}
	ancestry := &recordingAncestry{}
	srv, err := ipc.NewServer(ipc.ServerConfig{Handler: handler, Ancestry: ancestry, Events: ipc.NewPublisher()})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	serverSide, clientSide := net.Pipe()
	l := &pipeListener{conn: serverSide, done: make(chan struct{})}
	go srv.Serve(l)
	t.Cleanup(func() {
		srv.Close()
		l.Close()
	})
	c := ipc.NewClient(clientSide, ipc.ClientConfig{})
	t.Cleanup(func() { c.Close() })
	ctx, cancel := callCtx(t)
	defer cancel()

	if err := c.Call(ctx, "run.cancel", nil, nil); !errors.Is(err, ipc.ErrUnidentifiedPeer) {
		t.Fatalf("Call = %v, want ErrUnidentifiedPeer", err)
	}
	if handler.count() != 0 {
		t.Error("a request from an unidentified peer reached the handler")
	}
	if len(ancestry.questions()) != 0 {
		t.Error("the ancestry was asked about a peer with no credentials")
	}
	var got served
	if err := c.Call(ctx, "status", nil, &got); err != nil {
		t.Fatalf("an open method was refused for an unidentified peer: %v", err)
	}
	if got.PeerOK {
		t.Error("the handler read credentials out of a peer that was never identified")
	}
}

func TestSubscribeOpensGappedAndDelivers(t *testing.T) {
	h := serveOnSocket(t, nil)
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	stream, err := c.Subscribe(ctx, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stream.Close()

	first, err := stream.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if first.Type != "stream.gap" {
		t.Fatalf("first delivery is %q, want the opening gap marker", first.Type)
	}
	if gap, err := ipc.GapPayload(first); err != nil || gap.Dropped != 0 {
		t.Fatalf("opening marker = %+v (err %v), want a marker reporting nothing dropped", gap, err)
	}
	publish(t, h.events, state("s1", 4), activity("a1"))
	for i, want := range []ipc.Type{"run.state", "log.line"} {
		e, err := stream.Recv(ctx)
		if err != nil {
			t.Fatalf("Recv %d: %v", i, err)
		}
		if e.Type != want {
			t.Fatalf("delivery %d is %q, want %q", i, e.Type, want)
		}
		if want == "run.state" && e.Revision != 4 {
			t.Errorf("revision = %d, want the published 4", e.Revision)
		}
	}
}

func TestStreamEndsWhenThePublisherCloses(t *testing.T) {
	h := serveOnSocket(t, nil)
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	stream, err := c.Subscribe(ctx, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := stream.Recv(ctx); err != nil {
		t.Fatalf("Recv: %v", err)
	}
	h.events.Close()
	if _, err := stream.Recv(ctx); !errors.Is(err, ipc.ErrStreamClosed) {
		t.Errorf("Recv after the publisher closed = %v, want ErrStreamClosed", err)
	}
}

func TestCallsAndStreamsShareAConnection(t *testing.T) {
	h := serveOnSocket(t, nil)
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	stream, err := c.Subscribe(ctx, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Recv(ctx); err != nil {
		t.Fatalf("Recv: %v", err)
	}
	publish(t, h.events, state("s1", 1))
	var got served
	if err := c.Call(ctx, "health", nil, &got); err != nil {
		t.Fatalf("Call while a stream is open: %v", err)
	}
	if got.Method != "health" {
		t.Errorf("handler saw %q, want health", got.Method)
	}
	e, err := stream.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv after a call: %v", err)
	}
	if e.Type != "run.state" {
		t.Errorf("delivery is %q, want run.state", e.Type)
	}
}

func TestClientCloseReleasesWaitingCallers(t *testing.T) {
	h := serveOnSocket(t, nil)
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	stream, err := c.Subscribe(ctx, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := stream.Recv(ctx); err != nil {
		t.Fatalf("Recv: %v", err)
	}
	c.Close()
	if _, err := stream.Recv(ctx); !errors.Is(err, ipc.ErrClientClosed) {
		t.Errorf("Recv after Close = %v, want ErrClientClosed", err)
	}
	if err := c.Call(ctx, "health", nil, nil); !errors.Is(err, ipc.ErrClientClosed) {
		t.Errorf("Call after Close = %v, want ErrClientClosed", err)
	}
}

// blockingHandler holds every request inside the handler until the test lets
// it go, which is how a test holds a connection's in-flight slots open and sees
// what the request behind them gets.
type blockingHandler struct {
	entered chan struct{}
	release chan struct{}
}

func (h *blockingHandler) Serve(ctx context.Context, _ ipc.Request) (json.RawMessage, error) {
	select {
	case h.entered <- struct{}{}:
	default:
		// The channel is a signal for the test to wait on, not a rendezvous
		// every request has to be met at.
	}
	select {
	case <-h.release:
		return json.RawMessage(`{"served":true}`), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// failingHandler refuses every request with one error, so a test can watch a
// sentinel cross a real connection.
type failingHandler struct{ err error }

func (h *failingHandler) Serve(context.Context, ipc.Request) (json.RawMessage, error) {
	return nil, h.err
}

// TestASentinelRefusalMatchesItselfAtTheClient covers what a code is for. A
// consumer told its subscription stalled has to reattach and reconcile, and it
// can only tell that from "the service broke" if the sentinel survives the
// wire rather than arriving as the internal category.
func TestASentinelRefusalMatchesItselfAtTheClient(t *testing.T) {
	sentinels := map[string]error{
		"stalled":     ipc.ErrSubscriberStalled,
		"closed":      ipc.ErrStreamClosed,
		"too large":   ipc.ErrFrameTooLarge,
		"busy":        ipc.ErrConnectionBusy,
		"unavailable": ipc.ErrUnavailable,
		"contained":   ipc.ErrContained,
	}
	for name, sentinel := range sentinels {
		t.Run(name, func(t *testing.T) {
			h := serveOnSocket(t, func(c *ipc.ServerConfig) { c.Handler = &failingHandler{err: sentinel} })
			c := h.dial(t, ipc.ClientConfig{})
			ctx, cancel := callCtx(t)
			defer cancel()
			if err := c.Call(ctx, "status", nil, nil); !errors.Is(err, sentinel) {
				t.Fatalf("Call = %v, want it to match %v after the wire", err, sentinel)
			}
		})
	}
}

// TestAnAnswerTooLargeForAFrameIsReportedRatherThanDropped covers a request
// that was served and whose answer cannot be delivered. Nothing else answers
// that identifier, so without a refusal the caller waits for an answer that is
// never coming, and forever when its context has no deadline.
func TestAnAnswerTooLargeForAFrameIsReportedRatherThanDropped(t *testing.T) {
	const limit = 4096
	oversized, err := json.Marshal(map[string]string{"pad": strings.Repeat("x", 4*limit)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	h := serveOnSocket(t, func(c *ipc.ServerConfig) { c.MaxFrameBytes = limit })
	h.handler.result = oversized
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	err = c.Call(ctx, "status", nil, nil)
	if !errors.Is(err, ipc.ErrFrameTooLarge) {
		t.Fatalf("Call = %v, want the caller told its answer does not fit rather than left waiting", err)
	}
	var wire *ipc.Error
	if !errors.As(err, &wire) {
		t.Fatalf("Call error is %T, want *ipc.Error", err)
	}
	// The connection is healthy, so it is still serving.
	if err := c.Call(ctx, "health", nil, nil); !errors.Is(err, ipc.ErrFrameTooLarge) {
		t.Fatalf("second Call = %v, want the connection still answering", err)
	}
}

// TestAnEventTooLargeForAFrameGapsTheStreamRatherThanEndingIt covers the
// producer that did not bound its projection. The connection is healthy, so
// the event is a discard like any other: the consumer is told to reconcile and
// keeps its stream.
func TestAnEventTooLargeForAFrameGapsTheStreamRatherThanEndingIt(t *testing.T) {
	const limit = 4096
	h := serveOnSocket(t, func(c *ipc.ServerConfig) { c.MaxFrameBytes = limit })
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	stream, err := c.Subscribe(ctx, nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer stream.Close()
	if first, err := stream.Recv(ctx); err != nil || first.Type != "stream.gap" {
		t.Fatalf("first delivery = %q (err %v), want the opening gap marker", first.Type, err)
	}
	oversized := ipc.Event{
		Type:     "run.state",
		Revision: 7,
		Payload:  json.RawMessage(`{"label":"` + strings.Repeat("x", 4*limit) + `"}`),
	}
	publish(t, h.events, oversized, state("fits", 8))

	marker, err := stream.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv after an event that does not fit = %v, want the stream to survive it", err)
	}
	if marker.Type != "stream.gap" {
		t.Fatalf("delivery after an oversized event is %q, want a gap marker", marker.Type)
	}
	gap, err := ipc.GapPayload(marker)
	if err != nil || gap.Dropped != 1 {
		t.Fatalf("marker = %+v (err %v), want one discard reported", gap, err)
	}
	next, err := stream.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv after the marker: %v", err)
	}
	if next.Type != "run.state" || next.Revision != 8 || label(t, next) != "fits" {
		t.Fatalf("delivery after the marker = %+v, want the state event that does fit", next)
	}
}

// TestConnectionRefusesMoreStreamsThanItMayHold covers the bound on a resource
// any identified caller can reach. Exceeding it names the limit rather than
// dropping the request, and ending a stream gives the slot back.
func TestConnectionRefusesMoreStreamsThanItMayHold(t *testing.T) {
	h := serveOnSocket(t, func(c *ipc.ServerConfig) { c.MaxStreams = 2 })
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	first, err := c.Subscribe(ctx, nil)
	if err != nil {
		t.Fatalf("first Subscribe: %v", err)
	}
	defer first.Close()
	second, err := c.Subscribe(ctx, nil)
	if err != nil {
		t.Fatalf("second Subscribe: %v", err)
	}
	if _, err := c.Subscribe(ctx, nil); !errors.Is(err, ipc.ErrConnectionBusy) {
		t.Fatalf("Subscribe past the limit = %v, want ErrConnectionBusy", err)
	} else if !strings.Contains(err.Error(), "2 of 2 allowed") {
		t.Errorf("the refusal reads %q, want it to name the limit and the count", err)
	}

	// The service reads a connection's frames in order, so the cancel this
	// sends is applied before the request behind it: the slot is free.
	if err := second.Close(); err != nil {
		t.Fatalf("closing a stream: %v", err)
	}
	third, err := c.Subscribe(ctx, nil)
	if err != nil {
		t.Fatalf("Subscribe after a stream ended = %v, want the slot freed", err)
	}
	third.Close()
}

// TestConnectionRefusesMoreRequestsThanItMayServeAtOnce covers the same bound
// for work in a handler. The refusal must come back while the connection keeps
// serving, because the goroutine that would wait for a slot is the one reading
// the connection.
func TestConnectionRefusesMoreRequestsThanItMayServeAtOnce(t *testing.T) {
	handler := &blockingHandler{entered: make(chan struct{}, 1), release: make(chan struct{})}
	h := serveOnSocket(t, func(c *ipc.ServerConfig) {
		c.Handler = handler
		c.MaxInFlight = 1
	})
	c := h.dial(t, ipc.ClientConfig{})
	ctx, cancel := callCtx(t)
	defer cancel()

	held := make(chan error, 1)
	go func() { held <- c.Call(ctx, "status", nil, nil) }()
	select {
	case <-handler.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first call never reached the handler")
	}

	err := c.Call(ctx, "health", nil, nil)
	if !errors.Is(err, ipc.ErrConnectionBusy) {
		t.Fatalf("Call past the limit = %v, want ErrConnectionBusy", err)
	}
	if !strings.Contains(err.Error(), "1 of 1 allowed") {
		t.Errorf("the refusal reads %q, want it to name the limit and the count", err)
	}

	close(handler.release)
	if err := <-held; err != nil {
		t.Fatalf("the held call = %v, want it served", err)
	}
	if err := c.Call(ctx, "health", nil, nil); err != nil {
		t.Fatalf("Call after the held one finished = %v, want the slot freed", err)
	}
}

// TestCloseLeavesTheListenerToItsOwner holds Serve's contract. The listener is
// the caller's, so Close drops the connections and closing the listener is what
// makes Serve return. A caller that waited for Serve after Close alone would
// wait forever.
func TestCloseLeavesTheListenerToItsOwner(t *testing.T) {
	dir, err := os.MkdirTemp("", "ipc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	srv, err := ipc.NewServer(ipc.ServerConfig{
		Handler:  &recordingHandler{},
		Ancestry: &recordingAncestry{},
		Events:   ipc.NewPublisher(),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	l := listenLocal(t, socket)
	defer l.Close()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()

	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	// An answered request is how this test knows the connection is accepted and
	// being served, and therefore that Serve is back in Accept rather than
	// about to reach it.
	if _, err := conn.Write([]byte(`{"id":1,"method":"health"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	reader := bufio.NewReader(conn)
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("read deadline: %v", err)
	}
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	srv.Close()

	// Close dropped the connection it accepted.
	if _, err := reader.ReadBytes('\n'); err == nil {
		t.Error("a connection survived Close")
	}
	select {
	case err := <-done:
		t.Fatalf("Serve returned %v while the listener it does not own is still open", err)
	case <-time.After(200 * time.Millisecond):
	}
	l.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve after the listener closed = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the listener it was given was closed")
	}
}

// wireFrame is the part of a frame this test reads off the socket. The wire
// form is the contract between the two halves of this package, and a scripted
// peer is how a test sees what a client sent without a service answering it.
type wireFrame struct {
	ID     uint64 `json:"id"`
	Method string `json:"method"`
	Cancel bool   `json:"cancel"`
}

// TestAnAbandonedSubscribeTellsTheServiceToStop covers a Subscribe whose
// context ends after the request is already on the wire. The service may have
// opened the stream and started pumping it, and a client that walks away
// without saying so leaves that queue, its goroutine, and its share of the
// connection's writer in place for as long as the connection lives.
func TestAnAbandonedSubscribeTellsTheServiceToStop(t *testing.T) {
	peer, clientSide := net.Pipe()
	defer peer.Close()
	c := ipc.NewClient(clientSide, ipc.ClientConfig{})
	defer c.Close()

	frames := make(chan wireFrame, 4)
	go func() {
		r := bufio.NewReader(peer)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var f wireFrame
			if err := json.Unmarshal(line, &f); err != nil {
				return
			}
			frames <- f
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	opened := make(chan error, 1)
	go func() {
		_, err := c.Subscribe(ctx, nil)
		opened <- err
	}()

	var request wireFrame
	select {
	case request = <-frames:
	case <-time.After(5 * time.Second):
		t.Fatal("Subscribe never sent its request")
	}
	if request.Method != "events.subscribe" {
		t.Fatalf("the first frame is %q, want the subscribe request", request.Method)
	}

	// The request is on the wire and no acknowledgement has been sent, which is
	// the window where the service may already be pumping the stream.
	cancel()
	if err := <-opened; !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscribe = %v, want the context's error", err)
	}
	select {
	case f := <-frames:
		if !f.Cancel {
			t.Fatalf("the frame after an abandoned subscribe is %+v, want a cancel", f)
		}
		if f.ID != request.ID {
			t.Errorf("the cancel names stream %d, want the abandoned %d", f.ID, request.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an abandoned subscribe left the service pumping a stream nothing reads")
	}
}
