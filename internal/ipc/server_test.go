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
