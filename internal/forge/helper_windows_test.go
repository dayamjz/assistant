//go:build windows

package forge_test

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// standInSelfKill has no counterpart on Windows, where every process that ran
// reports a status, so there is no answer to model in which the provider
// reports none. No test uses the mode on this platform, and this exit says so
// rather than pretending the signalled path was exercised.
func standInSelfKill() {
	os.Exit(fakeGHNoScript)
}

// leaveGroup has no counterpart on Windows, where this package starts no
// process group, so the descendant stays as it was started.
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
