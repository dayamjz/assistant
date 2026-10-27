//go:build linux || darwin

package standin

import (
	"bufio"
	"errors"
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
	if _, err := processesWhere(kernel{}, func(processTable, int) (bool, error) { return false, nil }); err != nil {
		t.Errorf("a listing beside the zombie %d: %v", proc.Pid, err)
	}
	_, _ = proc.Wait()
	reaped = true
}

// refusedExitsEachReading is a processTable that passes every read through
// to the kernel and orders real processes' lives around its reads of the
// table: each reading starts a process before the table is read, so the
// reading names it running and reads it whole, and ends and reaps the one
// the reading before started once the table is read, so the reading names
// it and finds it gone. The first reading ends unread instead, a process
// started before the listing that no reading named running.
type refusedExitsEachReading struct {
	kernel
	t        *testing.T
	readings int
	// current is the process the last reading started, unread is the one
	// the first reading ends, and ended is each process a reading named and
	// then ended.
	current, unread *exec.Cmd
	ended           []int
}

func (r *refusedExitsEachReading) start() *exec.Cmd {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func (r *refusedExitsEachReading) ids() ([]int, error) {
	r.readings++
	previous := r.unread
	if r.readings > 1 {
		previous = r.current
	}
	r.current = r.start()
	pids, err := r.kernel.ids()
	if err != nil {
		return nil, err
	}
	if slices.Contains(pids, previous.Process.Pid) {
		r.ended = append(r.ended, previous.Process.Pid)
	}
	_ = previous.Process.Kill()
	_ = previous.Wait()
	return pids, nil
}

// A process the listing read whole and refused that exits does not keep a
// reading from settling (P15, #569): what it started once the reading began
// is read only by a later reading whether it exits or goes on running, so
// its exit says nothing new, and on CI, where other packages' tests start and
// end processes beside the listing, counting those exits left no reading of
// 64 settled. Here a real process the listing has read and refused exits in
// every reading after the first, and the listing settles. Before #569 every
// reading was unsettled and the listing failed. The control is that the
// exit of a process no reading had read whole still unsettles the first
// reading, so the listing read the table at least twice, and that the
// table named every process it ended before ending it.
func TestAListingSettlesWhileProcessesItRefusedExit(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	table := &refusedExitsEachReading{t: t}
	table.unread = table.start()
	refuseAll := func(table processTable, pid int) (bool, error) {
		_, _, err := table.commandLine(pid)
		return false, err
	}
	if _, err := processesWhere(table, refuseAll); err != nil {
		t.Fatalf("a listing while processes it refused exit, one in each reading: %v", err)
	}
	if table.readings < 2 {
		t.Fatalf("the listing read the table %d time(s): the exit of a process no reading had read whole did not unsettle the first, so this shows nothing", table.readings)
	}
	if len(table.ended) != table.readings {
		t.Fatalf("the table named %d of the %d processes it ended, so not every reading had an exit in it", len(table.ended), table.readings)
	}
}

// ProcessesMatching lists a process by its arguments whatever it runs: a real
// shell, not this binary, started with an argument of this test's own that
// holds spaces, is listed once with exactly the arguments it was started
// with, read whole rather than split. The control is that once the shell has
// exited the same listing finds nothing.
func TestProcessesMatchingListsAProcessByItsArguments(t *testing.T) {
	t.Parallel()
	token := fmt.Sprintf("matching %d %d", os.Getpid(), time.Now().UnixNano())
	shell := exec.Command("/bin/sh", "-c", "read line; exit 0", "sh", token)
	in, err := shell.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := shell.Start(); err != nil {
		t.Fatal(err)
	}
	exited := false
	t.Cleanup(func() {
		if !exited {
			_ = shell.Process.Kill()
			_ = shell.Wait()
		}
	})
	carriesToken := func(args []string) bool { return slices.Contains(args, token) }

	listed, err := ProcessesMatching(carriesToken)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(listed) != 1 || listed[0].PID != shell.Process.Pid || !slices.Equal(listed[0].Args, shell.Args) {
		t.Fatalf("the listing is %v, want only process %d with arguments %q", listed, shell.Process.Pid, shell.Args)
	}

	_ = in.Close()
	if err := shell.Wait(); err != nil {
		t.Fatalf("the shell did not exit cleanly once its input closed: %v", err)
	}
	exited = true
	if listed, err := ProcessesMatching(carriesToken); err != nil || len(listed) != 0 {
		t.Errorf("after the shell exited the listing is %v, %v, want none", listed, err)
	}
}

// refusalsSeen is a processTable that passes every read through to the
// kernel and records each process the kernel reported as another user's
// (foreign).
type refusalsSeen struct {
	kernel
	mu      sync.Mutex
	refused []int
}

func (t *refusalsSeen) foreign(pid int) (bool, error) {
	foreign, err := t.kernel.foreign(pid)
	if err == nil && foreign {
		t.mu.Lock()
		t.refused = append(t.refused, pid)
		t.mu.Unlock()
	}
	return foreign, err
}

// A listing of every process passes over another user's processes whose
// command line it cannot read, and settles beside them (P15): the real
// kernel answers every read, the listing accepts every process it reads,
// and it ends without an error. A failed read of this user's running
// process unsettles a reading instead, so this is the control that the
// refusal is read from the process's user and that another user's
// unreadable process still settles: the listing did meet at least one,
// which macOS's root processes are and Linux CI's namespace holds (tini and
// a sleep of the nobody user), and none of them was listed.
func TestAListingPassesOverAnotherUsersUnreadableProcess(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	table := &refusalsSeen{}
	listed, err := processesWhere(table, func(processTable, int) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("a listing of every process: %v", err)
	}
	if len(table.refused) == 0 {
		t.Fatal("no read failed for another user's process, so this shows nothing")
	}
	for _, p := range listed {
		if slices.Contains(table.refused, p.PID) {
			t.Errorf("%v is another user's and could not be read, and was listed", p)
		}
	}
}

// unreadableAsExiting is a processTable that names one real running
// process, pid, and passes every read of it through to the kernel but two:
// its command line
// fails to read as Linux's /proc does for a process that has begun to exit
// and released its address space (readlink of /proc/<pid>/exe: ENOENT,
// the read CI's ubuntu runner reported, run 40211320712), and it reads as
// exiting when exiting says so.
type unreadableAsExiting struct {
	kernel
	pid      int
	exiting  bool
	readings int
}

// ids names pid alone, so how many readings the listing takes is decided
// by that process and nothing else running beside the test.
func (u *unreadableAsExiting) ids() ([]int, error) {
	u.readings++
	return []int{u.pid}, nil
}

func (u *unreadableAsExiting) commandLine(pid int) (string, []string, error) {
	if pid == u.pid {
		return "", nil, &os.PathError{Op: "readlink", Path: "/proc/" + strconv.Itoa(pid) + "/exe", Err: syscall.ENOENT}
	}
	return u.kernel.commandLine(pid)
}

func (u *unreadableAsExiting) isExiting(pid int) (bool, error) {
	if pid == u.pid {
		return u.exiting, nil
	}
	return u.kernel.isExiting(pid)
}

// A process of this user's that has begun to exit starts nothing, as one
// that has exited does, so a read of it that fails does not keep a
// listing from settling (P15): a real running process whose command line
// fails to read as CI's ubuntu runner reported, readlink of its exe
// ENOENT, and that reads as exiting, is passed over and the listing
// settles without it, from the second reading: the first that meets it
// exiting is unsettled, as the first that meets an exited process is,
// since it may have started a process that reading does not name. The
// control is the same process not reading as
// exiting, which still fails the listing, naming it and the read's
// ENOENT, as TestTheCoordinatorClientDrivesEveryMethodAndRefusesAWorker
// did on that runner: a running process of this user's that cannot be read
// and is not exiting is never passed over (#549).
func TestAListingSettlesBesideAnUnreadableProcessThatIsExiting(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	refuseAll := func(table processTable, pid int) (bool, error) {
		_, _, err := table.commandLine(pid)
		return false, err
	}

	exiting := &unreadableAsExiting{pid: sleeper.Process.Pid, exiting: true}
	listed, err := processesWhere(exiting, refuseAll)
	if err != nil {
		t.Fatalf("a listing beside process %d, unreadable and exiting: %v", sleeper.Process.Pid, err)
	}
	if exiting.readings < 2 {
		t.Fatalf("the listing read the table %d time(s); want the reading that first met the exiting process unsettled, as one that first meets an exited process is", exiting.readings)
	}
	if slices.ContainsFunc(listed, func(p Process) bool { return p.PID == sleeper.Process.Pid }) {
		t.Fatalf("the listing %v names process %d, which it could not read", listed, sleeper.Process.Pid)
	}

	_, err = processesWhere(&unreadableAsExiting{pid: sleeper.Process.Pid}, refuseAll)
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) || unsettled.PID != sleeper.Process.Pid || !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("a listing beside process %d, unreadable and not exiting = %v; want it unsettled naming the process and its ENOENT", sleeper.Process.Pid, err)
	}
	if err := sleeper.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("process %d was not running once the listings returned (%v), so this shows nothing", sleeper.Process.Pid, err)
	}
}

// argumentsEmptyOnce is a processTable that names one real running process,
// pid, and passes every read of it through to the kernel but one: in the
// first reading its command line reads as its executable with an empty
// argument list and no error, which is what Linux's /proc read for a shell
// in the window between its exec and the loader placing its arguments
// (CI's ubuntu runner, run 40492934694, job 122151106251: the listing of
// TestProcessesMatchingListsAProcessByItsArguments settled without the
// shell it had just started). Every later reading is the kernel's.
type argumentsEmptyOnce struct {
	kernel
	pid      int
	readings int
}

// ids names pid alone, so how many readings the listing takes is decided
// by that process and nothing else running beside the test.
func (a *argumentsEmptyOnce) ids() ([]int, error) {
	a.readings++
	return []int{a.pid}, nil
}

func (a *argumentsEmptyOnce) commandLine(pid int) (string, []string, error) {
	if pid == a.pid && a.readings == 1 {
		executable, _, err := a.kernel.commandLine(pid)
		return executable, []string{""}, err
	}
	return a.kernel.commandLine(pid)
}

// An argument list that reads empty is not a read made whole (P15): a real
// running shell whose arguments read as an empty list in the first reading,
// as /proc gives them between a process's exec and the loader placing them,
// is read again and listed from the next reading with the arguments it was
// started with. Before, the match refused the empty list, the shell was
// read whole as refused, which no later reading revisits, and the listing
// settled without it (#549's listing, found empty again on CI's ubuntu
// runner, run 40492934694).
func TestAProcessWhoseArgumentsReadEmptyIsReadAgain(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	token := fmt.Sprintf("empty once %d %d", os.Getpid(), time.Now().UnixNano())
	shell := exec.Command("/bin/sh", "-c", "read line; exit 0", "sh", token)
	in, err := shell.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := shell.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = in.Close(); _ = shell.Process.Kill(); _ = shell.Wait() })
	table := &argumentsEmptyOnce{pid: shell.Process.Pid}
	listed, err := processesWhere(table, matching(func(args []string) bool { return slices.Contains(args, token) }))
	if err != nil {
		t.Fatalf("the listing beside the shell whose arguments read empty once: %v", err)
	}
	if len(listed) != 1 || listed[0].PID != shell.Process.Pid || !slices.Equal(listed[0].Args, shell.Args) {
		t.Fatalf("the listing is %v after %d reading(s), want only process %d with arguments %q", listed, table.readings, shell.Process.Pid, shell.Args)
	}
	if table.readings < 2 {
		t.Fatalf("the listing read the table %d time(s); want the reading that met the empty argument list unsettled", table.readings)
	}
}
