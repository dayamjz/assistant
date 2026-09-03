package ipc_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
)

// stalledConn is a connection whose peer has stopped reading: a write blocks
// until the test lets it go, which is what a full send buffer does to the
// caller whose frame is being written.
type stalledConn struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	closed  chan struct{}
}

func newStalledConn() *stalledConn {
	return &stalledConn{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (c *stalledConn) Write(p []byte) (int, error) {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
		return len(p), nil
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
}

func (c *stalledConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *stalledConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *stalledConn) LocalAddr() net.Addr              { return dummyAddr{} }
func (c *stalledConn) RemoteAddr() net.Addr             { return dummyAddr{} }
func (c *stalledConn) SetDeadline(time.Time) error      { return nil }
func (c *stalledConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stalledConn) SetWriteDeadline(time.Time) error { return nil }

// TestAStalledWriteDoesNotHoldEveryOtherCaller covers the request half of the
// Client's concurrency contract. One connection carries every caller, so a peer
// that stopped reading blocks the write in progress; what the others must wait
// on is their own context rather than that write.
func TestAStalledWriteDoesNotHoldEveryOtherCaller(t *testing.T) {
	conn := newStalledConn()
	c := ipc.NewClient(conn, ipc.ClientConfig{})
	defer c.Close()

	stuck := make(chan error, 1)
	go func() { stuck <- c.Call(context.Background(), "status", nil, nil) }()
	select {
	case <-conn.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first call never reached the connection")
	}

	// The peer is not reading, so this caller cannot send. It has to come back
	// with its own context's error rather than wait behind the write ahead of
	// it.
	waiting, giveUp := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer giveUp()
	answered := make(chan error, 1)
	go func() { answered <- c.Call(waiting, "health", nil, nil) }()
	select {
	case err := <-answered:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("the second Call = %v, want its own context's error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a caller waited behind a write to a peer that had stopped reading")
	}

	// The stuck caller is the one its context cannot release, so closing the
	// client under it is what does.
	close(conn.release)
	c.Close()
	select {
	case err := <-stuck:
		if !errors.Is(err, ipc.ErrClientClosed) {
			t.Errorf("the stuck Call = %v, want the close that released it", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("closing the client did not release the caller whose write was stuck")
	}
}
