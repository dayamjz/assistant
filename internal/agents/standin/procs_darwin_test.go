//go:build darwin

package standin

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
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

// churns is a processTable that starts a real process of this user's
// before each reading of the table (ids) and ends and reaps it when the
// reading first asks after it (identify), so that every reading meets a
// process that exited after the table named it, as one on a busy machine
// does. The table names it first, since macOS's process table names the
// newest process first. Every other read passes through to the table it
// wraps.
type churns struct {
	*failsMidExec
	mu    sync.Mutex
	live  map[int]*exec.Cmd
	ended int
}

func (c *churns) ids() ([]listedProcess, error) {
	p := exec.Command("/bin/sleep", "60")
	if err := p.Start(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.live[p.Process.Pid] = p
	c.mu.Unlock()
	return c.failsMidExec.ids()
}

func (c *churns) identify(pid int) (agents.Identity, bool, error) {
	c.mu.Lock()
	p, ok := c.live[pid]
	delete(c.live, pid)
	c.mu.Unlock()
	if ok {
		_ = p.Process.Kill()
		_ = p.Wait()
		c.ended++
	}
	return c.failsMidExec.identify(pid)
}

// newChurns wraps table in a churns that ends what it started with the
// test.
func newChurns(t *testing.T, table *failsMidExec) *churns {
	c := &churns{failsMidExec: table, live: map[int]*exec.Cmd{}}
	t.Cleanup(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, p := range c.live {
			_ = p.Process.Kill()
			_ = p.Wait()
		}
	})
	return c
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
// the shell over. Before #550, it came back empty with no error. Every
// reading here also meets another process that exits during it (churns), as
// a busy runner's do, and the shell is still the one named, as the process
// that kept every reading from settling: before, the error named whatever
// unsettled the last reading first, which on macOS CI was an unrelated
// process exiting (#561). The controls are that every reading met a process
// that exited, that the last met one besides the shell, and that the shell
// was running throughout.
func TestAListingFailsWhenThisUsersRunningProcessCannotBeRead(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	shell, token := startHeldShell(t)
	table := newChurns(t, &failsMidExec{pid: shell.Process.Pid, always: true})

	listed, err := processesWhere(table, carriesArg(token))
	if err == nil {
		t.Fatalf("the listing is %v and no error, though no read of the running shell %d succeeded", listed, shell.Process.Pid)
	}
	if table.ended != settleReadings {
		t.Fatalf("%d of %d readings met a process that exited, so this shows nothing", table.ended, settleReadings)
	}
	t.Logf("the listing failed with %v", err)
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) {
		t.Fatalf("the listing failed with %v, want an unsettledError", err)
	}
	if unsettled.Others == 0 {
		t.Fatalf("nothing but the shell kept the last reading from settling (%v), so this shows nothing", err)
	}
	if unsettled.PID != shell.Process.Pid || unsettled.Readings != settleReadings || !errors.Is(err, syscall.EIO) {
		t.Errorf("the listing failed with %v, want one naming process %d as keeping each of the %d readings from settling with the read's EIO", err, shell.Process.Pid, settleReadings)
	}
	if err := shell.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the shell was not running once the listing returned (%v), so this shows nothing", err)
	}
}
