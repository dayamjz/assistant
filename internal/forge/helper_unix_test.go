//go:build unix

package forge_test

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// standInSelfKill ends the stand-in provider with a signal, so it reports no
// exit status of its own. That is what a provider the system kills looks like
// to this package. Exiting instead would make the mode prove nothing, so what
// follows the signal is a loud failure rather than a fallback.
func standInSelfKill() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	time.Sleep(2 * time.Second)
	os.Exit(fakeGHNoScript)
}

// leaveGroup has the stand-in's descendant start a process group of its own,
// the one way it gets out of reach of the group its invocation was started
// in.
func leaveGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// alive reports whether pid names a process that has not been reaped. Signal
// zero sends nothing, so asking cannot end a process that reused the
// identifier; a reused identifier reads alive, which fails a test loudly
// rather than passing it.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// trackStandIns returns a reader of the identifiers the harness's stand-ins
// recorded. Ending them is releaseAtCleanup's, which every harness has.
func (h *harness) trackStandIns() func() []int {
	return func() []int { return readPIDs(h.dir) }
}

// awaitStandIns fails the test for each of pids still running five seconds
// after its release, which is releaseAtCleanup's check that a test ended
// every stand-in it started.
func awaitStandIns(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		if !gone(pid, 5*time.Second) {
			t.Errorf("stand-in process %d is still running 5s after its release", pid)
		}
	}
}

// gone waits up to limit for pid to stop being alive.
func gone(pid int, limit time.Duration) bool {
	end := time.Now().Add(limit)
	for alive(pid) {
		if time.Now().After(end) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}
