//go:build unix

package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
)

// unjoinedHost is a session.Host whose subordinate set reads empty until
// its Delegate returns, and whose Delegate returns only once released: the
// subordinate's process has started and been given its instructions, and
// the service cannot yet place it, which is the window between a provider
// starting the process and joining it to its set, held open for as long as
// a test needs.
type unjoinedHost struct {
	session.Host
	release  chan struct{}
	mu       sync.Mutex
	returned bool
	empty    *session.Subordinates
}

func (u *unjoinedHost) Delegate(ctx context.Context, spec session.SubordinateSpec) (*session.Session, error) {
	s, err := u.Host.Delegate(ctx, spec)
	<-u.release
	u.mu.Lock()
	u.returned = true
	u.mu.Unlock()
	return s, err
}

func (u *unjoinedHost) Subordinates() *session.Subordinates {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.returned {
		return u.empty
	}
	return u.Host.Subordinates()
}

// shellCall is the line a stand-in worker runs as one ipc call from its own
// process: this test binary's ipc-call verb over the socket, with the
// answer's exit status and wire code written to files under dir.
func shellCall(t *testing.T, socket, dir string, method ipc.Method, params string) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	return "run-in-shell: " + quote(bin) + " ipc-call " + quote(socket) + " " + string(method) + " " + quote(params) +
		" > " + quote(filepath.Join(dir, "out")) + " 2> " + quote(filepath.Join(dir, "err")) +
		"; echo $? > " + quote(filepath.Join(dir, "code.tmp")) + " && mv " + quote(filepath.Join(dir, "code.tmp")) + " " + quote(filepath.Join(dir, "code"))
}

// shellAnswer waits for a shellCall's exit status and returns it with the
// wire code it wrote. A refusal, status 1, is the wire error callMain
// writes as JSON, and its message, the refusal's reason, is logged at the
// caller's line; it fails the test when it is not one. Any other status
// returns no code and logs what the call wrote to standard error.
func shellAnswer(t *testing.T, dir string) (int, ipc.Code) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		raw, err := os.ReadFile(filepath.Join(dir, "code"))
		if err == nil {
			status, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			stderr, _ := os.ReadFile(filepath.Join(dir, "err"))
			if status != 1 {
				t.Logf("the subordinate's call exited %d, writing %q to standard error", status, stderr)
				return status, ""
			}
			var wire ipc.Error
			if err := json.Unmarshal(stderr, &wire); err != nil || wire.Code == "" {
				t.Fatalf("the subordinate's call exited as refused and its standard error is not a wire error with a code (%v): %q", err, stderr)
			}
			t.Logf("the subordinate's call was refused with %q: %s", wire.Code, wire.Message)
			return status, wire.Code
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the subordinate's call never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A subordinate coordinator's very first turn cannot act as the first
// coordinator: its instructions tell it to run domain.end as its first
// command, and it runs that while its process has started and has not yet
// joined the subordinate set, which the host here holds open. The call is
// refused unavailable and the delegation stands, because placement refuses
// an unplaced peer while a subordinate launch is in flight. Once the start
// returns and the process has joined, the same call from it is refused as
// the subordinate's, the positive control that the refusal is placement's.
// With the in-flight refusal removed, the first call is placed as the
// coordinator by the empty set and ends the delegation.
func TestASubordinatesFirstTurnIsNeverPlacedAsTheCoordinator(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	h, records := loopHomeConfigured(t, `{"supervision_beacon_interval": "1s"}`)
	l := openLoop(t, h, records, newScriptedTerminals(), nil)
	defer func() { _ = l.service.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = l.service.Serve(ctx) }()

	agent := standin.NewWorker(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "run-in-shell: ", Run: true}},
	}}, agents.WithTerminationGrace(2*time.Second))
	host := &unjoinedHost{Host: session.NewPipe(h, agent.Runner()), release: make(chan struct{}), empty: session.NewSubordinates()}
	l.service.mu.Lock()
	l.service.built = &driver{sessions: host}
	l.service.mu.Unlock()

	d, err := l.records(t).Delegate(ctx, store.DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.records(t).BeginDelegationAttempt(ctx, d.ID, 0); err != nil {
		t.Fatal(err)
	}
	dirFirst, dirLater := t.TempDir(), t.TempDir()
	end := `{"domain":"alpha"}`
	domainDir, err := h.Domain("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.service.writeDomainInstructions(domainDir, "alpha",
		shellCall(t, l.service.Socket(), dirFirst, ipc.MethodDomainEnd, end)+"\n"); err != nil {
		t.Fatal(err)
	}
	launched := make(chan error, 1)
	go func() { launched <- l.service.launchSubordinate(ctx, d, 1) }()

	status, code := shellAnswer(t, dirFirst)
	if status != 1 || code != ipc.CodeUnavailable {
		t.Fatalf("the first turn's domain.end exited %d with %q, want refused %q", status, code, ipc.CodeUnavailable)
	}
	if latest, err := l.records(t).DelegationByID(ctx, d.ID); err != nil || !latest.Standing() {
		t.Fatalf("after the first turn the delegation reads %+v (%v), want it standing", latest, err)
	}
	close(host.release)
	if err := <-launched; err != nil {
		t.Fatalf("the launch: %v", err)
	}
	_, sub, held := l.service.subordinateSessionOf("alpha")
	if !held {
		t.Fatal("the service holds no session for the subordinate")
	}
	if err := host.Send(ctx, sub, shellCall(t, l.service.Socket(), dirLater, ipc.MethodDomainEnd, end)); err != nil {
		t.Fatal(err)
	}
	if status, code := shellAnswer(t, dirLater); status != 1 || code != ipc.CodeSubordinate {
		t.Fatalf("the joined subordinate's domain.end exited %d with %q, want refused %q", status, code, ipc.CodeSubordinate)
	}
	if latest, err := l.records(t).DelegationByID(ctx, d.ID); err != nil || !latest.Standing() {
		t.Fatalf("the delegation reads %+v (%v), want it standing", latest, err)
	}
}
