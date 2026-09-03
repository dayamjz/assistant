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
	// DefaultMaxFrameBytes. A value below MinFrameBytes is refused, because a
	// server has to be able to write a refusal that fits.
	MaxFrameBytes int
	// MaxStreams bounds how many event streams one connection may hold open at
	// once. Zero means DefaultMaxStreams, and a negative value is refused.
	MaxStreams int
	// MaxInFlight bounds how many requests one connection may have being
	// served at once. Zero means DefaultMaxInFlight, and a negative value is
	// refused.
	MaxInFlight int
}

// DefaultMaxStreams bounds the event streams one connection may hold open at
// once when a caller does not choose a bound. Each stream costs a queue and a
// goroutine, and a consumer needs one of them, so the default leaves room for
// a client that opens a few and refuses a caller that opens them without end.
const DefaultMaxStreams = 16

// DefaultMaxInFlight bounds the requests one connection may have being served
// at once when a caller does not choose a bound. Each one is a goroutine
// inside a handler, and the protocol exists so a short call is not held up
// behind a long one, so the default leaves room for that and refuses a caller
// that turns the connection into an unbounded work queue.
const DefaultMaxInFlight = 64

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
	case cfg.MaxStreams < 0:
		return nil, fmt.Errorf("ipc: %d is not a number of streams a connection may hold", cfg.MaxStreams)
	case cfg.MaxInFlight < 0:
		return nil, fmt.Errorf("ipc: %d is not a number of requests a connection may serve at once", cfg.MaxInFlight)
	case cfg.MaxFrameBytes < 0:
		return nil, fmt.Errorf("ipc: %d is not a frame size", cfg.MaxFrameBytes)
	case cfg.MaxFrameBytes > 0 && cfg.MaxFrameBytes < minFrameBytes:
		return nil, fmt.Errorf("ipc: a frame limit of %d bytes cannot carry the refusals this server has to be able to write, which need %d",
			cfg.MaxFrameBytes, minFrameBytes)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		cfg:    cfg,
		conns:  make(map[net.Conn]struct{}),
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

// Serve accepts connections from l and serves each one until it ends. It
// returns nil once the server has been closed, and whatever Accept reported
// otherwise.
//
// The listener belongs to the caller. Close drops the connections this server
// accepted and does not close l, so a Serve waiting in Accept returns when the
// caller closes l or when one more connection arrives, whichever happens
// first.
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
		// The counter is raised under the same lock that Close takes before it
		// waits, so a connection accepted before Close is one Close waits for,
		// and there is no window where Close observes an empty counter while
		// this goroutine is about to raise it.
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			s.serveConn(conn)
		}()
	}
}

// Close stops serving, drops every connection this server accepted, and waits
// for the goroutines serving them to finish.
//
// It does not close the listener Serve was given, which the caller owns and
// may still be sharing. Closing that listener is what makes Serve return.
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

// maxStreams is how many streams one connection may hold open at once.
func (s *Server) maxStreams() int {
	if s.cfg.MaxStreams == 0 {
		return DefaultMaxStreams
	}
	return s.cfg.MaxStreams
}

// maxInFlight is how many requests one connection may have being served at
// once.
func (s *Server) maxInFlight() int {
	if s.cfg.MaxInFlight == 0 {
		return DefaultMaxInFlight
	}
	return s.cfg.MaxInFlight
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

	mu       sync.Mutex
	streams  map[uint64]*Subscription
	inflight int
	wg       sync.WaitGroup
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
			switch {
			case errors.Is(err, ErrInvalidRequest):
				// The reader took the whole line before it tried to decode it,
				// so it is positioned at the start of the next frame and this
				// connection can carry on. Dropping it here would take every
				// other call and stream this caller holds along with one frame
				// it got wrong.
				c.reportConnection(err)
				continue
			case errors.Is(err, ErrFrameTooLarge):
				// The rest of an over-long frame cannot be told apart from the
				// frames behind it, so there is no position to resume from and
				// the connection ends behind the report.
				c.reportConnection(err)
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

// reportConnection tells the peer about a frame this connection could not read.
//
// It names no request, because a frame that did not decode carries no
// identifier to answer, and the zero identifier is what says so: this package's
// Client allocates from one upwards and never asks with zero, so a peer can
// tell a report about the connection from an answer to something it asked for
// without guessing.
func (c *conn) reportConnection(cause error) {
	_ = c.w.write(frame{Error: newError("", cause)})
}

// dispatch serves one request frame.
func (c *conn) dispatch(f frame) {
	spec, ok := Lookup(f.Method)
	if !ok {
		c.answer(f.ID, frame{ID: f.ID, Error: newError(f.Method, fmt.Errorf("%w: %q", ErrUnknownMethod, f.Method))})
		return
	}
	peer := c.peer.withMarker(f.Marker)
	if err := c.authorize(spec, peer); err != nil {
		c.answer(f.ID, frame{ID: f.ID, Error: newError(f.Method, err)})
		return
	}
	if spec.Kind == KindStream {
		c.startStream(f)
		return
	}
	if err := c.beginRequest(); err != nil {
		c.answer(f.ID, frame{ID: f.ID, Error: newError(f.Method, err)})
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		result, err := c.srv.cfg.Handler.Serve(c.ctx, Request{Method: f.Method, Params: f.Params, Peer: peer})
		// The slot is returned before the answer is written, so a caller
		// holding its answer is a caller whose slot is already free rather
		// than one racing the write that delivered it.
		c.endRequest()
		if err != nil {
			c.answer(f.ID, frame{ID: f.ID, Error: newError(f.Method, err)})
			return
		}
		c.answer(f.ID, frame{ID: f.ID, Result: nullIfEmpty(result)})
	}()
}

// beginRequest takes one of this connection's in-flight slots, or reports the
// refusal a caller gets instead.
//
// It refuses rather than waiting. The goroutine that would wait is the one
// reading the connection, and a request cannot finish while nothing is reading
// the connection it would answer on, so waiting here would trade a refusal a
// caller can act on for a connection that has stopped.
func (c *conn) beginRequest() error {
	limit := c.srv.maxInFlight()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight >= limit {
		return fmt.Errorf("%w: this connection is already serving %d of %d allowed requests at once",
			ErrConnectionBusy, c.inflight, limit)
	}
	c.inflight++
	return nil
}

// endRequest returns an in-flight slot once the handler holding it has
// returned.
func (c *conn) endRequest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflight--
}

// errUndeliverableControl is why a stream ends when a control event cannot be
// put in a frame on its connection, whether it was too large or would not
// encode: the consumer's answer to both is to attach again, and the producer
// learns which at the publisher. Its text is fixed, so MinFrameBytes can be
// measured against the frame that carries it.
var errUndeliverableControl = fmt.Errorf("%w: a control event could not be put in one frame here, and control may not be discarded", ErrEventUndeliverable)

// undeliverableAnswer is the refusal a request gets when its answer could not
// be put on the wire. It carries no method and a fixed message, so its size
// depends on nothing but the identifier being answered.
//
// It says only what is known here. Every answer takes this path, including a
// refusal raised before any handler was reached, so it reports that the answer
// could not be delivered rather than claiming the request was served.
func undeliverableAnswer(id uint64, code Code) frame {
	message := "the answer to this request could not be encoded"
	if code == CodeFrameTooLarge {
		message = "the answer to this request does not fit in one frame"
	}
	return frame{ID: id, Error: &Error{Code: code, Message: message}}
}

// fixedRefusals are the frames this package must always be able to write: the
// fallback answer to a request whose reply could not be delivered, and each
// reason a stream ends over something rather than nothing. Every one of them
// carries a fixed message, so the widest identifier gives the widest frame.
func fixedRefusals() []frame {
	const widest = ^uint64(0)
	return []frame{
		undeliverableAnswer(widest, CodeFrameTooLarge),
		undeliverableAnswer(widest, CodeInternal),
		{ID: widest, Done: true, Error: newError(MethodEventsSubscribe, ErrSubscriberStalled)},
		{ID: widest, Done: true, Error: newError(MethodEventsSubscribe, errUndeliverableControl)},
	}
}

// minFrameBytes is the widest of those, measured rather than guessed so it
// cannot drift from the messages it is measuring.
var minFrameBytes = func() int {
	widest := 0
	for _, f := range fixedRefusals() {
		var counted countingWriter
		if err := newFrameWriter(&counted, DefaultMaxFrameBytes).write(f); err != nil {
			panic("ipc: a fixed refusal does not fit in a default frame: " + err.Error())
		}
		if int(counted) > widest {
			widest = int(counted)
		}
	}
	return widest
}()

// countingWriter measures a frame without keeping it.
type countingWriter int

func (c *countingWriter) Write(p []byte) (int, error) {
	*c += countingWriter(len(p))
	return len(p), nil
}

// MinFrameBytes is the smallest frame limit a server may be built with. Below
// it, a refusal this package must be able to write would not fit, and a caller
// would wait on an answer that was refused for not fitting, which is the
// failure the refusal exists to remove.
func MinFrameBytes() int { return minFrameBytes }

// answer writes the one reply a request gets, and answers with a refusal when
// that reply cannot be put on the wire at all.
//
// An answer past the frame limit, or one that does not encode, is a failure
// about the answer rather than about the connection, and no other path answers
// this identifier. Without the second write the caller would wait for an answer
// that is never coming, so it is told that its answer could not be delivered.
// The refusal carries no method and a fixed message, and
// NewServer refuses a frame limit too small to carry it, so on a server that was
// built it fits whatever the answer that did not.
func (c *conn) answer(id uint64, f frame) {
	err := c.w.write(f)
	if err == nil {
		return
	}
	if !unbuildable(err) {
		// The connection failed rather than the frame. There is nowhere to put
		// a smaller answer, and the read loop ends the connection.
		return
	}
	code := CodeInternal
	if errors.Is(err, ErrFrameTooLarge) {
		code = CodeFrameTooLarge
	}
	_ = c.w.write(undeliverableAnswer(id, code))
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
		c.answer(f.ID, frame{ID: f.ID, Error: newError(f.Method, err)})
		return
	}
	c.mu.Lock()
	if _, dup := c.streams[f.ID]; dup {
		c.mu.Unlock()
		sub.Close()
		c.answer(f.ID, frame{ID: f.ID, Error: newError(f.Method, fmt.Errorf("%w: stream %d is already open", ErrInvalidRequest, f.ID))})
		return
	}
	if limit := c.srv.maxStreams(); len(c.streams) >= limit {
		open := len(c.streams)
		c.mu.Unlock()
		sub.Close()
		c.answer(f.ID, frame{ID: f.ID, Error: newError(f.Method, fmt.Errorf("%w: this connection already holds %d of %d allowed event streams",
			ErrConnectionBusy, open, limit))})
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
//
// An event that could not be put in a frame is answered by its class rather
// than by which frame failure it was: activity and state are discarded and
// raise the gap, because a gapped consumer reads back what it missed, and
// control ends the stream, because nothing gives it back. A connection that
// failed ends the stream too, because there is nothing left to deliver on.
func (c *conn) pump(id uint64, sub *Subscription) {
	for {
		e, err := sub.Recv(c.ctx)
		if err != nil {
			c.finishStream(id, err)
			return
		}
		event := e
		err = c.w.write(frame{ID: id, Event: &event})
		switch {
		case err == nil:
		case !unbuildable(err):
			// The connection failed under the write, so there is nothing left
			// to deliver on and nowhere to report that.
			c.endStream(id, ErrStreamClosed)
			return
		case event.Class() == ClassControl:
			// Control may not be discarded, here as much as in the queue: no
			// read gives a consumer back an event about the channel, so
			// dropping one would be the invariant relaxed in the one path
			// nobody looks at. The stream ends with a reason instead, and
			// attaching again reconciles.
			c.finishStream(id, errUndeliverableControl)
			return
		default:
			// Activity and state are recoverable, so an event that could not
			// be put in a frame is a discard like any other: the gap is
			// raised, the consumer reconciles from a full read, and the stream
			// carries on.
			sub.discard()
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
