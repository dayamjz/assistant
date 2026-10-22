//go:build linux || darwin

package agents

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

	"github.com/dayamjz/assistant/internal/principles"
)

// PRD principle P15 for the program that ends a process tree, taskkill on
// Windows: runTreeKill gives it a process group it ends when the call
// returns. A stand-in that starts a child in the background and exits leaves
// no child once runTreeKill returns. The child sends its output elsewhere, so
// only ending the group ends it; the stand-in records the child before it
// exits, so the record is there whenever the call returns.
func TestATreeKillProgramsChildEndsWithIt(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	path := filepath.Join(t.TempDir(), "child")
	cmd := exec.Command("/bin/sh", "-c", "sleep 300 >/dev/null 2>&1 & echo $! > "+path)
	t.Cleanup(func() {
		if t.Failed() {
			endChild(path)
		}
	})
	if err := runTreeKill(cmd, treeKillDeadline); err != nil {
		t.Fatalf("runTreeKill = %v; want the stand-in's status 0", err)
	}
	child := childPID(t, path)
	gone := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(child, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if !time.Now().Before(gone) {
			t.Fatalf("the child %d the program started is still running after runTreeKill returned (%v)", child, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// PRD principle P15 for the same program's deadline: a stand-in that never
// exits is ended at the deadline runTreeKill is given, and the call answers
// an error, which its callers read as the program's failure and fall back
// on, so a hung tree kill cannot hold the termination path that runs it.
// The stand-in is reaped by the time the call returns.
func TestATreeKillProgramThatNeverExitsEndsAtItsDeadline(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	cmd := exec.Command("/bin/sh", "-c", "exec sleep 300")
	const deadline = 500 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- runTreeKill(cmd, deadline) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("runTreeKill of a program ended at its deadline answered no error")
		}
	case <-time.After(deadline + 3*treeKillGrace + 30*time.Second):
		t.Fatalf("runTreeKill had not returned well past its deadline of %s", deadline)
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("the stand-in %d still answers signal zero after runTreeKill returned (%v)", cmd.Process.Pid, err)
	}
}

// childPID reads the identifier the stand-in wrote to path.
func childPID(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the child's identifier: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		t.Fatalf("the stand-in wrote %q, which is no process identifier", b)
	}
	return pid
}

// endChild kills the child whose identifier the stand-in wrote to path, for
// a test that failed before it could show the child gone.
func endChild(path string) {
	if b, err := os.ReadFile(path); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
