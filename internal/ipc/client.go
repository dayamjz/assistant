package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
)

// ClientConfig is what a client may be given. Every field has a usable
// default, because a client holds no authority: what it may do is decided at
// the service.
type ClientConfig struct {
	// Marker is what this process says about its own environment. It is
	// diagnostic evidence the service may report, never authorization; see
	// MarkerVar. Leaving it empty reads MarkerVar from the environment.
	Marker Marker
	// Backlog is the queue depth of each stream this client opens. Zero means
	// DefaultBacklog. The queue is here as well as at the service because the
	// connection can fall behind too, and a consumer that stops reading must
	// gap itself rather than stall the goroutine that also carries this
	// client's replies.
	Backlog int
	// MaxFrameBytes bounds one frame in either direction. Zero means
	// DefaultMaxFrameBytes.
	MaxFrameBytes int
}

// Client is one connection to the service.
//
// It is safe for concurrent use. Replies are matched to calls by identifier, so
// a long call does not hold up a short one on the same connection, and a stream
// does not hold up either. Requests share one connection, so a peer that has
// stopped reading blocks whichever caller's write is already in progress, and
// what every other caller then waits on is its own context rather than that
// write.
//
// The caller whose write is in progress is the one its context cannot release,
// because abandoning a write midway would leave a partial line on a connection
// both sides read by line. Close, which closes the connection under that write,
// is what releases it.
type Client struct {
	conn    net.Conn
	w       *frameWriter
	marker  Marker
	backlog int

	// ctx ends when this client does. It is what the writes this client makes
	// for its own bookkeeping, which have no caller's context to wait under,
	// wait on instead.
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	next    uint64
	calls   map[uint64]chan frame
	streams map[uint64]*Subscription
	closed  bool
	cause   error

	done chan struct{}
}

// Dial connects to the service listening on a local socket.
func Dial(ctx context.Context, socket string, cfg ClientConfig) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("ipc: dialing %s: %w", socket, err)
	}
	return NewClient(conn, cfg), nil
}

// NewClient serves the protocol over an established connection. The client
// owns the connection from here and closes it with Close.
func NewClient(conn net.Conn, cfg ClientConfig) *Client {
	marker := cfg.Marker
	if marker == "" {
		marker = LocalMarker(os.Getenv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{
		conn:    conn,
		w:       newFrameWriter(conn, cfg.MaxFrameBytes),
		ctx:     ctx,
		cancel:  cancel,
		marker:  marker,
		backlog: cfg.Backlog,
		calls:   make(map[uint64]chan frame),
		streams: make(map[uint64]*Subscription),
		done:    make(chan struct{}),
	}
	go c.read(newFrameReader(conn, cfg.MaxFrameBytes))
	return c
}

// Call sends a request and waits for its answer. The answer is decoded into
// out, which may be nil when the caller wants only the error.
//
// A failure the service reported comes back as an *Error, and errors.Is
// matches it against the sentinel its code names.
func (c *Client) Call(ctx context.Context, method Method, params, out any) error {
	spec, ok := Lookup(method)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownMethod, method)
	}
	if spec.Kind == KindStream {
		return fmt.Errorf("%w: %q opens a stream, use Subscribe", ErrInvalidRequest, method)
	}
	body, err := encodeParams(params)
	if err != nil {
		return err
	}
	id, reply, err := c.register()
	if err != nil {
		return err
	}
	defer c.unregister(id)
	if err := c.w.write(ctx, frame{ID: id, Method: method, Marker: c.marker, Params: body}); err != nil {
		return fmt.Errorf("ipc: sending %s: %w", method, err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.closedErr()
	case f := <-reply:
		if f.Error != nil {
			return f.Error
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(nullIfEmpty(f.Result), out); err != nil {
			return fmt.Errorf("ipc: decoding the answer to %s: %w", method, err)
		}
		return nil
	}
}

// Stream is an open subscription to the service's events.
type Stream struct {
	id  uint64
	c   *Client
	sub *Subscription
}

// Subscribe opens the event stream. It returns once the service has
// acknowledged the subscription, so a refusal is reported here rather than at
// the first delivery.
//
// The stream opens gapped: its first delivery is a gap marker, and a consumer
// must reconcile before applying a delta.
func (c *Client) Subscribe(ctx context.Context, params any) (*Stream, error) {
	body, err := encodeParams(params)
	if err != nil {
		return nil, err
	}
	id, reply, err := c.register()
	if err != nil {
		return nil, err
	}
	// The stream this queue relays opens gapped at the service, and that
	// marker arrives ahead of everything else on it, so this queue starts
	// ungapped and raises a marker only for what it discards itself.
	sub := newSubscription(backlogOr(c.backlog), false)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.unregister(id)
		return nil, c.closedErr()
	}
	c.streams[id] = sub
	c.mu.Unlock()
	if err := c.w.write(ctx, frame{ID: id, Method: MethodEventsSubscribe, Marker: c.marker, Params: body}); err != nil {
		c.unregister(id)
		// A frame refused before it was written leaves nothing at the service,
		// and a frame the connection failed under may have arrived whole. The
		// cancel costs one small frame and covers the second case.
		c.release(id, ErrStreamClosed)
		return nil, fmt.Errorf("ipc: opening the event stream: %w", err)
	}
	defer c.unregister(id)
	select {
	case <-ctx.Done():
		// The request is already on the wire, so the service may have opened
		// the stream and be pumping it at a queue nothing will read again.
		// Abandoning it here without saying so would leave that queue, its
		// goroutine, and its share of the connection's writer in place for as
		// long as the connection lives.
		c.release(id, ErrStreamClosed)
		return nil, ctx.Err()
	case <-c.done:
		return nil, c.closedErr()
	case f := <-reply:
		if f.Error != nil {
			// The service refused, so it opened nothing to cancel.
			c.dropStream(id, ErrStreamClosed)
			return nil, f.Error
		}
		return &Stream{id: id, c: c, sub: sub}, nil
	}
}

// Recv returns the next event, waiting until one arrives, the stream ends, or
// ctx is done. It applies the same overflow policy as the service: activity
// first, then state collapsed into a gap marker, and control never discarded.
func (s *Stream) Recv(ctx context.Context) (Event, error) { return s.sub.Recv(ctx) }

// Close detaches the stream and tells the service to stop sending it.
//
// It returns nothing because neither half can fail in a way a caller could act
// on: detaching is local, and the cancel that follows it is best effort in the
// sense release describes. That cancel is attempted rather than awaited, so the
// stream slot it frees at the service frees when the cancel arrives rather than
// by the time this returns, and a caller already at ServerConfig.MaxStreams may
// have to ask again.
func (s *Stream) Close() {
	s.c.release(s.id, ErrStreamClosed)
}

// release detaches a stream this client will not read and tells the service to
// stop sending it. Every path that leaves a stream open at the service comes
// through here, so the cancel is never forgotten beside the detachment; the two
// places that detach without it are the ones where the service has already let
// the stream go, and each says so.
//
// Detaching is not optional and happens here, before this returns: from that
// point the client routes nothing to that stream.
//
// The cancel is optional, and best effort means something specific. One cancel
// frame is attempted, on a goroutine of its own, so no caller and not the
// goroutine reading this connection ever waits for the connection's writer to
// come free. That attempt gives up when this client ends, which is the point at
// which there is nothing left to cancel, because the service releases every
// stream a connection held when that connection goes. A cancel that never got
// through therefore costs one subscription and one pump goroutine at the
// service until the connection ends, and nothing after that.
func (c *Client) release(id uint64, cause error) {
	c.dropStream(id, cause)
	go func() {
		_ = c.w.write(c.ctx, frame{ID: id, Cancel: true})
	}()
}

// Close ends the connection. Calls waiting on an answer and streams waiting on
// an event are released with ErrClientClosed.
func (c *Client) Close() error {
	c.finish(ErrClientClosed)
	return c.conn.Close()
}

// read demultiplexes the connection until it ends.
//
// The service reports a frame it would not serve with reservedID, because such
// a frame has no identifier of its own to answer. That report is about the
// connection rather than about any request, so it ends nothing by itself; it is
// kept as the reason this client ended if the connection then does end, in
// place of the end of input a caller would otherwise be left with.
func (c *Client) read(r *frameReader) {
	// reported is the last thing the service said about a frame of this
	// client's it could not read. It is only ever read and written here.
	var reported error
	for {
		f, err := r.read()
		if err != nil {
			cause := err
			if reported != nil {
				cause = reported
			}
			c.finish(fmt.Errorf("%w: %w", ErrClientClosed, cause))
			return
		}
		switch {
		case f.Event != nil:
			c.mu.Lock()
			sub := c.streams[f.ID]
			c.mu.Unlock()
			if sub != nil && !sub.deliver(*f.Event) {
				// The relay queue ended under a delivery it could not make
				// room for. The service is still sending, so it is told to
				// stop rather than left pumping a stream this client has
				// stopped reading, which is the same leak an abandoned
				// Subscribe would leave.
				c.release(f.ID, ErrStreamClosed)
			}
		case f.Done:
			cause := ErrStreamClosed
			if f.Error != nil {
				cause = f.Error
			}
			c.dropStream(f.ID, cause)
		case f.ID == reservedID:
			// reservedID answers no request, and register never allocates it,
			// so a frame carrying it is the service's report about the
			// connection itself rather than an answer to anything asked here.
			if f.Error != nil {
				reported = f.Error
			}
		default:
			c.mu.Lock()
			reply := c.calls[f.ID]
			c.mu.Unlock()
			if reply != nil {
				select {
				case reply <- f:
				default:
				}
			}
			// An answer naming a request this client is no longer waiting for
			// is a late answer to a call whose context ended, which is ordinary
			// and disturbs nothing else on the connection. It is dropped.
		}
	}
}

// register allocates an identifier and a place to put its answer. It counts up
// from one, so it never hands out reservedID, which answers no request.
func (c *Client) register() (uint64, chan frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, nil, c.cause
	}
	c.next++
	id := c.next
	reply := make(chan frame, 1)
	c.calls[id] = reply
	return id, reply, nil
}

func (c *Client) unregister(id uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.calls, id)
}

// dropStream detaches a stream and ends it with cause.
func (c *Client) dropStream(id uint64, cause error) {
	c.mu.Lock()
	sub := c.streams[id]
	delete(c.streams, id)
	c.mu.Unlock()
	if sub != nil {
		sub.finish(cause)
	}
}

// finish releases everything waiting, keeping the first cause.
func (c *Client) finish(cause error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.cause = cause
	subs := make([]*Subscription, 0, len(c.streams))
	for id, sub := range c.streams {
		subs = append(subs, sub)
		delete(c.streams, id)
	}
	c.calls = make(map[uint64]chan frame)
	c.mu.Unlock()
	c.cancel()
	for _, sub := range subs {
		sub.finish(cause)
	}
	close(c.done)
}

func (c *Client) closedErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cause != nil {
		return c.cause
	}
	return ErrClientClosed
}

// encodeParams renders a request body, leaving an already-encoded one alone.
func encodeParams(params any) (json.RawMessage, error) {
	switch v := params.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return v, nil
	}
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding parameters: %w", ErrInvalidRequest, err)
	}
	return body, nil
}

// backlogOr resolves a queue depth, refusing nothing: a client's own backlog
// is not a decision the service depends on.
func backlogOr(n int) int {
	if n <= 0 {
		return DefaultBacklog
	}
	return n
}
