//go:build linux || darwin

package standin

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
)

// forkOnSignalArg makes this binary a process that, on SIGUSR1, starts this
// binary as a pane's process, which runs until something ends it, writes
// that process's identifier to the file forkRecordVar names, and exits
// (forkOnSignalMain). It prints a line once it is ready for the signal.
const (
	forkOnSignalArg = "-standin-test-fork-on-signal"
	forkRecordVar   = "STANDIN_TEST_FORK_RECORD"
)

// forkOnSignalMain is the process forkOnSignalArg makes this binary, when a
// test started it as one, and returns otherwise.
func forkOnSignalMain() {
	if !slices.Contains(os.Args[1:], forkOnSignalArg) {
		return
	}
	ready := make(chan os.Signal, 1)
	signal.Notify(ready, syscall.SIGUSR1)
	fmt.Println("ready")
	<-ready
	self, err := os.Executable()
	if err != nil {
		os.Exit(exitBroken)
	}
	child := exec.Command(self, "-test.run=^$", tmuxPaneArg)
	if err := child.Start(); err != nil {
		os.Exit(exitBroken)
	}
	if err := writeWhole(os.Getenv(forkRecordVar), []byte(strconv.Itoa(child.Process.Pid))); err != nil {
		os.Exit(exitBroken)
	}
	os.Exit(0)
}

// exitMidRead is a processTable that passes every read through to the
// kernel, and once the kernel has answered the read of pid's environment,
// runs exit before returning that answer: the process is read whole while
// it runs, and has exited by the listing's next read of it.
type exitMidRead struct {
	kernel
	pid   int
	exit  func()
	once  sync.Once
	fired bool
}

func (t *exitMidRead) environment(pid int) ([]string, error) {
	env, err := t.kernel.environment(pid)
	if pid == t.pid && err == nil {
		t.once.Do(func() { t.exit(); t.fired = true })
	}
	return env, err
}

// A listing the check counts as empty is one taken once the process table
// has settled (P15): a process carrying the owner identity whose reads
// succeed, and which starts another and exits before the listing is done
// with it, does not leave the listing empty while the process it started
// runs. The parent here is a real process carrying a value of this test's
// own; the table orders its exit right after the kernel answered the read
// of its environment, and every read is the kernel's. Before the listing
// settled, the parent was passed over as gone, the process it started was
// not in the process table the listing had read, and the empty listing let
// a passing check retire the sweep with that process running. The control
// is that the parent's exit did fire inside the listing.
func TestAListingFindsWhatAProcessStartsAsItExitsMidListing(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	value := fmt.Sprintf("mid-listing-%d-%d", os.Getpid(), time.Now().UnixNano())
	record := filepath.Join(t.TempDir(), "child")
	parent := exec.Command(self, "-test.run=^$", forkOnSignalArg)
	parent.Env = append(os.Environ(), OwnerVar+"="+value, forkRecordVar+"="+record)
	out, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	var child agents.Identity
	t.Cleanup(func() {
		_ = parent.Process.Kill()
		_ = parent.Wait()
		if child.PID != 0 {
			if presence, err := child.Presence(); err == nil && presence == agents.PresencePresent {
				_ = syscall.Kill(child.PID, syscall.SIGKILL)
			}
		}
	})
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("the parent said %q (%v) before it was ready", line, err)
	}

	table := &exitMidRead{pid: parent.Process.Pid, exit: func() {
		_ = parent.Process.Signal(syscall.SIGUSR1)
		_ = parent.Wait()
		raw, err := os.ReadFile(record)
		if err != nil {
			t.Errorf("the parent recorded no child: %v", err)
			return
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		if id, found, err := agents.Identify(pid); err == nil && found {
			child = id
		}
	}}
	listed, err := leftRunning(table, filepath.Join(t.TempDir(), "no-such-binary"), value)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if !table.fired {
		t.Fatal("the parent's environment was never read, so its exit did not fall inside the listing and this shows nothing")
	}
	if child.PID == 0 {
		t.Fatal("the process the parent started was gone before the listing returned, so this shows nothing")
	}
	if !slices.ContainsFunc(listed, func(p Process) bool { return p.Identity == child }) {
		t.Errorf("the listing is %v, without the process %v the parent started as it exited mid-listing; an empty one passes the check while it runs", listed, child)
	}
}

// A process that has exited and that nobody has reaped yet starts nothing,
// so it does not keep a listing from settling: the zombie here is a real
// child of this test, left unreaped, and the listing ends without an error.
// The control is that the process table still names the zombie and reads it
// as not running, which is the shape that would keep every reading
// unsettled if a process read as not running always counted against one.
func TestAListingSettlesBesideAZombie(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	proc, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exit 0"}, &os.ProcAttr{})
	if err != nil {
		t.Fatal(err)
	}
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_, _ = proc.Wait()
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		pids, err := processIDs()
		if err != nil {
			t.Fatal(err)
		}
		_, running, err := agents.Identify(proc.Pid)
		if err == nil && !running && slices.Contains(pids, proc.Pid) {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("process %d never read as an unreaped exited process (listed %v, running %v, %v)", proc.Pid, slices.Contains(pids, proc.Pid), running, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := processesWhere(kernel{}, func(processTable, int) bool { return false }); err != nil {
		t.Errorf("a listing beside the zombie %d: %v", proc.Pid, err)
	}
	_, _ = proc.Wait()
	reaped = true
}
