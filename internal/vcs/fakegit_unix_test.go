//go:build unix

package vcs_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// trackStandIns has every stand-in git that hangs and every grandchild
// started from here on record its identifier, and returns a reader of what
// was recorded. It is called after useFakeGit. Cleanup writes the release
// useFakeGit named and then waits for each recorded process to be gone, so a
// test ends what it started without signalling an identifier that may have
// been reused.
func trackStandIns(t *testing.T) func() []int {
	t.Helper()
	pids := filepath.Join(t.TempDir(), "pids")
	t.Setenv(fakeGitPIDs, pids)
	read := func() []int {
		data, _ := os.ReadFile(pids)
		var out []int
		for _, line := range strings.Fields(string(data)) {
			if n, err := strconv.Atoi(line); err == nil {
				out = append(out, n)
			}
		}
		return out
	}
	release := os.Getenv(fakeGitRelease)
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0o600)
		for _, pid := range read() {
			if !gone(pid, 5*time.Second) {
				t.Errorf("stand-in process %d is still running after its release", pid)
			}
		}
	})
	return read
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
