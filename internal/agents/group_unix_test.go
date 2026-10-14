//go:build unix

package agents_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
)

// startHeld starts the stand-in in its waiting mode as the leader of a new
// process group, the way runProcess starts an agent, and returns the group's
// identity read now. The group outlives the test unless ended, so the test
// ends it on cleanup whether or not the assertion that should have did.
func startHeld(t *testing.T) (*exec.Cmd, agents.Identity) {
	t.Helper()
	cmd, id, _ := startHeldReaped(t)
	return cmd, id
}

// startHeldReaped is startHeld that also hands back the channel closed once
// this process has reaped the stand-in, so a test that kills the leader can
// wait for the reap rather than for the identity read to stop finding it.
// The two are not the same moment here: macOS CI has answered signal zero
// to the group of a leader the identity read already counted absent, before
// this process reaped it.
func startHeldReaped(t *testing.T) (*exec.Cmd, agents.Identity, <-chan struct{}) {
	t.Helper()
	requiresIdentityRead(t)
	cmd := exec.Command(helperBinary(t))
	cmd.Env = []string{helperModeVar + "=hold"}
	cmd.Dir = os.TempDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the held stand-in: %v", err)
	}
	// The stand-in is this process's child, so this process is what reaps
	// it; an orphan a recovering service ends is reaped by the system. The
	// wait runs beside the test so a leader EndGroup kills leaves the group
	// the way an orphan would rather than lingering as a zombie in it.
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waited
	})
	id, found, err := agents.Identify(cmd.Process.Pid)
	if err != nil || !found {
		t.Fatalf("identifying the held stand-in: %+v, %v, %v", id, found, err)
	}
	return cmd, id, waited
}

// groupAlive reports whether the group still holds a process this process
// may signal, read the way terminateTree reads it.
func groupAlive(pgid int) bool {
	return !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
}

// TestEndGroupEndsTheGroupTheIdentityNamesAndNothingElse establishes the
// kill path's scope: two groups are started from the same binary with the
// same command line, which is the shape a pattern kill would match both of,
// and ending one by its identity leaves the other running. That the sibling
// was reachable by a pattern is the positive control: its command line is
// the reaped one's, by construction, and the assertion says so.
func TestEndGroupEndsTheGroupTheIdentityNamesAndNothingElse(t *testing.T) {
	target, targetID := startHeld(t)
	sibling, siblingID := startHeld(t)
	if filepath.Base(target.Path) != filepath.Base(sibling.Path) || strings.Join(target.Args, " ") != strings.Join(sibling.Args, " ") {
		t.Fatalf("the two groups were not started alike, so the sibling surviving says nothing about scope: %q and %q", target.Args, sibling.Args)
	}

	end, err := agents.EndGroup(targetID, 2*time.Second)
	if err != nil || end != agents.GroupEnded {
		t.Fatalf("EndGroup(target) = %s, %v, want %s", end, err, agents.GroupEnded)
	}
	// The leader is this test's child, so it may be a zombie until the wait
	// startHeld runs reaps it; the identity read counts that as gone, which
	// is the same answer EndGroup acted on.
	if presence, err := targetID.Presence(); err != nil || presence != agents.PresenceAbsent {
		t.Fatalf("the target leader %d is %s, %v after EndGroup reported its group ended", targetID.PID, presence, err)
	}
	if !groupAlive(siblingID.PID) {
		t.Fatalf("the sibling group %d, started with the same command line, was ended too", siblingID.PID)
	}
	presence, err := siblingID.Presence()
	if err != nil || presence != agents.PresencePresent {
		t.Fatalf("the sibling's presence = %s, %v, want present", presence, err)
	}
}

// TestEndGroupLeavesAReusedIdentifierAlone establishes P6's refusal on the
// kill path: an identity whose identifier is present with another start
// time is a reused number, and nothing is signalled. The counterfeit is a
// mutation of the real reading, since nothing can make the kernel reuse a
// live identifier on demand.
func TestEndGroupLeavesAReusedIdentifierAlone(t *testing.T) {
	principles.Cite(t, principles.P6)
	_, id := startHeld(t)
	reused := agents.Identity{PID: id.PID, Start: id.Start + 1}
	end, err := agents.EndGroup(reused, time.Second)
	if err != nil || end != agents.GroupReplaced {
		t.Fatalf("EndGroup(reused identifier) = %s, %v, want %s", end, err, agents.GroupReplaced)
	}
	if !groupAlive(id.PID) {
		t.Fatalf("the group %d was ended on a reused identifier", id.PID)
	}
	if _, err := agents.EndGroup(agents.Identity{PID: id.PID}, time.Second); !errors.Is(err, agents.ErrIdentityUnreadable) {
		t.Fatalf("an identity with no start time was acted on: %v, want ErrIdentityUnreadable", err)
	}
	if !groupAlive(id.PID) {
		t.Fatalf("the group %d was ended on an identity with no start time", id.PID)
	}
}

// TestEndGroupAnswersForALeaderThatIsGone pins the two answers a gone leader
// has on this platform: a group that emptied with it is gone and nothing is
// signalled, and a group that outlived it is reported outlived and left
// alone, because the recorded identity names the leader and not the
// survivor. The survivor is the stand-in's own child, started in the
// leader's group the way an agent's descendant is.
func TestEndGroupAnswersForALeaderThatIsGone(t *testing.T) {
	_, id, reaped := startHeldReaped(t)
	if err := syscall.Kill(id.PID, syscall.SIGKILL); err != nil {
		t.Fatalf("killing the leader: %v", err)
	}
	waitAbsent(t, id)
	// The empty-group answer is about a group with nothing left in it, so
	// wait for this process to reap the leader. An exited leader not yet
	// reaped is the case TestEndGroupCountsAnExitedUnreapedMemberAsGone
	// drives on purpose, and it reads gone only where exitedOnly can list
	// it, which is Linux alone.
	select {
	case <-reaped:
	case <-time.After(10 * time.Second):
		t.Fatalf("the killed leader %d was not reaped", id.PID)
	}
	end, err := agents.EndGroup(id, time.Second)
	if err != nil || end != agents.GroupGone {
		t.Fatalf("EndGroup(gone leader, empty group) = %s, %v, want %s", end, err, agents.GroupGone)
	}

	pidFile := filepath.Join(t.TempDir(), "pids")
	leader := exec.Command(helperBinary(t))
	leader.Env = []string{helperModeVar + "=spawn-and-exit", helperPidVar + "=" + pidFile}
	leader.Dir = os.TempDir()
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := leader.Start(); err != nil {
		t.Fatalf("starting the spawning stand-in: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-leader.Process.Pid, syscall.SIGKILL) })
	leaderID, found, err := agents.Identify(leader.Process.Pid)
	if err != nil || !found {
		// The leader may have finished before it was looked at; its
		// identity is then unreadable and the case below cannot be driven.
		t.Skipf("the spawning leader %d was gone before its identity was read: %v", leader.Process.Pid, err)
	}
	if err := leader.Wait(); err != nil {
		t.Fatalf("the spawning stand-in failed: %v", err)
	}
	pids, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the stand-in left no pid file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(pids)), "\n")
	child, err := strconv.Atoi(lines[len(lines)-1])
	if err != nil {
		t.Fatalf("the pid file carries %q, want the child's identifier last", pids)
	}
	if !groupAlive(leaderID.PID) {
		t.Fatalf("the group %d emptied with its leader, so the outlived case cannot be driven", leaderID.PID)
	}
	end, err = agents.EndGroup(leaderID, time.Second)
	if err != nil || end != agents.GroupOutlivedLeader {
		t.Fatalf("EndGroup(gone leader, survivor) = %s, %v, want %s", end, err, agents.GroupOutlivedLeader)
	}
	if err := syscall.Kill(child, 0); err != nil {
		t.Fatalf("the survivor %d was signalled on the strength of a leader nobody could verify: %v", child, err)
	}
}

// waitAbsent waits until no running process has the identity, which for a
// child of this process is its reaping by the wait startHeld runs beside the
// test, bounded so a leader that never goes fails rather than hangs.
func waitAbsent(t *testing.T, id agents.Identity) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if presence, err := id.Presence(); err == nil && presence == agents.PresenceAbsent {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d is still present", id.PID)
}

// TestEndGroupCountsAnExitedUnreapedMemberAsGone pins groupGone's widening
// on Linux, the one platform whose listing of a group's members is trusted,
// and its over-cautious answer everywhere else: a leader that has exited
// and that nothing has reaped is still in its group, signal zero still
// reaches it, and that is the state an orphan a recovering service ends is
// left in wherever nothing reaps orphans. The control is the signal-zero
// read itself, which says the group is still there, so the answer below came
// from the listing and not from a group that had already emptied.
func TestEndGroupCountsAnExitedUnreapedMemberAsGone(t *testing.T) {
	requiresIdentityRead(t)
	cmd := exec.Command(helperBinary(t))
	cmd.Env = []string{helperModeVar + "=hold"}
	cmd.Dir = os.TempDir()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the held stand-in: %v", err)
	}
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	})
	id, found, err := agents.Identify(cmd.Process.Pid)
	if err != nil || !found {
		t.Fatalf("identifying the held stand-in: %+v, %v, %v", id, found, err)
	}
	if err := syscall.Kill(id.PID, syscall.SIGKILL); err != nil {
		t.Fatalf("killing the leader: %v", err)
	}
	// Nothing waits on the leader yet, so it stays a zombie in its group:
	// the identity read counts it absent once the kernel has marked it.
	waitAbsent(t, id)
	if !groupAlive(id.PID) {
		t.Fatalf("signal zero no longer reaches group %d, so the unreaped case is not being driven", id.PID)
	}

	end, err := agents.EndGroup(id, time.Second)
	switch runtime.GOOS {
	case "linux":
		if err != nil || end != agents.GroupGone {
			t.Fatalf("EndGroup(an exited, unreaped leader) = %s, %v, want %s", end, err, agents.GroupGone)
		}
	default:
		// No member reading here is trusted to identify an exited member,
		// so the zombie keeps the group from reading gone, which is the
		// over-cautious direction groupGone documents. darwin is here because
		// its process-table reading by group did not answer gone for this
		// very case on darwin/arm64 CI; group_otherunix.go says so.
		if err != nil || end != agents.GroupOutlivedLeader {
			t.Fatalf("EndGroup(an exited, unreaped leader) on %s = %s, %v, want %s",
				runtime.GOOS, end, err, agents.GroupOutlivedLeader)
		}
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("the leader reported a clean exit after SIGKILL")
	}
	reaped = true
}
