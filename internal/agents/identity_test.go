package agents_test

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// requiresIdentityRead skips a test on a platform where internal/agents has
// no process start-time read, saying so in the terms a reader deciding
// whether to trust a green run needs: nothing about identities is
// established there, and a pass would have read nothing.
func requiresIdentityRead(t *testing.T) {
	t.Helper()
	if _, _, err := agents.Identify(os.Getpid()); errors.Is(err, agents.ErrIdentityUnreadable) {
		t.Skipf("%s has no process start-time read, so no identity is established here: %v", runtime.GOOS, err)
	}
}

// TestIdentifyNamesARunningProcessAndProvesAbsence pins the identity read on
// each platform the module targets: this process reads as found with a start
// time stable across two reads, a child that has exited and been reaped
// reads as absent rather than as an error, and a Presence over this
// process's identity answers present while the same identifier with another
// start time answers replaced, which is the PID-reuse check every record
// rests on.
func TestIdentifyNamesARunningProcessAndProvesAbsence(t *testing.T) {
	requiresIdentityRead(t)
	self, found, err := agents.Identify(os.Getpid())
	if err != nil || !found {
		t.Fatalf("Identify(this process) = %+v, %v, %v, want found", self, found, err)
	}
	if self.PID != os.Getpid() || self.Start == 0 {
		t.Fatalf("this process's identity is %+v, want its identifier and a non-zero start time", self)
	}
	again, _, err := agents.Identify(os.Getpid())
	if err != nil || again != self {
		t.Fatalf("a second read answered %+v, %v, want %+v again", again, err, self)
	}

	presence, err := self.Presence()
	if err != nil || presence != agents.PresencePresent {
		t.Fatalf("this process's presence = %s, %v, want %s", presence, err, agents.PresencePresent)
	}
	// A reused identifier is the same number with another start time. No
	// test can make the operating system reuse this process's identifier,
	// so the counterfeit is a mutation of the real reading, which is the one
	// shape a reuse has.
	reused := agents.Identity{PID: self.PID, Start: self.Start + 1}
	presence, err = reused.Presence()
	if err != nil || presence != agents.PresenceReplaced {
		t.Fatalf("a mutated start time reads %s, %v, want %s", presence, err, agents.PresenceReplaced)
	}
	if _, err := (agents.Identity{PID: self.PID}).Presence(); !errors.Is(err, agents.ErrIdentityUnreadable) {
		t.Fatalf("an identity with no start time was compared: %v, want ErrIdentityUnreadable", err)
	}

	child := exec.Command(helperBinary(t))
	child.Env = []string{helperModeVar + "=envelope", helperResultVar + "=done"}
	child.Dir = os.TempDir()
	if err := child.Run(); err != nil {
		t.Fatalf("running the stand-in to completion: %v", err)
	}
	// The child has been waited for, so its identifier names no running
	// process, unless the operating system has already given it to another
	// process, which reads as replaced; either is the absence of the child.
	gone := agents.Identity{PID: child.Process.Pid, Start: 1}
	presence, err = gone.Presence()
	if err != nil {
		t.Fatalf("a reaped child's presence could not be read: %v", err)
	}
	if presence == agents.PresencePresent {
		t.Fatalf("a reaped child reads present under a start time nothing started at")
	}
}
