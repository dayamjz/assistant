package service

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
)

// coordinatorRequest is a request whose peer is this test process, as the
// kernel identifies it over a connected local socket, the way the service
// identifies every client. The package's TestMain runs the tests in the
// foreground of a terminal (standin.AtTerminal), so the coordinator's
// methods place this process as a shell at a terminal: the coordinator.
// A zero ipc.Request is not that: its peer reads as process 0, which has
// no terminal above it and is refused the coordinator's methods as
// unplaced. The peer is read once, since it is the same process for every
// test; a socket that cannot be made fails the test that asked.
func coordinatorRequest(tb testing.TB) ipc.Request {
	tb.Helper()
	req, err := thisProcess()
	if err != nil {
		tb.Fatalf("identifying this process as a socket peer: %v", err)
	}
	return req
}

// thisProcess makes the one request coordinatorRequest hands out.
var thisProcess = sync.OnceValues(func() (ipc.Request, error) {
	dir, err := standin.TempDir("peer")
	if err != nil {
		return ipc.Request{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	listener, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		return ipc.Request{}, err
	}
	defer func() { _ = listener.Close() }()
	client, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		return ipc.Request{}, err
	}
	defer func() { _ = client.Close() }()
	server, err := listener.Accept()
	if err != nil {
		return ipc.Request{}, err
	}
	defer func() { _ = server.Close() }()
	peer := ipc.Identify(server)
	if _, err := peer.Credentials(); err != nil {
		return ipc.Request{}, err
	}
	return ipc.Request{Peer: peer}, nil
})
