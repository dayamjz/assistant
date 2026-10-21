//go:build windows

package vcs_test

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// leaveGroup has no counterpart on Windows, where this package starts no
// process group, so the grandchild stays as it was started.
func leaveGroup(*exec.Cmd) {}

// awaitStandIns fails the test for each of pids still running five seconds
// after its release. On Windows os.FindProcess opens a handle to the process
// and Wait on it returns once it exits, whoever started it; an identifier
// that names no process any more cannot be opened and is gone.
func awaitStandIns(t *testing.T, pids []int) {
	t.Helper()
	for _, pid := range pids {
		p, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		done := make(chan struct{})
		go func() {
			_, _ = p.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("stand-in process %d is still running 5s after its release", pid)
		}
		_ = p.Release()
	}
}
