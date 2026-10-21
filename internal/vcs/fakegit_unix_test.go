//go:build unix

package vcs_test

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// leaveGroup has the stand-in's grandchild start a process group of its own,
// the one way a descendant gets out of reach of the group its invocation was
// started in.
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

// standInExitBound is how long a released stand-in has to exit before its
// test fails. A released stand-in returns within one poll of the release, so
// this is generous.
const standInExitBound = 5 * time.Second

// awaitStandIns fails the test for each of pids still running
// standInExitBound after its release, which is useFakeGit's cleanup's check
// that a test ended every stand-in it started.
func awaitStandIns(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		if !gone(pid, standInExitBound) {
			t.Errorf("stand-in process %d is still running %s after its release", pid, standInExitBound)
		}
	}
}

// trackStandIns returns a reader of the identifiers of the stand-in gits
// that hang and the grandchildren they start, which useFakeGit has them
// record; it is called after useFakeGit.
func trackStandIns(t *testing.T) func() []int {
	t.Helper()
	path := os.Getenv(fakeGitPIDs)
	if path == "" {
		t.Fatal("trackStandIns was called before useFakeGit")
	}
	return func() []int { return readPIDs(path) }
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
