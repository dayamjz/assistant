//go:build unix

package agents_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// A read of the installed CLI that never finishes fails, naming what it
// waited for, and leaves nothing it started running. Two stand-ins are
// read. One never exits: it records its own identifier, starts a child that
// inherits its standard output, and waits for ever, which is the shape that
// held make check to the package timeout and left claude --version
// processes running for hours once the binary was killed; it fails at the
// bound. The other exits at once and leaves that child holding the pipe,
// which the bound never reaches, since the command exited: exec.Cmd cuts
// the wait off installedWaitDelay later without cancelling anything, and
// the read fails then. In both, every recorded process is checked by
// identifier after the read returns.
func TestAHungInstalledCLIFailsItsReadAndLeavesNothingRunning(t *testing.T) {
	for _, c := range []struct {
		name, script string
		bound        time.Duration
		want         error
		names        string
		within       time.Duration
	}{{
		name:   "never exits",
		script: "#!/bin/sh\necho $$ >> \"$PIDS\"\nsleep 3600 &\necho $! >> \"$PIDS\"\nwait\n",
		bound:  2 * time.Second,
		want:   context.DeadlineExceeded,
		names:  (2 * time.Second).String(),
		within: 2*time.Second + installedWaitDelay,
	}, {
		name:   "exits leaving a child on its output",
		script: "#!/bin/sh\necho $$ >> \"$PIDS\"\nsleep 3600 &\necho $! >> \"$PIDS\"\nexit 0\n",
		bound:  time.Minute,
		want:   exec.ErrWaitDelay,
		names:  "holding its output open",
		within: installedWaitDelay + 5*time.Second,
	}} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			pids := filepath.Join(dir, "pids")
			bin := filepath.Join(dir, "claude")
			if err := procgroup.WriteProgram(bin, []byte(c.script), 0o755); err != nil {
				t.Fatalf("writing the stand-in: %v", err)
			}
			// Whatever this test fails on, nothing it started outlives it.
			t.Cleanup(func() {
				for _, pid := range recordedPIDs(pids) {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			})

			began := time.Now()
			_, err := askInstalled(t.Context(), c.bound, bin, []string{"PIDS=" + pids, "PATH=/usr/bin:/bin"}, "--version")
			took := time.Since(began)
			if err == nil {
				t.Fatalf("the stand-in answered its version read")
			}
			if !errors.Is(err, c.want) {
				t.Errorf("the read failed with %v, want %v", err, c.want)
			}
			for _, want := range []string{bin + " --version", c.names} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the failure %q does not name %q", err, want)
				}
			}
			if took > c.within {
				t.Errorf("the read returned after %v, want within %v", took, c.within)
			}

			started := recordedPIDs(pids)
			if len(started) != 2 {
				t.Fatalf("the stand-in recorded %v, want itself and its child", started)
			}
			for _, pid := range started {
				if !gone(pid, 10*time.Second) {
					t.Errorf("process %d the read started is still running after the read returned", pid)
				}
			}
		})
	}
}

// The check above is not vacuous: gone reports a process this test started
// and has not ended as still running.
func TestGoneSeesARunningProcess(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "3600")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting a process to look for: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	if gone(cmd.Process.Pid, 200*time.Millisecond) {
		t.Fatalf("gone reported running process %d as gone", cmd.Process.Pid)
	}
}

// recordedPIDs reads the identifiers the stand-in appended to path, one per
// line, and skips what does not parse.
func recordedPIDs(path string) []int {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pids []int
	for _, field := range strings.Fields(string(data)) {
		if pid, err := strconv.Atoi(field); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// gone reports whether no process with identifier pid exists within wait.
// A child the read's group kill ended is reparented and reaped by the
// system, which can lag the kill, so it is looked for until wait elapses.
func gone(pid int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
