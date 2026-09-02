package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
)

// Request is one call, as a handler sees it.
type Request struct {
	// Method is the method the caller named. It is always one this build
	// serves, because an unknown method is refused before a handler is
	// reached.
	Method Method
	// Params is the body the caller sent, left as written.
	Params json.RawMessage
	// Peer is who is on the other end. Reading an identity out of it means
	// handling the refusal when there is none.
	Peer Peer
}

// Handler serves requests. A refusal is a returned error, and one that matches
// a sentinel in this package crosses the wire with that sentinel's code.
type Handler interface {
	Serve(ctx context.Context, req Request) (json.RawMessage, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, req Request) (json.RawMessage, error)

// Serve calls f.
func (f HandlerFunc) Serve(ctx context.Context, req Request) (json.RawMessage, error) {
	return f(ctx, req)
}

// Containment says whether a caller is running inside an active validation
// stage, and which one.
type Containment struct {
	// Contained is whether the caller is inside an active validation stage.
	Contained bool
	// Run and Stage name what contains the caller, for a refusal that says
	// what happened. Both are empty when Contained is false.
	Run   string
	Stage string
}

// Ancestry answers whether the process holding a connection descends from an
// active validation stage. It is the seam PRD section 9's containment rule
// needs, and nothing in this repository implements it yet: resolving a process
// tree and knowing which runs are still active are both state this package
// does not own.
//
// It takes Credentials rather than a marker or a claimed identifier, because
// authority comes from what the kernel attributes to the connection. An
// implementation that consults the environment instead is answering a question
// the caller was allowed to write the answer to.
type Ancestry interface {
	// Contained reports whether the process named by c is inside an active
	// validation stage. An error is a fact that could not be established, and
	// the server refuses the request rather than serving it on an assumption.
	Contained(ctx context.Context, c Credentials) (Containment, error)
}

// ServerConfig is what a server needs before it can serve anything. Every
// field is required, and NewServer refuses a missing one.
type ServerConfig struct {
	// Handler serves every request that passes the access check.
	Handler Handler
	// Ancestry decides containment for restricted methods. There is no
	// default: a server with no way to answer the question would serve
	// restricted methods to a validating agent, which is the failure the rule
	// exists to prevent, so a server cannot be built without one.
	Ancestry Ancestry
	// Events is the stream subscribers attach to.
	Events *Publisher
	// Backlog is the per-subscriber queue depth. Zero means DefaultBacklog.
	Backlog int
	// MaxFrameBytes bounds one frame in either direction. Zero means
	// DefaultMaxFrameBytes.
	MaxFrameBytes int
}

// Server serves the local protocol over a listener.
type Server struct {
	cfg ServerConfig

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool

	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc
}

// NewServer returns a server, or an error naming what it was not given.
func NewServer(cfg ServerConfig) (*Server, error) {
	switch {
	case cfg.Handler == nil:
		return nil, errors.New("ipc: server needs a handler")
	case cfg.Ancestry == nil:
		return nil, errors.New("ipc: server needs an ancestry, because a restricted method cannot be decided without one")
	case cfg.Events == nil:
		return nil, errors.New("ipc: server needs a publisher to subscribe to")
	case cfg.Backlog < 0:
		return nil, fmt.Errorf("ipc: backlog %d is not a queue depth", cfg.Backlog)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		cfg:    cfg,
		conns:  make(map[net.Conn]struct{}),
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

// Serve accepts connections until the listener fails or the server is closed.
// It returns nil when it stopped because of Close.
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			if s.isClosed() {
				return nil
			}
			return err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			_ = conn.Close()
			return nil
		}
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(conn)
		}()
	}
}

// Close stops serving, drops every connection, and waits for the goroutines
// serving them to finish.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	s.cancel()
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
	return nil
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Server) forget(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

// conn is one served connection.
type conn struct {
	srv    *Server
	nc     net.Conn
	peer   Peer
	r      *frameReader
	w      *frameWriter
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	streams map[uint64]*Subscription
	wg      sync.WaitGroup
}

// serveConn identifies the peer once and then serves frames until the
// connection ends.
func (s *Server) serveConn(nc net.Conn) {
	ctx, cancel := context.WithCancel(s.ctx)
	c := &conn{
		srv:     s,
		nc:      nc,
		peer:    Identify(nc),
		r:       newFrameReader(nc, s.cfg.MaxFrameBytes),
		w:       newFrameWriter(nc, s.cfg.MaxFrameBytes),
		ctx:     ctx,
		cancel:  cancel,
		streams: make(map[uint64]*Subscription),
	}
	defer func() {
		cancel()
		c.closeStreams()
		_ = nc.Close()
		c.wg.Wait()
		s.forget(nc)
	}()
	c.serve()
}

func (c *conn) serve() {
	for {
		f, err := c.r.read()
		if err != nil {
			if errors.Is(err, ErrFrameTooLarge) || errors.Is(err, ErrInvalidRequest) {
				// The reader has no position to resume from, so the failure is
				// reported and the connection ends.
				_ = c.w.write(frame{Error: newError("", err)})
			}
			return
		}
		if f.Cancel {
			c.endStream(f.ID, ErrStreamClosed)
			continue
		}
		c.dispatch(f)
	}
}

// dispatch serves one request frame.
func (c *conn) dispatch(f frame) {
	spec, ok := Lookup(f.Method)
	if !ok {
		_ = c.w.write(frame{ID: f.ID, Error: newError(f.Method, fmt.Errorf("%w: %q", ErrUnknownMethod, f.Method))})
		return
	}
	peer := c.peer.withMarker(f.Marker)
	if err := c.authorize(spec, peer); err != nil {
		_ = c.w.write(frame{ID: f.ID, Error: newError(f.Method, err)})
		return
	}
	if spec.Kind == KindStream {
		c.startStream(f)
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		result, err := c.srv.cfg.Handler.Serve(c.ctx, Request{Method: f.Method, Params: f.Params, Peer: peer})
		if err != nil {
			_ = c.w.write(frame{ID: f.ID, Error: newError(f.Method, err)})
			return
		}
		_ = c.w.write(frame{ID: f.ID, Result: nullIfEmpty(result)})
	}()
}

// authorize applies the access class of a method to a peer.
//
// A restricted method needs an identified peer and an answered containment
// question. Each of those is a fact, and a fact that cannot be established
// refuses the request: an unidentified peer cannot be shown not to be a
// validating agent, and an ancestry that could not answer has not said no.
func (c *conn) authorize(spec Spec, peer Peer) error {
	if spec.Access != AccessRestricted {
		return nil
	}
	creds, err := peer.Credentials()
	if err != nil {
		return err
	}
	containment, err := c.srv.cfg.Ancestry.Contained(c.ctx, creds)
	if err != nil {
		return fmt.Errorf("%w: whether %s is inside a validation stage could not be determined: %w", ErrUnavailable, creds, err)
	}
	if containment.Contained {
		return fmt.Errorf("%w: %s is inside stage %q of run %q",
			ErrContained, creds, containment.Stage, containment.Run)
	}
	return nil
}

// startStream subscribes and pumps events to the caller. The subscription is
// acknowledged before the first event, so a caller learns synchronously that
// its stream is open.
func (c *conn) startStream(f frame) {
	sub, err := c.srv.cfg.Events.Subscribe(c.srv.cfg.Backlog)
	if err != nil {
		_ = c.w.write(frame{ID: f.ID, Error: newError(f.Method, err)})
		return
	}
	c.mu.Lock()
	if _, dup := c.streams[f.ID]; dup {
		c.mu.Unlock()
		sub.Close()
		_ = c.w.write(frame{ID: f.ID, Error: newError(f.Method, fmt.Errorf("%w: stream %d is already open", ErrInvalidRequest, f.ID))})
		return
	}
	c.streams[f.ID] = sub
	c.mu.Unlock()
	if err := c.w.write(frame{ID: f.ID, Result: json.RawMessage("{}")}); err != nil {
		c.endStream(f.ID, ErrStreamClosed)
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.pump(f.ID, sub)
	}()
}

// pump moves events from a subscription onto the connection. It blocks on the
// connection, never on the publisher: a caller that stops reading fills its own
// queue and gaps itself, and the work being reported on is untouched.
func (c *conn) pump(id uint64, sub *Subscription) {
	for {
		e, err := sub.Recv(c.ctx)
		if err != nil {
			c.finishStream(id, err)
			return
		}
		event := e
		if err := c.w.write(frame{ID: id, Event: &event}); err != nil {
			c.endStream(id, ErrStreamClosed)
			return
		}
	}
}

// finishStream tells the caller a stream ended and why, and forgets it.
func (c *conn) finishStream(id uint64, cause error) {
	c.mu.Lock()
	sub, ok := c.streams[id]
	delete(c.streams, id)
	c.mu.Unlock()
	if ok {
		sub.Close()
	}
	f := frame{ID: id, Done: true}
	if cause != nil && !errors.Is(cause, ErrStreamClosed) && !errors.Is(cause, context.Canceled) {
		f.Error = newError(MethodEventsSubscribe, cause)
	}
	_ = c.w.write(f)
}

// endStream closes a stream without reporting anything back, for the paths
// where the connection itself is what ended.
func (c *conn) endStream(id uint64, cause error) {
	c.mu.Lock()
	sub, ok := c.streams[id]
	delete(c.streams, id)
	c.mu.Unlock()
	if ok {
		sub.finish(cause)
		sub.Close()
	}
}

// closeStreams detaches every stream this connection opened.
func (c *conn) closeStreams() {
	c.mu.Lock()
	subs := make([]*Subscription, 0, len(c.streams))
	for id, sub := range c.streams {
		subs = append(subs, sub)
		delete(c.streams, id)
	}
	c.mu.Unlock()
	for _, sub := range subs {
		sub.Close()
	}
}

// nullIfEmpty renders a handler's empty result as JSON null, so a frame always
// carries a body a caller can decode.
func nullIfEmpty(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return b
}
