//go:build darwin

package standin

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

// failsMidExec is a processTable that passes every read through to the
// kernel except the reads of pid's kern.procargs2 record (its command line
// and its environment), which it answers with EIO: every one of them when
// always is set, and otherwise only the first. EIO is what that read
// answered for this user's shell read right after exec.Cmd.Start returned,
// on the machine this was written on, about three times in three thousand,
// so the table stands in for that read at a moment the test chooses.
type failsMidExec struct {
	kernel
	pid    int
	always bool
	mu     sync.Mutex
	failed int
}

// fail reports whether this read of pid's record is one the table fails.
func (t *failsMidExec) fail(pid int) bool {
	if pid != t.pid {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.always && t.failed > 0 {
		return false
	}
	t.failed++
	return true
}

func (t *failsMidExec) commandLine(pid int) (string, []string, error) {
	if t.fail(pid) {
		return "", nil, syscall.EIO
	}
	return t.kernel.commandLine(pid)
}

func (t *failsMidExec) environment(pid int) ([]string, error) {
	if t.fail(pid) {
		return nil, syscall.EIO
	}
	return t.kernel.environment(pid)
}

// startHeldShell starts a real shell of this user that waits on its input,
// carrying an argument of the test's own, and ends it with the test.
func startHeldShell(t *testing.T) (*exec.Cmd, string) {
	t.Helper()
	token := fmt.Sprintf("mid-exec %d %d", os.Getpid(), time.Now().UnixNano())
	shell := exec.Command("/bin/sh", "-c", "read line; exit 0", "sh", token)
	in, err := shell.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := shell.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = in.Close()
		_ = shell.Process.Kill()
		_ = shell.Wait()
	})
	return shell, token
}

// carriesArg is ProcessesMatching's match (matching) for a process
// carrying token.
func carriesArg(token string) func(processTable, int) (bool, error) {
	return matching(func(args []string) bool { return slices.Contains(args, token) })
}

// A read of this user's running process that fails once is not that
// process read whole, so the listing reads the table again rather than
// settling without it (P15): the shell here is real and running, and only
// the first read of its record fails, with the EIO the kernel answered
// mid-exec (failsMidExec). Before, the failed read was passed over as one
// that cannot be read while the process runs, the reading settled, and the
// listing came back empty with no error, which is what main's macOS CI
// read for TestProcessesMatchingListsAProcessByItsArguments (#549). The
// control is that the table did fail a read of the shell.
func TestAListingReadsAgainAProcessWhoseReadFailedOnce(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	shell, token := startHeldShell(t)
	table := &failsMidExec{pid: shell.Process.Pid}

	listed, err := processesWhere(table, carriesArg(token))
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if table.failed == 0 {
		t.Fatal("no read of the shell failed, so this shows nothing")
	}
	if len(listed) != 1 || listed[0].PID != shell.Process.Pid || !slices.Equal(listed[0].Args, shell.Args) {
		t.Fatalf("the listing is %v, want only process %d with arguments %q", listed, shell.Process.Pid, shell.Args)
	}
}

// A list that cannot be read is an error and never an empty list (P15): when
// every read of this user's running shell fails, no reading settles, and the
// listing fails naming the shell and the read's error rather than passing
// the shell over. Before, it came back empty with no error. The control is
// that the shell was running throughout, so nothing else unsettled it.
func TestAListingFailsWhenThisUsersRunningProcessCannotBeRead(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	shell, token := startHeldShell(t)
	table := &failsMidExec{pid: shell.Process.Pid, always: true}

	listed, err := processesWhere(table, carriesArg(token))
	if err == nil {
		t.Fatalf("the listing is %v and no error, though no read of the running shell %d succeeded", listed, shell.Process.Pid)
	}
	if !errors.Is(err, syscall.EIO) || !strings.Contains(err.Error(), fmt.Sprintf("process %d ", shell.Process.Pid)) {
		t.Errorf("the listing failed with %v, want one naming process %d and the read's EIO", err, shell.Process.Pid)
	}
	if err := shell.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the shell was not running once the listing returned (%v), so this shows nothing", err)
	}
}
