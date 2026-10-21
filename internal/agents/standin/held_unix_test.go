//go:build linux || darwin

package standin

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// heldRun is a held process startIdentifiedHeld started: exited is closed
// once it has been waited for, with its error then in err.
type heldRun struct {
	cmd    *exec.Cmd
	exited chan struct{}
	err    error
}

// startIdentifiedHeld starts a held process that records its identity, the
// way a test's shell does, and returns it, its release file and the
// identity it recorded once it has. The process is killed in cleanup if it
// is still running.
func startIdentifiedHeld(t *testing.T) (*heldRun, string, agents.Identity) {
	t.Helper()
	dir := t.TempDir()
	release, identity := filepath.Join(dir, "release"), filepath.Join(dir, "identity")
	cmd := exec.Command("/bin/sh", "-c", "exec "+HeldWaitIdentified(t, release, identity))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := &heldRun{cmd: cmd, exited: make(chan struct{})}
	go func() { r.err = cmd.Wait(); close(r.exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-r.exited
	})
	for deadline := time.Now().Add(30 * time.Second); ; {
		id, err := ReadHeldIdentity(identity)
		if err == nil {
			return r, release, id
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the held process never recorded its identity: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A held process records its own identity, the identifier and start time
// agents.Identify reads of it, and a Process built from that record is
// signalled only while that identifier still has that start time (P15's
// "ends with its test" without signalling anything else). The stale case is
// the shape a reused identifier takes: the identifier names a running
// process whose start time is not the recorded one, and End sends it
// nothing. A released held process exits 0, after which its recorded
// identity names nothing and End sends nothing either.
func TestAHeldProcessIsSignalledOnlyByTheIdentityItRecorded(t *testing.T) {
	t.Parallel()
	held, _, id := startIdentifiedHeld(t)
	if id.PID != held.cmd.Process.Pid {
		t.Fatalf("the held process recorded identifier %d, but it is process %d", id.PID, held.cmd.Process.Pid)
	}
	now, found, err := agents.Identify(id.PID)
	if err != nil || !found || now != id {
		t.Fatalf("the held process recorded %+v, and reads as %+v (found %v, %v)", id, now, found, err)
	}

	stale := Process{Identity: agents.Identity{PID: id.PID, Start: id.Start + 1}}
	if stale.End() {
		t.Fatal("End killed a process whose start time is not the one recorded")
	}
	// A signal is delivered after the call that sends it returns, so the
	// process is watched for a while rather than read once.
	if (Process{Identity: id}).GoneWithin(2 * time.Second) {
		t.Fatal("after End on a stale identity the held process went; End signalled it")
	}
	if presence, err := id.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("after End on a stale identity the held process reads %q, %v; want it untouched", presence, err)
	}

	(Process{Identity: id}).End()
	select {
	case <-held.exited:
		if held.err == nil {
			t.Fatal("the held process ended by its recorded identity exited 0, as if released")
		}
	case <-time.After(EndGrace + 5*time.Second):
		t.Fatal("the held process was still running after End on the identity it recorded")
	}

	released, release, releasedID := startIdentifiedHeld(t)
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released.exited:
		if released.err != nil {
			t.Fatalf("the released held process exited with %v, want 0", released.err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the released held process never exited")
	}
	if (Process{Identity: releasedID}).End() {
		t.Fatal("End signalled the identity of a held process that had already exited")
	}
}

// ReadHeldIdentity hands back nothing a caller could signal unless the
// record holds both an identifier and a start time: a missing record and
// each partial one is an error with a zero identity.
func TestReadHeldIdentityRefusesAPartialRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if id, err := ReadHeldIdentity(filepath.Join(dir, "absent")); err == nil || id != (agents.Identity{}) {
		t.Fatalf("a missing record read as %+v, %v", id, err)
	}
	for _, record := range []string{"", "123", "123 0", "0 5", "-4 5", "x 5", "123 y", "123 5 6"} {
		path := filepath.Join(dir, "record")
		if err := os.WriteFile(path, []byte(record+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if id, err := ReadHeldIdentity(path); err == nil || id != (agents.Identity{}) {
			t.Errorf("record %q read as %+v, %v; want an error and no identity", record, id, err)
		}
	}
	path := filepath.Join(dir, "whole")
	if err := os.WriteFile(path, []byte("123 456\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id, err := ReadHeldIdentity(path); err != nil || id != (agents.Identity{PID: 123, Start: 456}) {
		t.Fatalf("a whole record read as %+v, %v", id, err)
	}
}
