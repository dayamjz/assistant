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

// EndGrace is how long Process.End and leftAfterTests give a process of this
// binary to exit on SIGTERM before they kill it.
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
// What it does not cover: a process that ends inside run without returning,
// a test's os.Exit, a panic, a timeout or a kill, runs no check; what it
// left is the owner sweep's to end, and nothing reports it.
func RunOwned(run func() int) int {
	if err := ownProcesses(); err != nil {
		fmt.Fprintln(os.Stderr, "recording this test binary as the owner of what its tests start:", err)
		return 1
	}
	setChecked(true)
	code := run()
	setChecked(false)
	if !leftAfterTests() && code == 0 {
		code = 1
	}
	return code
}

// setChecked records whether RunOwned's run is running, which is when
// requireOwned lets Tmux through.
func setChecked(running bool) {
	owning.mu.Lock()
	defer owning.mu.Unlock()
	owning.checked = running
}

// ownProcesses records this process as the owner of every process the tests
// start, and starts the owner sweep that ends those still running once it is
// gone. RunOwned calls it, so the owner is the binary that runs the tests. A
// second call does nothing.
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
		wrapperDir, err = os.MkdirTemp("", "standin-tmux")
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
	sweeper, err := startSweep(value, wrapperDir)
	if err != nil {
		_ = os.RemoveAll(wrapperDir)
		return fmt.Errorf("standin: starting the owner sweep: %w", err)
	}
	owning.value, owning.sweeper, owning.tmux = value, sweeper, wrapper
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
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	return "#!/bin/sh\n" + tmuxWrapperMark + "\n" +
		OwnerVar + "=" + quote(value) + "\nexport " + OwnerVar + "\n" +
		"exec " + quote(real) + ` "$@"` + "\n"
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
// OwnerVar spells it, the identity of the sweep it started, and the
// tmux wrapper Tmux names, empty when there was no tmux to wrap; and whether
// RunOwned's run is running (checked).
var owning struct {
	mu      sync.Mutex
	value   string
	sweeper agents.Identity
	tmux    string
	checked bool
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

// requireOwned fails tb unless it is called inside RunOwned's run, where
// ownership is recorded and the check is certain to follow. Tmux calls it,
// so a package whose tests start a terminal server cannot leave the sweep,
// the wrapper or the check unwired and pass.
func requireOwned(tb testing.TB) {
	tb.Helper()
	owning.mu.Lock()
	checked := owning.checked
	owning.mu.Unlock()
	if checked {
		return
	}
	tb.Fatalf("standin: this package's tests start terminal servers, which leave its process group, and they are not running inside standin.RunOwned, " +
		"so nothing would end one a test leaves running, or report it once the tests return. End TestMain, after Main, with:\n\n" +
		"\tos.Exit(standin.RunOwned(m.Run))\n")
}

// startSweep starts this binary again as the owner sweep for value, which
// removes wrapperDir when it is not empty, and returns its identity. It is
// not waited for beyond reaping, because it outlives this process by
// design, and it holds none of this process's files: its standard streams
// are the null device, so a go test reading this binary's output does not
// wait on it.
func startSweep(value, wrapperDir string) (agents.Identity, error) {
	self, err := os.Executable()
	if err != nil {
		return agents.Identity{}, err
	}
	cmd := exec.Command(self, sweepFlag+value, sweepDirFlag+wrapperDir)
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); name != OwnerVar {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return agents.Identity{}, err
	}
	go func() { _ = cmd.Wait() }()
	id, found, err := agents.Identify(cmd.Process.Pid)
	if err != nil || !found {
		_ = cmd.Process.Kill()
		return agents.Identity{}, fmt.Errorf("reading the identity of sweep %d (found %v): %w", cmd.Process.Pid, found, err)
	}
	return id, nil
}

// sweepRounds bounds how many times the sweep lists what is left and ends
// it, since ending a process can start another one carrying the identity
// (a shell running a trap, for one) and the sweep must itself end.
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
// outright sweeps nothing.
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

// endAll ends every process in ps at once rather than one after another, so
// a process that takes its whole EndGrace does not hold up the rest.
func endAll(ps []Process) {
	var wg sync.WaitGroup
	for _, p := range ps {
		wg.Go(func() { p.End() })
	}
	wg.Wait()
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
