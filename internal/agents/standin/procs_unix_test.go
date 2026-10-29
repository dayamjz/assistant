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

// A matching process whose parent is a refused shell, and that starts
// another matching process as it exits mid-listing, is found on a later
// reading (P15, #735 r1 finding 1): match is not ancestry, so the shell's
// refusal must not settle the matching process's unread exit. The parent
// here is a real /bin/sh that only waits; the mid process is this binary
// started as the shell's child with the owner identity, and exitMidRead
// ends it after the environment match. With the parent-outside shortcut
// the first reading settled empty while the child ran. The control is that
// the mid exit fired inside the listing and the child was still running.
func TestAListingFindsWhatAMatchingProcessUnderARefusedShellStartsAsItExits(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	value := fmt.Sprintf("refused-shell-%d-%d", os.Getpid(), time.Now().UnixNano())
	record := filepath.Join(t.TempDir(), "child")
	// Shell stays as parent: it starts mid with the owner identity in mid's
	// environment only (env), so carrying refuses the shell and accepts mid.
	shell := exec.Command("/bin/sh", "-c",
		`env "`+OwnerVar+`=`+value+`" "`+forkRecordVar+`=`+record+`" "$1" -test.run=^$ `+forkOnSignalArg+` & wait $!`,
		"sh", self)
	out, err := shell.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := shell.Start(); err != nil {
		t.Fatal(err)
	}
	var child agents.Identity
	var midPID int
	t.Cleanup(func() {
		_ = shell.Process.Kill()
		_ = shell.Wait()
		if midPID != 0 {
			_ = syscall.Kill(midPID, syscall.SIGKILL)
		}
		if child.PID != 0 {
			if presence, err := child.Presence(); err == nil && presence == agents.PresencePresent {
				_ = syscall.Kill(child.PID, syscall.SIGKILL)
			}
		}
	})
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("the mid process said %q (%v) before it was ready", line, err)
	}
	// Mid is the shell's child: the one carrying the owner identity.
	listed, err := carrying(value)
	if err != nil || len(listed) != 1 {
		t.Fatalf("finding the mid process under the shell: %v, %v", listed, err)
	}
	midPID = listed[0].PID
	lineage, found, err := agents.ReadLineage(midPID)
	if err != nil || !found || lineage.Parent != shell.Process.Pid {
		t.Fatalf("mid %d parent is %v (found %v, %v), want shell %d so the refused parent is real",
			midPID, lineage.Parent, found, err, shell.Process.Pid)
	}

	table := &exitMidRead{pid: midPID, exit: func() {
		_ = syscall.Kill(midPID, syscall.SIGUSR1)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			raw, err := os.ReadFile(record)
			if err != nil {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
			if id, found, err := agents.Identify(pid); err == nil && found {
				child = id
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Error("the mid process recorded no child")
	}}
	foundList, err := processesWhere(table, carries(value))
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if !table.fired {
		t.Fatal("the mid process's environment was never read, so its exit did not fall inside the listing")
	}
	if child.PID == 0 {
		t.Fatal("the process the mid started was gone before the listing returned")
	}
	if !slices.ContainsFunc(foundList, func(p Process) bool { return p.Identity == child }) {
		t.Errorf("the listing is %v, without the process %v the matching mid started as it exited under a refused shell; a parent-outside settle returns empty here", foundList, child)
	}
	if presence, err := child.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the child %v was not running once the listing returned (%v, %v)", child, presence, err)
	}
}

// eioChildOfShell is a processTable that passes every read through to the
// kernel except commandLine and environment of child, which fail with EIO
// (the shape #549 and #735 r1 finding 2 use), while parent is a refused
// shell that owns child.
type eioChildOfShell struct {
	kernel
	parent, child int
}

func (t *eioChildOfShell) commandLine(pid int) (string, []string, error) {
	if pid == t.child {
		return "", nil, syscall.EIO
	}
	return t.kernel.commandLine(pid)
}

func (t *eioChildOfShell) environment(pid int) ([]string, error) {
	if pid == t.child {
		return nil, syscall.EIO
	}
	return t.kernel.environment(pid)
}

// A failed read of this user's running process under a refused shell still
// unsettles the reading (P15, #735 r1 finding 2): the shell here is real and
// owns a real sleep; every command-line read of the sleep fails with EIO;
// match refuses both. The parent-outside shortcut settled the sleep's failed
// read through the shell and returned empty while the sleep still ran. The
// control is that the sleep is still running once the listing returns an
// unsettledError naming it.
func TestAListingDoesNotSettleAFailedReadThroughARefusedParent(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	holder := exec.Command("/bin/sh", "-c", `sleep 60 & echo $!; wait $!`)
	out, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill(); _ = holder.Wait() })
	var sleepPID int
	if _, err := fmt.Fscan(bufio.NewReader(out), &sleepPID); err != nil || sleepPID <= 0 {
		t.Fatalf("the shell did not print its sleep's pid: %v, %d", err, sleepPID)
	}
	lineage, found, err := agents.ReadLineage(sleepPID)
	if err != nil || !found || lineage.Parent != holder.Process.Pid {
		t.Fatalf("sleep %d parent is %v (found %v, %v), want shell %d", sleepPID, lineage.Parent, found, err, holder.Process.Pid)
	}
	table := &eioChildOfShell{parent: holder.Process.Pid, child: sleepPID}
	refuseAll := func(table processTable, pid int) (bool, error) {
		_, _, err := table.commandLine(pid)
		return false, err
	}
	_, err = processesWhere(table, refuseAll)
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) || unsettled.PID != sleepPID || !errors.Is(err, syscall.EIO) {
		t.Fatalf("listing = %v; want unsettled naming sleep %d and EIO (a parent-outside settle returns nil here)", err, sleepPID)
	}
	if err := syscall.Kill(sleepPID, 0); err != nil {
		t.Fatalf("sleep %d was not running once the listing returned (%v)", sleepPID, err)
	}
}

// unreadGoneEachReadingBesideMatched names only a real matched process and,
// each reading, a fresh pid that identify reports as not running: the
// unread-exit shape. The matched process is a real sleep so Identity.Presence
// is the kernel's (#735 r1 finding 3).
type unreadGoneEachReadingBesideMatched struct {
	kernel
	matched  int
	readings int
	gone     int
}

func (u *unreadGoneEachReadingBesideMatched) ids() ([]int, error) {
	u.readings++
	u.gone = 80_000 + u.readings
	return []int{u.matched, u.gone}, nil
}

func (u *unreadGoneEachReadingBesideMatched) identify(pid int) (agents.Identity, bool, error) {
	if pid == u.gone {
		return agents.Identity{}, false, nil
	}
	return u.kernel.identify(pid)
}

// An unread exit beside a matched process still unsettles (P15, #735 r1
// finding 3 / #722 control): the matched process is a real sleep (so
// Presence is not a fabricated identity), and every reading also names a
// pid that is already gone. If unread-first-time exits were ignored, the
// listing would settle with only the sleep. The control is that removing
// that unsettle leaves readings == 1 and a non-error return (checked in
// REPORT.md fail-before); with it, all 64 readings unsettle.
func TestUnreadExitBesideAMatchedProcessUnsettles(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	id, found, err := agents.Identify(sleeper.Process.Pid)
	if err != nil || !found {
		t.Fatalf("Identify(sleep) = %v, %v, %v", id, found, err)
	}
	if presence, err := id.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("sleep Presence = %v, %v; the fixture must be a real running process", presence, err)
	}
	table := &unreadGoneEachReadingBesideMatched{matched: sleeper.Process.Pid}
	acceptSleep := func(_ processTable, pid int) (bool, error) {
		_, _, err := table.commandLine(pid)
		return pid == sleeper.Process.Pid, err
	}
	_, err = processesWhere(table, acceptSleep)
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) {
		t.Fatalf("listing = %v; want unsettled from the unread gone pid", err)
	}
	// Each reading's gone pid is new, so Readings on the named cause is 1;
	// the guard is that every reading unsettled (table.readings).
	if table.readings != settleReadings {
		t.Fatalf("readings = %d, want %d (listing settled early: %v)", table.readings, settleReadings, err)
	}
}

// churnUnreadUnderRefusedParent is #722's model: every reading names a
// refused parent and a new child already gone when identified. Under the
// settle invariant, that unread exit unsettles (a refused parent does not
// place the child outside). The listing fails to settle - the open #722
// gap when never-read-whole processes keep exiting. Presence for the
// parent uses a real sleep so the fixture is not a fabricated identity.
func TestOutsideChurnOfUnreadExitsStillPreventsSettling(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	table := &unreadGoneEachReadingBesideMatched{matched: sleeper.Process.Pid}
	refuseAll := func(table processTable, pid int) (bool, error) {
		_, _, err := table.commandLine(pid)
		return false, err
	}
	_, err := processesWhere(table, refuseAll)
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) {
		t.Fatalf("listing = %v; want #722's settle failure (unread exits under a refused parent must not settle)", err)
	}
	if table.readings != settleReadings {
		t.Fatalf("readings = %d, want %d (a parent-outside settle ends in one reading)", table.readings, settleReadings)
	}
}
