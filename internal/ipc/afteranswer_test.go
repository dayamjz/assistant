package ipc_test

import (
	"context"
	"encoding/json"
	"errors"
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

// An answer that reached the caller is the answer even when the connection
// ends straight after it. That is the shape a service stopping itself
// produces: its stop is answered, and the work deferred to after the answer
// ends the service, which closes the connection. The caller must read the
// answer it was sent rather than the end of input that followed it.
//
// The ending is the server's own: the deferred work closes the connection
// the server accepted, as Server.Close does to every connection it holds, so
// what the client reads on the wire is a whole answer and then the end of
// input, which is all the real mechanism can put there. It is repeated
// because the defect it guards was an unordered choice between the two, and
// one attempt would pass by chance about half the time it ran.
func TestAnAnswerFollowedByTheConnectionEndingIsStillTheAnswer(t *testing.T) {
	dir, err := os.MkdirTemp("", "ipc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")

	accepted := make(chan net.Conn, 1)
	ended := make(chan struct{}, 1)
	handler := ipc.HandlerFunc(func(_ context.Context, req ipc.Request) (json.RawMessage, error) {
		req.AfterAnswer(func() {
			_ = (<-accepted).Close()
			ended <- struct{}{}
		})
		return json.RawMessage(`{"ok":true}`), nil
	})
	srv, err := ipc.NewServer(ipc.ServerConfig{
		Handler:  handler,
		Ancestry: &recordingAncestry{},
		Events:   publisher(t, ipc.PublisherConfig{}),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	listener := &handingListener{Listener: listenLocal(t, socket), accepted: accepted}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = listener.Close()
		<-done
	})

	const attempts = 200
	lost := 0
	for range attempts {
		ctx, cancel := callCtx(t)
		client, err := ipc.Dial(ctx, socket, ipc.ClientConfig{})
		if err != nil {
			cancel()
			t.Fatalf("Dial: %v", err)
		}
		var out struct {
			OK bool `json:"ok"`
		}
		err = client.Call(ctx, ipc.MethodHealth, nil, &out)
		// The next connection is accepted only once this one has ended, so
		// each attempt's deferred work ends the connection it answered on.
		<-ended
		cancel()
		_ = client.Close()
		switch {
		case errors.Is(err, ipc.ErrClientClosed):
			lost++
		case err != nil:
			t.Fatalf("calling the server: %v", err)
		case !out.OK:
			t.Fatal("the call succeeded with an answer that never arrived")
		}
	}
	if lost > 0 {
		t.Fatalf("%d of %d answers that were written before the connection ended were reported as the connection ending", lost, attempts)
	}
}

// handingListener passes each connection it accepts to the test, so the test
// can end one the way the server ends every connection it holds. The test
// takes each before the next is dialled.
type handingListener struct {
	net.Listener
	accepted chan net.Conn
}

func (l *handingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.accepted <- conn
	return conn, nil
}
