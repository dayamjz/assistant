//go:build windows

package agents_test

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
)

// createNewProcessGroup is CREATE_NEW_PROCESS_GROUP, spelled here as the
// adapter spells it, so the stand-in is started the way an agent is.
const createNewProcessGroup = 0x00000200

// startHeld starts the stand-in in its waiting mode at the root of a new
// process group and returns its identity read now.
func startHeld(t *testing.T) (*exec.Cmd, agents.Identity) {
	t.Helper()
	cmd := exec.Command(helperBinary(t))
	cmd.Env = []string{helperModeVar + "=hold"}
	cmd.Dir = os.TempDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the held stand-in: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	id, found, err := agents.Identify(cmd.Process.Pid)
	if err != nil || !found {
		t.Fatalf("identifying the held stand-in: %+v, %v, %v", id, found, err)
	}
	return cmd, id
}

// TestEndGroupEndsTheLeaderTheIdentityNamesAndNothingElse establishes what
// this platform delivers: the verified leader's tree is ended, a sibling
// started with the same command line is not, and a reused identifier is
// left alone. What is not established here, because the platform offers no
// read of a group's remaining members, is anything about a descendant that
// outlived its leader: a gone leader answers GroupLeaderGone and nothing
// more is claimed.
func TestEndGroupEndsTheLeaderTheIdentityNamesAndNothingElse(t *testing.T) {
	principles.Cite(t, principles.P6)
	target, targetID := startHeld(t)
	sibling, siblingID := startHeld(t)

	reused := agents.Identity{PID: targetID.PID, Start: targetID.Start + 1}
	if end, err := agents.EndGroup(reused, time.Second); err != nil || end != agents.GroupReplaced {
		t.Fatalf("EndGroup(reused identifier) = %s, %v, want %s", end, err, agents.GroupReplaced)
	}
	if presence, err := targetID.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the target was ended on a reused identifier: %s, %v", presence, err)
	}

	end, err := agents.EndGroup(targetID, 2*time.Second)
	if err != nil || end != agents.GroupEnded {
		t.Fatalf("EndGroup(target) = %s, %v, want %s", end, err, agents.GroupEnded)
	}
	_ = target.Wait()
	if presence, err := siblingID.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the sibling, started with the same command line, is %s, %v, want present", presence, err)
	}
	_ = sibling.Process.Kill()
	_ = sibling.Wait()
	if end, err := agents.EndGroup(siblingID, time.Second); err != nil || end != agents.GroupLeaderGone {
		t.Fatalf("EndGroup(gone leader) = %s, %v, want %s", end, err, agents.GroupLeaderGone)
	}
}
