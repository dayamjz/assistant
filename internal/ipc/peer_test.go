package ipc_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
)

// platformIdentifiesPeers says whether this build can ask the kernel who opened
// a local socket. It is written down rather than derived from the package, so
// a platform that quietly stopped identifying peers fails here instead of
// being reported as correct.
func platformIdentifiesPeers() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "darwin"
}

// requiresIdentifiedPeer skips a test whose subject is a decision taken on
// kernel-reported credentials, on a platform that reports none. There a
// restricted method is refused for the missing identity before containment is
// ever reached, so the containment behavior this test names does not exist to
// observe. That refusal is not left unchecked:
// TestUnidentifiedPeerIsRefusedForRestrictedMethods holds it on every
// platform, over a connection the kernel cannot be asked about.
//
// The skip cannot hide a regression on a platform that does identify peers,
// because platformIdentifiesPeers is written down rather than derived, and
// TestCredentialsComeFromTheKernel fails there when the kernel stops
// answering.
func requiresIdentifiedPeer(t *testing.T) {
	t.Helper()
	if !platformIdentifiesPeers() {
		t.Skipf("%s reports no local socket peer credentials, so a restricted call is refused before containment is decided", runtime.GOOS)
	}
}

// acceptedPair returns the two ends of a local socket connection inside this
// process, which makes the process identifier and user the kernel should
// report values the test already knows.
func acceptedPair(t *testing.T) (server, client net.Conn) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ipc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	l := listenLocal(t, filepath.Join(dir, "s"))
	defer l.Close()
	type accepted struct {
		conn net.Conn
		err  error
	}
	ch := make(chan accepted, 1)
	go func() {
		c, err := l.Accept()
		ch <- accepted{c, err}
	}()
	client, err = net.Dial("unix", l.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	got := <-ch
	if got.err != nil {
		t.Fatalf("accept: %v", got.err)
	}
	t.Cleanup(func() {
		got.conn.Close()
		client.Close()
	})
	return got.conn, client
}

// TestCredentialsComeFromTheKernel is what makes peer identification authority
// rather than decoration. The expected values are this process's own, which the
// peer never sent and could not have changed.
func TestCredentialsComeFromTheKernel(t *testing.T) {
	server, _ := acceptedPair(t)
	peer := ipc.Identify(server)
	creds, err := peer.Credentials()
	if !platformIdentifiesPeers() {
		if err == nil {
			t.Fatalf("%s reported credentials %s, but this build has no way to ask for them", runtime.GOOS, creds)
		}
		if !errors.Is(err, ipc.ErrUnidentifiedPeer) {
			t.Errorf("Credentials = %v, want ErrUnidentifiedPeer", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if creds.PID != os.Getpid() {
		t.Errorf("PID = %d, want this process's %d", creds.PID, os.Getpid())
	}
	if creds.UID != os.Getuid() {
		t.Errorf("UID = %d, want this process's %d", creds.UID, os.Getuid())
	}
	if !peer.Identified() {
		t.Error("Identified reports false for a peer that produced credentials")
	}
}

// TestATransportWithNoCredentialsRefuses covers the shape the protocol really
// produces when it is carried over something that is not a local socket.
func TestATransportWithNoCredentialsRefuses(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	peer := ipc.Identify(a)
	if peer.Identified() {
		t.Fatal("a pipe was identified")
	}
	creds, err := peer.Credentials()
	if !errors.Is(err, ipc.ErrUnidentifiedPeer) {
		t.Fatalf("Credentials = %v, want ErrUnidentifiedPeer", err)
	}
	if creds != (ipc.Credentials{}) {
		t.Errorf("Credentials returned %s alongside a refusal", creds)
	}
}

func TestLocalMarkerReadsTheEnvironment(t *testing.T) {
	env := map[string]string{"ASSISTANT_STAGE": "run=r1 stage=review"}
	got := ipc.LocalMarker(func(name string) string { return env[name] })
	if got != "run=r1 stage=review" {
		t.Errorf("LocalMarker = %q, want the value of ASSISTANT_STAGE", got)
	}
	if ipc.LocalMarker(func(string) string { return "" }) != "" {
		t.Error("LocalMarker invented a marker")
	}
	if ipc.LocalMarker(nil) != "" {
		t.Error("LocalMarker(nil) did not report an empty marker")
	}
}

func TestCredentialsString(t *testing.T) {
	got := ipc.Credentials{PID: 12, UID: 501, GID: 20}.String()
	if got != "pid 12 uid 501 gid 20" {
		t.Errorf("String = %q", got)
	}
}
