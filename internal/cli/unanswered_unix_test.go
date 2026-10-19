//go:build linux || darwin

package cli_test

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// unansweringService serves the protocol on a home's socket and answers a
// readiness check, and when it is asked to stop it ends that request's
// connection without answering. That is the shape a service produces when it
// reads a stop and then ends before writing the answer, which an operating
// system's stop arriving at the same moment can cause. When down is set it
// also lets go of the socket, the way a service that ended does; when it is
// not, it goes on answering.
type unansweringService struct {
	mu       sync.Mutex
	conns    []net.Conn
	listener net.Listener
	down     bool
}

// serveUnanswering starts one for h and stops it when the test ends.
func serveUnanswering(t *testing.T, h *home.Home, down bool) {
	t.Helper()
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	health, err := json.Marshal(machine.Health{Ready: true, Home: h.Root(), Build: build, Instance: "the only one"})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", h.Socket())
	if err != nil {
		t.Fatalf("binding the home's socket: %v", err)
	}
	s := &unansweringService{listener: listener, down: down}
	events, err := ipc.NewPublisher(ipc.PublisherConfig{})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := ipc.NewServer(ipc.ServerConfig{
		Handler: ipc.HandlerFunc(func(ctx context.Context, req ipc.Request) (json.RawMessage, error) {
			if req.Method != ipc.MethodServiceStop && req.Method != ipc.MethodServiceRestart {
				return health, nil
			}
			s.end()
			<-ctx.Done()
			return nil, ctx.Err()
		}),
		Ancestry: anyAncestry{},
		Events:   events,
	})
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(s) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = listener.Close()
		<-served
		_ = events.Close()
	})
}

// Accept hands the server each connection and keeps it, so a stop can end
// the connection it arrived on.
func (s *unansweringService) Accept() (net.Conn, error) {
	conn, err := s.listener.Accept()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.conns = append(s.conns, conn)
	s.mu.Unlock()
	return conn, nil
}

func (s *unansweringService) Close() error   { return s.listener.Close() }
func (s *unansweringService) Addr() net.Addr { return s.listener.Addr() }

// end closes every connection taken so far, which includes the one the stop
// arrived on, and when the service is going down the socket too. Nothing is
// written on those connections first, so a caller reads the end of input.
func (s *unansweringService) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, conn := range s.conns {
		_ = conn.Close()
	}
	if s.down {
		_ = s.listener.Close()
	}
}

// anyAncestry contains nobody, which is what a caller outside every stage is.
type anyAncestry struct{}

func (anyAncestry) Contained(context.Context, ipc.Credentials) (ipc.Containment, error) {
	return ipc.Containment{}, nil
}

// A stop whose connection ended with no answer is decided by what the
// service then does. A service that let go of the home is a stop that went
// through, reported as the stopping row with a success exit; a service that
// goes on answering is the ended connection reported as the failure it is,
// which is also the control showing the first half is decided by observation
// and not by the ended connection being taken as success.
func TestAStopWithNoAnswerIsDecidedByWhetherTheServiceStoppedAnswering(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	t.Run("the service went down", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		serveUnanswering(t, h, true)
		got := run(t, h, "", "service", "stop")
		if got.code != machine.ExitOK || !strings.Contains(got.stdout, machine.LifecyclePhrase(machine.Lifecycle{Accepted: true}).String()) {
			t.Fatalf("a stop the service ended under exited %s, want success and the stopping row:\n%s%s", got.code, got.stdout, got.stderr)
		}
	})
	t.Run("the service is still up", func(t *testing.T) {
		t.Parallel()
		h := newHome(t)
		serveUnanswering(t, h, false)
		got := run(t, h, "", "service", "stop")
		if got.code != machine.ExitFailure || !strings.Contains(got.stderr, ipc.ErrClientClosed.Error()) ||
			!strings.Contains(got.stderr, "still answering") {
			t.Fatalf("a stop with no answer from a service still up exited %s, want a failure naming the ended connection:\n%s%s",
				got.code, got.stdout, got.stderr)
		}
	})
}

// A restart whose connection ended with no answer is not taken as a restart
// unless a different service answers. Here the one asked goes on answering,
// so the ended connection is the failure.
func TestARestartWithNoAnswerFromAServiceStillUpIsAFailure(t *testing.T) {
	requiresIdentifiedPeer(t)
	t.Parallel()
	h := newHome(t)
	serveUnanswering(t, h, false)
	got := run(t, h, "", "service", "restart")
	if got.code != machine.ExitFailure || !strings.Contains(got.stderr, ipc.ErrClientClosed.Error()) {
		t.Fatalf("a restart with no answer exited %s, want a failure naming the ended connection:\n%s%s", got.code, got.stdout, got.stderr)
	}
}
