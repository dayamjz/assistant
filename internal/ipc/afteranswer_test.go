package ipc_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
)

// A handler that acts on the connection it is answering on has to let the
// answer go out first, and returning is not enough: the answer is written
// after the handler returns. Request.AfterAnswer is that ordering, and this
// holds it: the deferred work sees the write already done.
//
// Nothing here reads the source. The write is observed on the connection the
// server was given, which is where the ordering is either true or not.
func TestWorkDeferredToAfterTheAnswerRunsAfterItIsWritten(t *testing.T) {
	var written atomic.Bool
	saw := make(chan bool, 1)
	handler := ipc.HandlerFunc(func(_ context.Context, req ipc.Request) (json.RawMessage, error) {
		req.AfterAnswer(func() {
			select {
			case saw <- written.Load():
			default:
			}
		})
		return json.RawMessage(`{"ok":true}`), nil
	})
	client := serveThrough(t, handler, &written)

	ctx, cancel := callCtx(t)
	defer cancel()
	var out struct {
		OK bool `json:"ok"`
	}
	if err := client.Call(ctx, ipc.MethodHealth, nil, &out); err != nil {
		t.Fatalf("calling the server: %v", err)
	}
	if !out.OK {
		t.Fatal("the answer did not arrive")
	}
	select {
	case ordered := <-saw:
		if !ordered {
			t.Fatal("the deferred work ran before the answer was written to the connection")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the deferred work never ran")
	}
}

// A request this package did not build has nowhere to defer to, so the work
// runs at once rather than being dropped. A handler called directly by a test
// gets the work done and not the ordering, which is the honest answer.
func TestWorkDeferredOnARequestNoServerBuiltRunsAtOnce(t *testing.T) {
	ran := false
	ipc.Request{Method: ipc.MethodHealth}.AfterAnswer(func() { ran = true })
	if !ran {
		t.Fatal("work deferred on a request no server built was dropped")
	}
}

// serveThrough serves handler over a local socket whose writes are recorded,
// and returns a client connected to it.
func serveThrough(t *testing.T, handler ipc.Handler, written *atomic.Bool) *ipc.Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "ipc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")

	srv, err := ipc.NewServer(ipc.ServerConfig{
		Handler:  handler,
		Ancestry: &recordingAncestry{},
		Events:   publisher(t, ipc.PublisherConfig{}),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	listener := &watchedListener{Listener: listenLocal(t, socket), written: written}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after Close")
		}
	})

	ctx, cancel := callCtx(t)
	defer cancel()
	client, err := ipc.Dial(ctx, socket, ipc.ClientConfig{})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// watchedListener hands out connections that record having been written to, so
// a test observes the write itself rather than an account of it.
type watchedListener struct {
	net.Listener
	written *atomic.Bool
}

func (l *watchedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &watchedConn{Conn: conn, written: l.written}, nil
}

type watchedConn struct {
	net.Conn
	written *atomic.Bool
}

func (c *watchedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Store(true)
	return n, err
}
