//go:build linux || darwin

package standin

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// A process a test starts can outlive the test binary that started it: a
// service the product launches in a session of its own, by design, and the
// test binary AtTerminal runs again as the leader of a session of its own.
// Neither macOS nor Linux ends a process when its owner dies (Linux's
// parent-death signal reaches only an immediate parent, and a restart's
// successor is not the child of the test binary that owns it), so this file
// is the one owner watch both rest on: an owned process carries its owner's
// identity, polls for it, and ends once it is gone. A process that runs
// another program cannot poll for anything, and a terminal server is one a
// test starts that also leaves the test's process group and session, so a
// kill of either misses it; the owner sweep is the same watch run for those
// in a process of its own (sweep). The check a package runs once its tests
// have returned, leftAfterTests, is in procs_unix.go, and RunOwned is what
// runs it.

// OwnerVar names the environment variable carrying the identity of the test
// binary that owns a process this binary runs as. RunOwned sets it in the
// process that runs the tests, so whatever the tests start inherits it,
// including a service the product launches and the successor a restart
// launches, and WatchOwner reads it.
const OwnerVar = "ASSISTANT_TEST_OWNER"

// OwnerPoll is how often an owned process looks for its owner, and
// OwnerGrace how long WatchOwner gives a process's own graceful exit, on
// SIGTERM, before it kills its process group.
const (
	OwnerPoll  = 250 * time.Millisecond
	OwnerGrace = 10 * time.Second
)

// EndGrace is how long Process.End gives a process to exit on SIGTERM before
// it kills it, and how long leftAfterTests gives each process it lists to
// exit on its own before it names and ends it.
const EndGrace = 10 * time.Second

// encodeOwner is the one spelling of an owner's identity in an environment
// variable, which parseOwner reads.
func encodeOwner(id agents.Identity) string {
	return strconv.Itoa(id.PID) + ":" + strconv.FormatUint(id.Start, 10)
}

// parseOwner reads an identity encodeOwner wrote, and refuses anything else.
func parseOwner(value string) (agents.Identity, error) {
	pid, start, ok := strings.Cut(value, ":")
	var owner agents.Identity
	var perr, serr error
	owner.PID, perr = strconv.Atoi(pid)
	owner.Start, serr = strconv.ParseUint(start, 10, 64)
	if !ok || perr != nil || serr != nil || owner.PID <= 0 {
		return agents.Identity{}, fmt.Errorf("%q is not a process identity", value)
	}
	return owner, nil
}

// selfOwner is this process's identity, encoded for an owned process.
func selfOwner() (string, error) {
	self, found, err := agents.Identify(os.Getpid())
	if err != nil || !found {
		return "", fmt.Errorf("reading this test binary's own identity (found %v): %w", found, err)
	}
	return encodeOwner(self), nil
}

// awaitGone returns once owner is read as no longer running, polling every
// OwnerPoll. A read that fails counts as the owner still running, so a
// process is never ended on an identity it could not read.
func awaitGone(owner agents.Identity) {
	for {
		time.Sleep(OwnerPoll)
		if presence, err := owner.Presence(); err == nil && presence != agents.PresencePresent {
			return
		}
	}
}

// RunOwned runs a package's tests under the owner watch and the check that
// follows them, and is the status TestMain exits with. A package whose tests
// start a lasting process of its binary, run AtTerminal, or call Tmux
// calls it from TestMain after Main, AtTerminal and anything else that makes
// this binary another process, with run calling m.Run:
//
//	os.Exit(standin.RunOwned(m.Run))
//
// It records this process as the owner of every process the tests start
// (ownProcesses), runs run, and then runs leftAfterTests, and it is run's
// status unless that was 0 and the check failed, which is 1. A failure to
// record ownership is printed and is 1, with run not run. Because the check
// runs here, after run returns, and nowhere else, a package cannot record
// ownership and leave the check unwired: Tmux refuses a test anywhere but
// inside run, which is the only place the check is certain to follow.
//
// Once run returns, RunOwned has this binary start no further process
// (stopStarting) before the check lists anything, and the check lists again
// whenever what it listed has exited, until a listing comes back empty, so a
// process that one the tests left starts as it exits (a shell's EXIT trap)
// is in a listing too. Only then, on a passing check, does RunOwned end the
// owner sweep and remove the tmux wrapper's directory (retireSweep), so
// neither outlives a binary whose check passed (#454): by then no process
// the check can see is left to start one carrying the identity. When the
// check fails, the sweep is left to end, once the owner is gone, what the
// check's own ending of a process may start.
//
// What it does not cover: a process that ends inside run without returning,
// a test's os.Exit, a panic, a timeout or a kill, runs no check; what it
// left is the owner sweep's to end, and nothing reports it. A process
// running another program that the check cannot read as carrying the
// identity (leftAfterTests), which on some macOS machines includes every
// process of Apple's shells and /bin/sleep (carrying), is neither reported
// nor ended by the check, and once a passing check has retired the sweep,
// neither is a process it starts afterwards, carrying the identity or not;
// before the sweep was retired, it ended such a process only when it had
// started by the time the sweep listed, after the owner was gone. A process
// this binary starts other than through Go's fork (stopStarting) is not
// held back. And it removes no file a test left: the wrapper's directory is
// the only path it removes, so what a test leaves under os.TempDir or
// anywhere else stays.
func RunOwned(run func() int) int {
	if err := ownProcesses(); err != nil {
		fmt.Fprintln(os.Stderr, "recording this test binary as the owner of what its tests start:", err)
		return 1
	}
	setChecked(true)
	code := run()
	setChecked(false)
	stopStarting()
	if !leftAfterTests() {
		if code == 0 {
			code = 1
		}
		return code
	}
	if err := retireSweep(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL: ending this test binary's owner sweep after its check passed:", err)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// stopStarting has this binary start no further process through Go, for
// as long as it runs: it takes syscall.ForkLock for writing and never
// releases it. The syscall package's documentation of ForkLock is that a
// fork is done holding it for writing, so a goroutine still running once the
// tests have returned that starts a process waits on the lock until this
// binary exits, and starts nothing; TestNothingStartsOnceRunOwnedHasReturned
// reads that of an os/exec start, beside the same start returning before.
// A fork already under way holds the lock, so this returns once it is done,
// and the process it started is in the check's listing that follows.
// RunOwned calls it once run has returned, so a TestMain can start no
// process after RunOwned returns: such a start never returns.
//
// What it does not hold back: a process started other than through Go's
// fork (cgo calling fork or posix_spawn, a raw system call). And a
// descriptor Go creates holding ForkLock for reading, which on macOS
// os.Pipe and the net package's sockets are, waits until this binary exits
// too; nothing RunOwned does after stopStarting creates either.
func stopStarting() {
	syscall.ForkLock.Lock()
}

// retireSweep ends the owner sweep ownProcesses started, waits until it has
// been reaped, and removes the tmux wrapper's directory, which the sweep
// would have removed once it had swept. RunOwned calls it only after a
// passing check, once this binary starts nothing (stopStarting) and a
// listing came back empty, so the sweep is killed with nothing it could see
// left to end. The sweep is this process's child, so the wait is on its
// exit status and not on a poll. An error is the wrapper's directory that
// could not be removed.
func retireSweep() error {
	owning.mu.Lock()
	proc, exited, wrapper := owning.sweepProc, owning.sweepExited, owning.tmux
	owning.mu.Unlock()
	if proc != nil {
		_ = proc.Kill()
		<-exited
	}
	if wrapper != "" {
		return os.RemoveAll(filepath.Dir(wrapper))
	}
	return nil
}

// setChecked records whether RunOwned's run is running, which is when
// requireOwned lets Tmux through.
func setChecked(running bool) {
	owning.mu.Lock()
	defer owning.mu.Unlock()
	owning.checked = running
}

// mainCalled records that Main ran in this process, which ownProcesses
// checks before it starts this binary again as the owner sweep: a binary
// whose TestMain does not call Main would not be the sweep when started as
// one, so nothing would end what its tests leave behind. Main sets it
// through noteMain, the one writer.
var mainCalled bool

// noteMain records that Main ran in this process.
func noteMain() { mainCalled = true }

// ownProcesses records this process as the owner of every process the tests
// start, and starts the owner sweep that ends those still running once it is
// gone, unless a passing check retires it first (RunOwned). RunOwned calls
// it, so the owner is the binary that runs the tests. A second call does
// nothing.
//
// The identity reaches what the tests start through the environment
// (OwnerVar), which every process started with this process's environment
// inherits. A tmux server is also reached when it is started with an
// environment without it: ownProcesses puts a tmux of its own (tmuxWrapper)
// first on this process's PATH, which Tmux names and every process that
// looks tmux up on that PATH finds, and it puts the identity back in the
// environment of the real tmux it runs, so the server it starts, and the
// processes in that server's panes, carry it. That a tmux server and the
// process in its pane carry it is read, not assumed:
// TestATmuxServerATestLeavesEndsWhenItsOwnerIsKilled reads it of real ones,
// started with this environment and with one built from nothing.
//
// The sweep is this binary started again with sweepFlag, in a session of its
// own and with OwnerVar removed, so neither a kill of this process's group
// nor this process's own watch ends it, and it is never one of the processes
// it sweeps. It removes the wrapper's directory once it has swept.
func ownProcesses() error {
	owning.mu.Lock()
	defer owning.mu.Unlock()
	if owning.value != "" {
		return nil
	}
	if !mainCalled {
		return errors.New("standin: RunOwned was called before Main, so the owner sweep it starts would not be one")
	}
	value, err := selfOwner()
	if err != nil {
		return err
	}
	if err := os.Setenv(OwnerVar, value); err != nil {
		return err
	}
	real, path := realTmux(os.Getenv("PATH"))
	var wrapper, wrapperDir string
	if real != "" {
		// The wrapper's directory carries the namespace the running tests
		// registered, so their next start reclaims it when this process and
		// the sweeper that removes it below are both gone.
		wrapperDir, err = TempDir("tmux-")
		if err != nil {
			return fmt.Errorf("standin: making a directory for the tmux tests find: %w", err)
		}
		wrapper = filepath.Join(wrapperDir, "tmux")
		if err := os.WriteFile(wrapper, []byte(tmuxWrapper(value, real)), 0o700); err != nil {
			_ = os.RemoveAll(wrapperDir)
			return fmt.Errorf("standin: writing the tmux tests find: %w", err)
		}
		if err := os.Setenv("PATH", strings.Join(append([]string{wrapperDir}, path...), string(os.PathListSeparator))); err != nil {
			_ = os.RemoveAll(wrapperDir)
			return err
		}
	}
	sweeper, proc, exited, err := startSweep(value, wrapperDir)
	if err != nil {
		_ = os.RemoveAll(wrapperDir)
		return fmt.Errorf("standin: starting the owner sweep: %w", err)
	}
	owning.value, owning.sweeper, owning.tmux = value, sweeper, wrapper
	owning.sweepProc, owning.sweepExited = proc, exited
	return nil
}

// tmuxWrapperMark is the line that marks a tmux tmuxWrapper wrote, so a test
// binary started by another one's tests looks past that one's wrapper for the
// real tmux rather than wrapping a wrapper that would put the other owner's
// identity back.
const tmuxWrapperMark = "# standin: the tmux a test finds, which runs the real one with the owner identity put back"

// tmuxWrapper is a script that runs real with OwnerVar set to value in its
// environment, whatever environment it was started with, and every
// argument it was given.
func tmuxWrapper(value, real string) string {
	return "#!/bin/sh\n" + tmuxWrapperMark + "\n" +
		OwnerVar + "=" + shellWord(value) + "\nexport " + OwnerVar + "\n" +
		"exec " + shellWord(real) + ` "$@"` + "\n"
}

// realTmux is the first tmux on path that is not a wrapper tmuxWrapper wrote,
// as an absolute path, empty when there is none, and path's directories
// without those holding such a wrapper. It looks a directory up as
// exec.LookPath does on these platforms, for an executable regular file, and
// passes over an empty entry rather than reading it as the working
// directory.
func realTmux(path string) (string, []string) {
	var real string
	var kept []string
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, "tmux")
		info, err := os.Stat(candidate)
		executable := err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
		if executable && isTmuxWrapper(candidate) {
			continue
		}
		kept = append(kept, dir)
		if executable && real == "" {
			if abs, err := filepath.Abs(candidate); err == nil {
				real = abs
			}
		}
	}
	return real, kept
}

// isTmuxWrapper reports whether the file at path is one tmuxWrapper wrote.
func isTmuxWrapper(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, len("#!/bin/sh\n")+len(tmuxWrapperMark))
	n, _ := io.ReadFull(f, head)
	return string(head[:n]) == "#!/bin/sh\n"+tmuxWrapperMark
}

// owning is what ownProcesses recorded: this process's owner identity as
// OwnerVar spells it, the identity of the sweep it started, the sweep's
// process and a channel closed once it has been waited for (retireSweep),
// and the tmux wrapper Tmux names, empty when there was no tmux to wrap; and
// whether RunOwned's run is running (checked).
var owning struct {
	mu          sync.Mutex
	value       string
	sweeper     agents.Identity
	sweepProc   *os.Process
	sweepExited chan struct{}
	tmux        string
	checked     bool
}

// owned is what ownProcesses recorded, empty when it was not called.
func owned() (value string, sweeper agents.Identity) {
	owning.mu.Lock()
	defer owning.mu.Unlock()
	return owning.value, owning.sweeper
}

// ownedTmux is the tmux wrapper ownProcesses wrote, empty when it wrote none.
func ownedTmux() string {
	owning.mu.Lock()
	defer owning.mu.Unlock()
	return owning.tmux
}

// requireOwned fails tb unless it is called inside RunOwned's run in this
// process, where ownership is recorded and the check is certain to follow,
// and otherwise returns this process's owner identity as OwnerVar spells it.
// The proof is state only RunOwned sets in this process, never OwnerVar,
// which a binary another package's tests started inherits without running
// RunOwned. starts says what the test starts, for the refusal. Tmux and
// HeldWait call it, so a package whose tests start a terminal server or a
// held process cannot leave the sweep, the wrapper or the check unwired
// and pass.
func requireOwned(tb testing.TB, starts string) string {
	tb.Helper()
	owning.mu.Lock()
	checked, value := owning.checked, owning.value
	owning.mu.Unlock()
	if checked && value != "" {
		return value
	}
	tb.Fatalf("standin: this test starts %s and is not running inside standin.RunOwned in this process, "+
		"so nothing would end one it leaves running, or report it once the tests return. End TestMain, after Main, with:\n\n"+
		"\tos.Exit(standin.RunOwned(m.Run))\n", starts)
	return ""
}

// startSweep starts this binary again as the owner sweep for value, which
// removes wrapperDir when it is not empty, and returns its identity, its
// process, and a channel closed once it has been waited for. It is not
// waited for beyond reaping, because it outlives this process by design
// unless a passing check retires it first (retireSweep), and it holds none
// of this process's files: its standard streams are the null device, so a
// go test reading this binary's output does not wait on it.
func startSweep(value, wrapperDir string) (agents.Identity, *os.Process, chan struct{}, error) {
	self, err := os.Executable()
	if err != nil {
		return agents.Identity{}, nil, nil, err
	}
	cmd := exec.Command(self, sweepFlag+value, sweepDirFlag+wrapperDir)
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); name != OwnerVar {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return agents.Identity{}, nil, nil, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	id, found, err := agents.Identify(cmd.Process.Pid)
	if err != nil || !found {
		_ = cmd.Process.Kill()
		return agents.Identity{}, nil, nil, fmt.Errorf("reading the identity of sweep %d (found %v): %w", cmd.Process.Pid, found, err)
	}
	return id, cmd.Process, exited, nil
}

// sweepRounds bounds how many times the sweep lists what is left and ends
// it, and how many times leftAfterTests lists what is left, since a process
// ending, on a signal or on its own, can start another one carrying the
// identity (a shell running an EXIT trap, for one), and both must
// themselves end.
const sweepRounds = 3

// sweep is the owner sweep for the owner value names, which Main runs when
// this binary was started with sweepFlag: it waits, with WatchOwner's watch,
// until the owner is gone, and then ends every process started with the
// owner's identity in its environment (carrying), whatever its executable,
// listing again after each round until nothing is left or sweepRounds have
// run, and then removes wrapperDir, the owner's tmux wrapper, when that is
// not empty. It exits 0 when the last listing was empty and 1 otherwise;
// nobody reads the status, since the owner is gone, so the status is for a
// person reading the process table.
//
// What it does not cover: an owner read as present that is gone is noticed
// within OwnerPoll, and a read of it that fails counts as present, so a
// failed read keeps the sweep, and what it would end, running; a process
// started with an environment that dropped OwnerVar does not carry the
// identity unless it is a tmux the wrapper ran (ownProcesses) or a process
// in that tmux's panes, and one whose environment the read cannot find
// (carrying) does not show it, so neither is seen; and a sweep killed
// outright sweeps nothing, which is how retireSweep ends one, once a passing
// check has left nothing the sweep could see.
func sweep(value, wrapperDir string) int {
	owner, err := parseOwner(value)
	if err != nil {
		return 1
	}
	awaitGone(owner)
	code := 1
	for range sweepRounds {
		left, err := carrying(value)
		if err != nil {
			break
		}
		if len(left) == 0 {
			code = 0
			break
		}
		endAll(left)
	}
	if wrapperDir != "" {
		_ = os.RemoveAll(wrapperDir)
	}
	return code
}

// endAll ends every process in ps as one set (endSet), signalling each with
// End's check of its identity first (Process.signal).
func endAll(ps []Process) {
	endSet(ps, Process.signal)
}

// endSet ends every process in ps as one set. It sends each SIGTERM with
// send, which reports whether it sent it, every process before any child of
// its in ps (parentsFirst), and only then gives each it signalled EndGrace to
// exit, all at once rather than one after another, so one that takes its
// whole EndGrace does not hold up the rest; one still running then is killed
// as End kills one.
//
// The order is what makes the processes one set. A process can act on its
// child's ending: a shell whose child ends runs on to its EXIT trap, which
// can start a process no listing has seen. Each sent SIGTERM from a
// goroutine of its own, as End once sent it here, whether the child or the
// shell met its signal first was the scheduler's choice, and a check that
// ended the last of a chain of such shells could start the next process
// itself (#558). Sent parents first, a process has been sent its own
// SIGTERM before any child of its in ps is sent one, however long the
// sender is held between the two.
//
// What it does not cover: a process whose parent cannot be read is ordered
// as though it had none in ps (parentsFirst); a process that learns of the
// ending of one in ps that is not its child, a sibling's through a pipe for
// one, can act on it before its own SIGTERM when that one comes first; and
// what a process does on its own SIGTERM is its own, so what that starts is
// left to whoever lists next.
func endSet(ps []Process, send func(Process, syscall.Signal) bool) {
	var signalled []Process
	for _, p := range parentsFirst(ps) {
		if send(p, syscall.SIGTERM) {
			signalled = append(signalled, p)
		}
	}
	var wg sync.WaitGroup
	for _, p := range signalled {
		wg.Go(func() { p.killUnlessGone() })
	}
	wg.Wait()
}

// parentsFirst is ps ordered so that each process comes after the one in ps
// that is its parent, and otherwise in ps's order. A process's parent is read
// from the kernel (agents.ReadLineage), and the one in ps with that
// identifier is taken for it only when the read is of the process ps names
// and that one started no later than it, since a process starts after its
// parent (agents.Lineage); a process whose parent cannot be read so is
// ordered as though it had none in ps.
func parentsFirst(ps []Process) []Process {
	byPID := map[int]Process{}
	for _, p := range ps {
		byPID[p.PID] = p
	}
	parentOf := map[agents.Identity]Process{}
	for _, p := range ps {
		lineage, found, err := agents.ReadLineage(p.PID)
		if err != nil || !found || lineage.Identity != p.Identity {
			continue
		}
		if parent, ok := byPID[lineage.Parent]; ok && parent.Identity != p.Identity && parent.Start <= p.Start {
			parentOf[p.Identity] = parent
		}
	}
	ordered := make([]Process, 0, len(ps))
	placed := map[agents.Identity]bool{}
	var place func(Process)
	place = func(p Process) {
		if placed[p.Identity] {
			return
		}
		placed[p.Identity] = true
		if parent, ok := parentOf[p.Identity]; ok {
			place(parent)
		}
		ordered = append(ordered, p)
	}
	for _, p := range ps {
		place(p)
	}
	return ordered
}

// WatchOwner has this process end once the test binary named by OwnerVar is
// gone, and does nothing when the variable is unset; a value that does not
// parse ends this process at once with a message, since nothing would end it
// with its owner. A package whose tests start this binary as a lasting
// process calls it first thing in TestMain. The process is first sent
// SIGTERM, which a service's own shutdown handles, and then, OwnerGrace
// later, SIGKILL, to its whole group when it leads one, which a launched
// service does, since the launch gives it a session of its own.
//
// What it does not cover: an owner read as present that is gone is noticed
// within OwnerPoll, so a process outlives its owner by up to that plus
// OwnerGrace; a process stopped by a signal runs no watcher; and what this
// process started in a session or group of its own, a terminal server for
// one, is reached by neither signal, so it is left to the service's own
// shutdown and, where it carries the owner's identity, to the owner's sweep.
func WatchOwner() {
	value := os.Getenv(OwnerVar)
	if value == "" {
		return
	}
	owner, err := parseOwner(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "standin: the test binary's owner %s: %v, so nothing would end this process with it\n", OwnerVar, err)
		os.Exit(1)
	}
	go func() {
		awaitGone(owner)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(OwnerGrace)
		if group, err := syscall.Getpgid(0); err == nil && group == os.Getpid() {
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}()
}

// watchOuter has the test binary AtTerminal ran again end, with its whole
// process group, once the outer binary named by value is gone, and on every
// signal that would otherwise end it without its group. It is WatchOwner's
// watch with a different ending: the binary run again is a test binary with
// nobody left to report to, not a service with a shutdown of its own to run,
// so it kills its group at once rather than asking first, and everything the
// tests started in that group goes with it.
//
// The watch alone does not reach the ending when a signal ends this process
// first, which the outer's death sends it: the terminal's master side closes
// with the outer and the kernel hangs the terminal up, which ends a process
// with the hangup signal handled normally at once, well inside the OwnerPoll
// the watch sleeps, and leaves the group behind. So the hangup, termination
// and interrupt signals, each of which ends a Go program by default and the
// outer forwards, are caught and lead to the same ending, endGroup, rather
// than a second one. A signal this process was started ignoring is left
// ignored, because catching it would undo the caller's choice and it ends
// nothing; the watch is what ends this process then. What reaches neither:
// the quit signal, which is left to the runtime so a test timeout's goroutine
// dump is kept, SIGKILL, a signal arriving before AtTerminal runs, and a
// panic, each of which ends this process alone.
func watchOuter(value string) error {
	outer, err := parseOwner(value)
	if err != nil {
		return err
	}
	var caught []os.Signal
	for _, sig := range []os.Signal{syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT} {
		if !signal.Ignored(sig) {
			caught = append(caught, sig)
		}
	}
	ending := make(chan os.Signal, 1)
	if len(caught) > 0 {
		signal.Notify(ending, caught...)
	}
	gone := make(chan struct{})
	go func() {
		awaitGone(outer)
		close(gone)
	}()
	go func() {
		select {
		case <-ending:
		case <-gone:
		}
		endGroup()
	}()
	return nil
}

// endGroup kills this process's group when it leads one, which the binary
// AtTerminal ran again does, and then this process.
func endGroup() {
	if group, err := syscall.Getpgid(0); err == nil && group == os.Getpid() {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	}
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
}
