package ipc

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// haltingConn is a connection whose peer takes a few frames and then stops
// reading, which is what a service that has stalled does to whatever this
// client writes next.
type haltingConn struct {
	absorb  int
	blocked chan struct{}
	closed  chan struct{}
	once    sync.Once

	mu     sync.Mutex
	writes int
}

func newHaltingConn(absorb int) *haltingConn {
	return &haltingConn{
		absorb:  absorb,
		blocked: make(chan struct{}, 1),
		closed:  make(chan struct{}),
	}
}

func (c *haltingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writes++
	taken := c.writes <= c.absorb
	c.mu.Unlock()
	if taken {
		return len(p), nil
	}
	select {
	case c.blocked <- struct{}{}:
	default:
	}
	<-c.closed
	return 0, io.ErrClosedPipe
}

func (c *haltingConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *haltingConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *haltingConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (c *haltingConn) RemoteAddr() net.Addr             { return pipeAddr{} }
func (c *haltingConn) SetDeadline(time.Time) error      { return nil }
func (c *haltingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *haltingConn) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "halting" }
func (pipeAddr) String() string  { return "halting" }

// TestAnAbandonedSubscribeDoesNotWaitForItsOwnCancel covers what best effort
// has to mean for the cancel that follows an abandoned Subscribe. The service
// took the request and will never acknowledge it, and it has stopped reading,
// so the cancel cannot go out; the caller still has to come back when its own
// context ends rather than waiting on a frame that is optional.
//
// The stream table is read directly at the end because detaching is the half
// that is not optional and has no other observer: the caller never received a
// Stream, so nothing exported can say whether this client still routes to it.
func TestAnAbandonedSubscribeDoesNotWaitForItsOwnCancel(t *testing.T) {
	// One frame taken, which is the subscribe request, and nothing after it.
	conn := newHaltingConn(1)
	c := NewClient(conn, ClientConfig{})
	defer c.Close()

	abandoned, giveUp := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer giveUp()
	opened := make(chan error, 1)
	go func() {
		_, err := c.Subscribe(abandoned, nil)
		opened <- err
	}()

	select {
	case err := <-opened:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Subscribe = %v, want its own context's error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Subscribe waited on a cancel it is only obliged to attempt")
	}

	// The cancel is still trying, which is the state this is about, and the
	// stream is gone from this client regardless.
	select {
	case <-conn.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancel was never attempted")
	}
	c.mu.Lock()
	live := len(c.streams)
	c.mu.Unlock()
	if live != 0 {
		t.Errorf("this client still routes to %d abandoned streams", live)
	}
}
