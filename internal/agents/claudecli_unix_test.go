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
)

// A read of the installed CLI that never answers fails within its bound,
// naming what it waited for, and leaves nothing it started running. The
// stand-in for a hung CLI is a script that records its own identifier,
// starts a child that inherits its standard output, and waits for ever, so
// both the process the read started and a child holding the pipe open are
// checked by identifier after the read returns. Before the read was bounded
// this is the shape that held make check to the package timeout and left
// claude --version processes running for hours once the binary was killed.
func TestAHungInstalledCLIFailsItsReadAndLeavesNothingRunning(t *testing.T) {
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\necho $$ >> \"$PIDS\"\nsleep 3600 &\necho $! >> \"$PIDS\"\nwait\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the hung stand-in: %v", err)
	}
	// Whatever this test fails on, nothing it started outlives it.
	t.Cleanup(func() {
		for _, pid := range recordedPIDs(pids) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	const bound = 2 * time.Second
	began := time.Now()
	_, err := askInstalled(t.Context(), bound, bin, []string{"PIDS=" + pids, "PATH=/usr/bin:/bin"}, "--version")
	took := time.Since(began)
	if err == nil {
		t.Fatalf("a stand-in that never exits answered its version read")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the read failed with %v, want the bound's deadline", err)
	}
	for _, want := range []string{bin + " --version", bound.String()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure %q does not name %q", err, want)
		}
	}
	if limit := bound + installedWaitDelay; took > limit {
		t.Errorf("the read returned after %v, past its bound and the pipe's grace, %v", took, limit)
	}

	started := recordedPIDs(pids)
	if len(started) != 2 {
		t.Fatalf("the stand-in recorded %v, want itself and its child", started)
	}
	for _, pid := range started {
		if !gone(pid, 10*time.Second) {
			t.Errorf("process %d the hung read started is still running after the read returned", pid)
		}
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
