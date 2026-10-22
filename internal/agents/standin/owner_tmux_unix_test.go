//go:build linux || darwin

package standin

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
)

// The variables a test hands this binary when it starts it as a test binary
// that owns a tmux server (tmuxOwnerMain): the directory it records itself
// in, the socket it starts the server on, and what it does then. It resolves
// the tmux it starts itself, through Tmux, since the one this test's Tmux
// names puts this test's owner identity back and not that binary's.
const (
	tmuxOwnerDirVar    = "STANDIN_TEST_TMUX_OWNER_DIR"
	tmuxOwnerSocketVar = "STANDIN_TEST_TMUX_OWNER_SOCKET"
	tmuxOwnerModeVar   = "STANDIN_TEST_TMUX_OWNER_MODE"
	tmuxOwnerRecord    = "record"
	// tmuxPaneArg makes this binary the process in the server's pane, which
	// waits forever on its terminal as a stand-in worker does. It is an
	// argument rather than a variable, because the pane inherits the owner's
	// variables through the server.
	tmuxPaneArg = "-standin-test-tmux-pane"
	// trapExitArg makes this binary a process a test leaves that exits on
	// its own and, as it exits, has a shell's EXIT trap start another
	// process carrying the owner identity (trapExitMain). trapChildRecord is
	// the file in the owner's directory the trap writes that process's
	// identifier to.
	trapExitArg     = "-standin-test-trap-exit"
	trapChildRecord = "trap-child"
	// trapChainVar is how many more processes like itself a trapExitMain
	// process has its trap start, one after another, before the last one's
	// trap starts a pane's process; unset is none.
	trapChainVar = "STANDIN_TEST_TRAP_CHAIN"
	// lateChildRecord is the file in the owner's directory an ownerLate
	// owner writes the identifier of a process it started after RunOwned
	// returned to, which it must never be able to do.
	lateChildRecord = "late-child"
)

// lateBound is how long an ownerLate owner gives a process start to return,
// before the tests return as the control and after RunOwned returned.
const lateBound = 5 * time.Second

// The modes of tmuxOwnerMain. ownerWaits leaves the server running and
// waits forever, so nothing but the owner sweep can end the server once the
// owner is killed; ownerStrips does the same with the server started under
// an environment built from nothing, without the owner identity, the way
// the adapter starts one under a test's base environment; ownerLeaves leaves
// it running and returns, so RunOwned runs its check the way a package's
// TestMain does once its tests return; ownerCleans ends it first and then
// returns, the control that the check passes when nothing is left;
// ownerTraps ends it too and returns leaving a process that, as it exits,
// starts another (trapExitMain); ownerChains does the same with a chain of
// sweepRounds such processes, each started by the previous one's exit;
// ownerLate ends it too, and starts a process inside RunOwned's run and
// then again once RunOwned has returned (lateMain); ownerUnowned asks Tmux
// outside RunOwned, with no ownership recorded; ownerUnchecked records
// ownership, as a TestMain that started the sweep and wired no check would,
// and asks Tmux outside RunOwned.
const (
	ownerWaits     = "wait"
	ownerStrips    = "strip"
	ownerLeaves    = "leave"
	ownerCleans    = "clean"
	ownerUnowned   = "unowned"
	ownerUnchecked = "unchecked"
	ownerTraps     = "trap"
	ownerLate      = "late"
	ownerChains    = "chain"
)

// exitLeft is the status tmuxOwnerMain exits with when RunOwned's check
// failed, which is RunOwned's 1 for tests that passed; exitBroken the one
// when the owner could not do what its mode says; and exitRefused the one
// when Tmux's refusal fired.
const (
	exitLeft    = 1
	exitBroken  = 2
	exitRefused = 4
)

// tmuxOwnerMain makes this binary a test binary whose test started a tmux
// server and did not end it, when a test started it as one, and returns
// otherwise. Inside RunOwned, as a package's tests run, it records its owner
// identity, the server's and its pane's process identifiers, its sweep's,
// and the tmux wrapper Tmux named, and then does what its mode says.
func tmuxOwnerMain() {
	if slices.Contains(os.Args[1:], tmuxPaneArg) {
		for {
			time.Sleep(time.Hour)
		}
	}
	if slices.Contains(os.Args[1:], trapExitArg) {
		os.Exit(trapExitMain())
	}
	dir := os.Getenv(tmuxOwnerDirVar)
	if dir == "" {
		return
	}
	mode := os.Getenv(tmuxOwnerModeVar)
	if mode == ownerUnowned || mode == ownerUnchecked {
		if mode == ownerUnchecked {
			if err := ownProcesses(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(exitBroken)
			}
		}
		if _, refusal := tmuxOutsideATest(); refusal != "" {
			fmt.Fprint(os.Stderr, refusal)
			os.Exit(exitRefused)
		}
		os.Exit(0)
	}
	if mode == ownerLate {
		os.Exit(lateMain(dir))
	}
	os.Exit(RunOwned(func() int { return tmuxOwnerTests(dir, mode) }))
}

// lateMain is an ownerLate owner: inside RunOwned's run it starts a process
// of this binary, which must return within lateBound, and ends it, the
// control that a start here returns in that time; then it runs the tests as
// ownerCleans does, so the check passes. Once RunOwned has returned, which
// is where a goroutine a test left still running would start one, it
// starts the same process again, and records it in lateChildRecord when
// the start returned within lateBound. It returns RunOwned's status, or
// exitBroken when the control's start did not return.
func lateMain(dir string) int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "locating this binary:", err)
		return exitBroken
	}
	code := RunOwned(func() int {
		cmd, err := startWithin(self, lateBound)
		if err != nil {
			fmt.Fprintln(os.Stderr, "the control's start inside the tests:", err)
			return exitBroken
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return tmuxOwnerTests(dir, ownerLate)
	})
	if cmd, err := startWithin(self, lateBound); err == nil {
		_ = writeWhole(filepath.Join(dir, lateChildRecord), []byte(strconv.Itoa(cmd.Process.Pid)))
		fmt.Fprintf(os.Stderr, "started process %d after RunOwned returned\n", cmd.Process.Pid)
	}
	return code
}

// startWithin starts this binary as a pane's process, which runs until
// something ends it, and returns it once the start has returned, or an
// error when the start failed or had not returned within bound.
func startWithin(self string, bound time.Duration) (*exec.Cmd, error) {
	cmd := exec.Command(self, "-test.run=^$", tmuxPaneArg)
	started := make(chan error, 1)
	go func() { started <- cmd.Start() }()
	select {
	case err := <-started:
		return cmd, err
	case <-time.After(bound):
		return nil, fmt.Errorf("the start had not returned %v later", bound)
	}
}

// tmuxOwnerTests is the tests of the binary tmuxOwnerMain makes, run inside
// RunOwned: it starts the server, records it, and leaves it running, ends
// it, or waits, as mode says.
func tmuxOwnerTests(dir, mode string) int {
	fail := func(what string, err error) {
		fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
		os.Exit(exitBroken)
	}
	self, err := os.Executable()
	if err != nil {
		fail("locating this binary", err)
	}
	tmux, refusal := tmuxOutsideATest()
	if refusal != "" {
		fail("resolving tmux", errors.New(refusal))
	}
	socket := os.Getenv(tmuxOwnerSocketVar)
	start := exec.Command(tmux, "-S", socket, "-f", os.DevNull, "new-session", "-d", "-s", "owned",
		"-x", "80", "-y", "24", "--", self, "-test.run=^$", tmuxPaneArg)
	if mode == ownerStrips {
		start.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
	}
	if out, err := start.CombinedOutput(); err != nil {
		fail("starting a tmux server", fmt.Errorf("%w: %s", err, out))
	}
	pids, err := exec.Command(tmux, "-S", socket, "display-message", "-p", "#{pid} #{pane_pid}").Output()
	if err != nil {
		fail("reading the tmux server's process identifiers", err)
	}
	value, sweeper := owned()
	record := fmt.Sprintf("%s %s %d %s %d", value, strings.TrimSpace(string(pids)), sweeper.PID, tmux, sweeper.Start)
	// Whole or not at all, since startTmuxOwner polls this one path and
	// parses the first content it reads there as the record (#459).
	if err := writeWhole(filepath.Join(dir, tmuxOwnerRecord), []byte(record)); err != nil {
		fail("recording the tmux server", err)
	}
	switch mode {
	case ownerWaits, ownerStrips:
		for {
			time.Sleep(time.Hour)
		}
	case ownerCleans, ownerTraps, ownerChains, ownerLate:
		if err := exec.Command(tmux, "-S", socket, "kill-server").Run(); err != nil {
			fail("ending the tmux server", err)
		}
	}
	if mode == ownerTraps || mode == ownerChains {
		trap := exec.Command(self, "-test.run=^$", trapExitArg)
		if mode == ownerChains {
			trap.Env = append(os.Environ(), trapChainVar+"="+strconv.Itoa(sweepRounds-1))
		}
		if err := trap.Start(); err != nil {
			fail("starting the process whose exit starts another", err)
		}
		// Reaped as it exits, so the check reads it gone rather than a
		// zombie of this process.
		go func() { _ = trap.Wait() }()
	}
	return 0
}

// trapExitMain is a process a test leaves that exits on its own, within
// EndGrace, and as it exits starts another process carrying the owner
// identity, the case sweepRounds exists for: a shell whose EXIT trap starts
// this binary as a pane's process, which runs until something ends it, or,
// while trapChainVar is above zero, as another process like this one with
// it one lower, and writes that process's identifier to trapChildRecord in
// the owner's directory. The shell runs for two seconds first, so the check
// lists this process before it exits. It is this binary waiting on the
// shell, rather than the shell itself, because on macOS the read finds no
// environment for Apple's /bin/sh, so a shell is seen by neither the check
// nor the sweep there; this binary is listed by its executable on both
// platforms, and it exits only once the trap has started its child. Ended
// by SIGTERM, as the check ends what it names, it kills the shell first, so
// the trap never runs and an ended chain starts nothing more.
func trapExitMain() int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "locating this binary:", err)
		return exitBroken
	}
	next := []string{"-test.run=^$", tmuxPaneArg}
	env := os.Environ()
	if chain, _ := strconv.Atoi(os.Getenv(trapChainVar)); chain > 0 {
		next = []string{"-test.run=^$", trapExitArg}
		env = append(env, trapChainVar+"="+strconv.Itoa(chain-1))
	}
	shell := exec.Command("/bin/sh", append([]string{"-c",
		`trap '"$0" "$@" </dev/null >/dev/null 2>&1 & echo $! >"$TRAP_RECORD"' EXIT; sleep 2`,
		self}, next...)...)
	shell.Env = append(env, "TRAP_RECORD="+filepath.Join(os.Getenv(tmuxOwnerDirVar), trapChildRecord))
	ended := make(chan os.Signal, 1)
	signal.Notify(ended, syscall.SIGTERM)
	if err := shell.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "starting the shell whose exit starts a process:", err)
		return exitBroken
	}
	exited := make(chan error, 1)
	go func() { exited <- shell.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			fmt.Fprintln(os.Stderr, "running the shell whose exit starts a process:", err)
			return exitBroken
		}
		return 0
	case <-ended:
		_ = shell.Process.Kill()
		<-exited
		return 0
	}
}

// tmuxOutsideATest is what Tmux answers outside a test, which is how
// tmuxOwnerMain reads it: the path it names, or the message of the Fatalf
// that refused. The testing.TB it is given records that message and ends
// Tmux there, as a test's Fatalf ends the test; its other methods are the
// nil TB's and panic, so a Tmux that reached for any other, a skip among
// them, ends this binary with the panic rather than reading as either.
func tmuxOutsideATest() (path, refusal string) {
	tb := &refusalTB{}
	defer func() {
		if recovered := recover(); recovered != nil {
			if _, ok := recovered.(refused); !ok {
				panic(recovered)
			}
			refusal = tb.message
		}
	}()
	return Tmux(tb), ""
}

// refusalTB is the testing.TB tmuxOutsideATest gives Tmux.
type refusalTB struct {
	testing.TB
	message string
}

// refused is what refusalTB's Fatalf panics with, so tmuxOutsideATest
// recovers that and nothing else.
type refused struct{}

func (r *refusalTB) Helper() {}

func (r *refusalTB) Fatalf(format string, args ...any) {
	r.message = fmt.Sprintf(format, args...)
	panic(refused{})
}

// tmuxOwned is what a test binary started by startTmuxOwner recorded: the
// owner identity its processes carry, its tmux server and the server's pane,
// its owner sweep, and the tmux wrapper its Tmux named.
type tmuxOwned struct {
	cmd                  *exec.Cmd
	done                 chan struct{}
	stderr               *bytes.Buffer
	value                string
	server, pane, sweep  agents.Identity
	wrapper              string
	tmux, socket, record string
}

// startTmuxOwner starts this binary as a test binary owning a tmux server in
// mode, in a session of its own so that a kill of this test's group does not
// reach it and the kill below is of it alone, and waits for its record. Every
// process it names is ended in cleanup where it outlives the test.
func startTmuxOwner(t *testing.T, mode string) *tmuxOwned {
	t.Helper()
	return startTmuxOwnerOf(t, mode, false)
}

// startTmuxOwnerCopy is startTmuxOwner with the owner a copy of this binary
// written for it alone, so its check lists the copy's processes and never
// those of this binary that other parallel tests start, nor theirs its.
func startTmuxOwnerCopy(t *testing.T, mode string) *tmuxOwned {
	t.Helper()
	return startTmuxOwnerOf(t, mode, true)
}

func startTmuxOwnerOf(t *testing.T, mode string, copied bool) *tmuxOwned {
	t.Helper()
	o := &tmuxOwned{tmux: Tmux(t), socket: filepath.Join(SocketDir(t), "o"), done: make(chan struct{}), stderr: &bytes.Buffer{}}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if copied {
		raw, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		self = filepath.Join(dir, "owner.test")
		if err := os.WriteFile(self, raw, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	o.record = filepath.Join(dir, tmuxOwnerRecord)
	o.cmd, err = StartCopied(func() *exec.Cmd {
		cmd := exec.Command(self, "-test.run=^$")
		cmd.Env = append(os.Environ(), tmuxOwnerDirVar+"="+dir,
			tmuxOwnerSocketVar+"="+o.socket, tmuxOwnerModeVar+"="+mode)
		o.stderr.Reset()
		cmd.Stderr = o.stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		return cmd
	})
	if err != nil {
		t.Fatalf("starting the owner: %v", err)
	}
	go func() { _ = o.cmd.Wait(); close(o.done) }()
	t.Cleanup(func() {
		select {
		case <-o.done:
		default:
			_ = syscall.Kill(o.cmd.Process.Pid, syscall.SIGKILL)
			<-o.done
		}
		_ = exec.Command(o.tmux, "-S", o.socket, "kill-server").Run()
		for _, id := range []agents.Identity{o.pane, o.server} {
			if id.PID == 0 {
				continue
			}
			if presence, err := id.Presence(); err == nil && presence == agents.PresencePresent {
				_ = syscall.Kill(id.PID, syscall.SIGKILL)
			}
		}
		// The sweep is given the time it takes to notice its owner is gone,
		// sweep what is left and remove the wrapper, which a kill would
		// leave behind; one still running then is killed and fails the test.
		if o.sweep.PID != 0 && !(Process{Identity: o.sweep}).GoneWithin(OwnerPoll+EndGrace+5*time.Second) {
			t.Errorf("the owner's sweep %v was still running long after its owner was gone", o.sweep)
			_ = syscall.Kill(o.sweep.PID, syscall.SIGKILL)
			if o.wrapper != "" {
				_ = os.RemoveAll(filepath.Dir(o.wrapper))
			}
		}
	})
	if mode == ownerUnowned || mode == ownerUnchecked {
		return o
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(o.record)
		if err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) != 6 {
				t.Fatalf("the owner recorded %q", raw)
			}
			o.value, o.wrapper = fields[0], fields[4]
			// An owner that ends its server ends it before this read can, so
			// a process already gone is a zero identity, which nothing
			// below signals.
			o.server, o.pane = identifyRunning(t, fields[1]), identifyRunning(t, fields[2])
			// The sweep is the owner's own record of it, never a read of a
			// running process: an owner whose check passes retires its sweep
			// (RunOwned), which can be before this read.
			pid, perr := strconv.Atoi(fields[3])
			start, serr := strconv.ParseUint(fields[5], 10, 64)
			if perr != nil || serr != nil || pid <= 0 || start == 0 {
				t.Fatalf("the owner recorded its sweep as %q started at %q", fields[3], fields[5])
			}
			o.sweep = agents.Identity{PID: pid, Start: start}
			return o
		}
		select {
		case <-o.done:
			t.Fatalf("the owner exited before it recorded its tmux server: %s", o.stderr)
		default:
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the owner never recorded its tmux server")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// identifyRunning reads the identity of the process numbered by field, and
// is the zero identity when that process is not running.
func identifyRunning(t *testing.T, field string) agents.Identity {
	t.Helper()
	pid, err := strconv.Atoi(field)
	if err != nil {
		t.Fatalf("%q is not a process identifier", field)
	}
	id, found, err := agents.Identify(pid)
	if err != nil {
		t.Fatalf("reading the identity of process %d: %v", pid, err)
	}
	if !found {
		return agents.Identity{}
	}
	return id
}

// A tmux server a test starts and does not end, and the process in its
// pane, end once the test binary that started it is killed outright, which
// is #418: the server leaves the binary's process group and session, so a
// kill of the binary, or of its group, misses it, and the pane's process
// waits on a terminal nothing ever closes. The server is read from the
// kernel as outside the owner's group and not its child, which is the
// control that only the sweep can end it; the check's own read (carrying)
// lists the server and the pane by the owner identity in their environment
// while the owner runs, which is the control that the read sees a real tmux
// server at all; and the sweep is read as running. Then the owner is sent
// SIGKILL, and the server, the pane's process and the sweep itself are all
// read as gone within OwnerPoll and a margin, and so is the directory of the
// tmux wrapper the owner's Tmux named. The pane runs this binary, as a
// stand-in worker's does, since on macOS the read finds no environment for
// some programs, an Apple-signed /bin/sleep among them.
//
// It holds for a server started under the owner's environment and for one
// started under an environment built from nothing, without the owner
// identity, the way the adapter starts one under a test's base environment
// (claudeTerminalEnv in internal/agents), because the tmux Tmux names puts
// the identity back. For the second, the pane's environment is read as the
// one built from nothing, the control that it is what the server started
// with.
func TestATmuxServerATestLeavesEndsWhenItsOwnerIsKilled(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{ownerWaits, ownerStrips} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			tmuxServerEndsWithItsOwner(t, mode)
		})
	}
}

// tmuxServerEndsWithItsOwner is TestATmuxServerATestLeavesEndsWhenItsOwnerIsKilled
// for an owner in mode.
func tmuxServerEndsWithItsOwner(t *testing.T, mode string) {
	o := startTmuxOwner(t, mode)
	owner := o.cmd.Process.Pid
	if o.server.PID == 0 || o.pane.PID == 0 || o.sweep.PID == 0 {
		t.Fatalf("the owner's server %v, pane %v or sweep %v was gone before the owner was killed", o.server, o.pane, o.sweep)
	}

	lineage, found, err := agents.ReadLineage(o.server.PID)
	if err != nil || !found {
		t.Fatalf("ReadLineage(the tmux server) = %+v, %v, %v", lineage, found, err)
	}
	if lineage.Parent == owner || lineage.Group == owner {
		t.Fatalf("the tmux server reads %+v; want it neither the owner %d's child nor in its group, or a kill of the owner would reach it and this test would show nothing about the sweep",
			lineage, owner)
	}
	listed, err := carrying(o.value)
	if err != nil {
		t.Fatalf("listing what carries the owner's identity: %v", err)
	}
	for _, p := range []struct {
		name string
		id   agents.Identity
	}{{"the tmux server", o.server}, {"the process in its pane", o.pane}} {
		if !slices.ContainsFunc(listed, func(q Process) bool { return q.Identity == p.id }) {
			t.Fatalf("the read of what carries the owner's identity lists %v, not %s %v; the sweep and the check could not see it", listed, p.name, p.id)
		}
	}
	if slices.ContainsFunc(listed, func(q Process) bool { return q.Identity == o.sweep }) {
		t.Fatalf("the read of what carries the owner's identity lists the sweep %v, which would end itself", o.sweep)
	}
	if presence, err := o.sweep.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the owner's sweep reads %v (%v) while the owner runs; want it running", presence, err)
	}
	if mode == ownerStrips {
		env, err := startEnvironment(o.pane.PID)
		if err != nil || !slices.Contains(env, "PATH=/usr/bin:/bin") {
			t.Fatalf("the pane's environment reads %q (%v); want the one built from nothing, or this case shows nothing the other does not", env, err)
		}
	}
	if _, err := os.Stat(o.wrapper); err != nil {
		t.Fatalf("the owner's tmux wrapper %s reads %v while the owner runs; want it there", o.wrapper, err)
	}

	_ = syscall.Kill(owner, syscall.SIGKILL)
	<-o.done
	bound := OwnerPoll + 5*time.Second
	for _, p := range []struct {
		name string
		id   agents.Identity
	}{{"the tmux server", o.server}, {"the process in its pane", o.pane}, {"the owner's sweep", o.sweep}} {
		if !(Process{Identity: p.id}).GoneWithin(bound) {
			t.Errorf("%s, %v, was still running %v after the owner was killed", p.name, p.id, bound)
		}
	}
	if _, err := os.Stat(filepath.Dir(o.wrapper)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the owner's tmux wrapper directory %s reads %v once its sweep is gone; want it removed", filepath.Dir(o.wrapper), err)
	}
}

// RunOwned's check fails a package whose tests leave a tmux server running,
// although the server runs another executable than the package's binary: the
// owner's tests leave one and return inside RunOwned, the way a package's
// tests do, and the check reports false, names the server's process on
// standard error, and has ended it by the time the owner exits, which is
// before the sweep could have (it waits OwnerPoll after the owner is gone).
// It leaves this test binary's own sweep running, which is a process of the
// same binary that no test started. The control is the same owner ending its
// server first, which the check passes, so the failure above is the server's
// doing and not the check's.
func TestRunOwnedFailsAPackageThatLeavesATmuxServer(t *testing.T) {
	t.Parallel()
	left := startTmuxOwner(t, ownerLeaves)
	if left.server.PID == 0 {
		t.Fatal("the owner's tmux server was gone before its check ran")
	}
	<-left.done
	if code := left.cmd.ProcessState.ExitCode(); code != exitLeft {
		t.Fatalf("the owner that left a tmux server exited %d, want %d from a failed check: %s", code, exitLeft, left.stderr)
	}
	if want := fmt.Sprintf("FAIL: process %d (", left.server.PID); !strings.Contains(left.stderr.String(), want) {
		t.Errorf("the check's report does not name the tmux server with %q:\n%s", want, left.stderr)
	}
	if presence, err := left.server.Presence(); err != nil || presence == agents.PresencePresent {
		t.Errorf("the tmux server reads %v (%v) once the check returned; want it ended by the check", presence, err)
	}
	// The owner is this binary, so its check lists this binary's processes,
	// and this test binary's own sweep is one; it is not one the owner's
	// tests left, and ending it would leave this binary with nothing to end
	// what its tests leave if it is killed.
	if _, sweeper := owned(); sweeper.PID == 0 {
		t.Fatal("this test binary records no sweep of its own")
	} else if presence, err := sweeper.Presence(); err != nil || presence != agents.PresencePresent {
		t.Errorf("this test binary's own sweep %v reads %v (%v) once the owner's check returned; want it left running", sweeper, presence, err)
	}

	clean := startTmuxOwner(t, ownerCleans)
	<-clean.done
	if code := clean.cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("the owner that ended its tmux server exited %d, want 0 from a passed check: %s", code, clean.stderr)
	}
}

// A package whose check passes leaves neither its owner sweep nor its tmux
// wrapper's directory behind once its binary has exited (P15): RunOwned ends
// the sweep and removes the wrapper's directory before it returns, so both
// are already gone when the owner's exit is observed, with no wait. Before #454 the sweep outlived
// every passing package by up to OwnerPoll and then removed the wrapper
// itself, so a check of what go test leaves behind saw the last package's
// sweep and wrapper whenever it looked in time. The control is the owner
// that leaves a server, whose sweep stays to end what the check's own
// ending can start, as TestRunOwnedFailsAPackageThatLeavesATmuxServer
// reads of this binary's.
func TestAPassedCheckLeavesNoSweepOrWrapperBehind(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	clean := startTmuxOwnerCopy(t, ownerCleans)
	if clean.wrapper == "" {
		t.Fatal("the owner recorded no tmux wrapper")
	}
	<-clean.done
	if code := clean.cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("the owner that ended its tmux server exited %d, want 0 from a passed check: %s", code, clean.stderr)
	}
	if !(Process{Identity: clean.sweep}).GoneWithin(0) {
		t.Errorf("the owner's sweep %v was still running when the owner whose check passed had exited", clean.sweep)
	}
	if _, err := os.Stat(filepath.Dir(clean.wrapper)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the owner's tmux wrapper directory %s reads %v when the owner whose check passed had exited; want it removed", filepath.Dir(clean.wrapper), err)
	}

	left := startTmuxOwnerCopy(t, ownerLeaves)
	<-left.done
	if code := left.cmd.ProcessState.ExitCode(); code != exitLeft {
		t.Fatalf("the owner that left a tmux server exited %d, want %d from a failed check: %s", code, exitLeft, left.stderr)
	}
	if presence, err := left.sweep.Presence(); err != nil || presence != agents.PresencePresent {
		t.Errorf("the sweep %v of the owner whose check failed reads %v (%v) when the owner had exited; want it still running to end what the check's ending started", left.sweep, presence, err)
	}
}

// A process a test leaves that exits on its own while the check waits on it,
// and as it exits has a shell's EXIT trap start another process carrying
// the owner identity, fails the package and leaves nothing running (P15):
// the check lists again once what it listed is gone, so it finds the trap's
// process, names it and ends it, and the sweep stays, as for any failed
// check. Before the check listed again, the first listing's process exiting
// in time passed the check, RunOwned retired the sweep, and the trap's
// process, started after the only listing, outlived the binary with nothing
// left to end it. The owner is a copy, so its check lists the processes of
// that copy alone.
func TestRunOwnedFailsAPackageWhoseExitingProcessStartsAnother(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	o := startTmuxOwnerCopy(t, ownerTraps)
	var child agents.Identity
	t.Cleanup(func() {
		if child.PID != 0 {
			if presence, err := child.Presence(); err == nil && presence == agents.PresencePresent {
				_ = syscall.Kill(child.PID, syscall.SIGKILL)
			}
		}
	})
	<-o.done
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(o.record), trapChildRecord))
	if err != nil {
		t.Fatalf("the trap recorded no process (%v); the owner said: %s", err, o.stderr)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("the trap recorded %q", raw)
	}
	// Read as soon as the owner is gone, so a process still running is one
	// the owner's check left, and its start time keeps a reused number from
	// being signalled in cleanup.
	child, found, err := agents.Identify(pid)
	if err != nil {
		t.Fatalf("reading the identity of the trap's process %d: %v", pid, err)
	}
	if !found {
		child = agents.Identity{}
	}
	if found {
		t.Errorf("the trap's process %d was still running when its owner had exited %d; want it ended by the owner's check",
			pid, o.cmd.ProcessState.ExitCode())
	}
	if code := o.cmd.ProcessState.ExitCode(); code != exitLeft {
		t.Errorf("the owner whose test left a process that started another as it exited exited %d, want %d from a failed check: %s",
			code, exitLeft, o.stderr)
	}
	if want := fmt.Sprintf("FAIL: process %d (", pid); !strings.Contains(o.stderr.String(), want) {
		t.Errorf("the check's report does not name the trap's process with %q:\n%s", want, o.stderr)
	}
}

// A chain of processes, each started by the previous one's EXIT trap as it
// exits, fails the package once the check has listed sweepRounds times and
// still finds one, rather than the check following it for as long as it
// goes on (P15): the check names the last one, the process still being
// started, and ends it, so the chain starts nothing more. The owner leaves
// the first of sweepRounds such processes, so its check's last listing finds
// the last of them.
func TestRunOwnedFailsAPackageWhoseProcessesKeepStartingOthers(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	o := startTmuxOwnerCopy(t, ownerChains)
	<-o.done
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(o.record), trapChildRecord))
	if err != nil {
		t.Fatalf("the chain recorded no process (%v); the owner said: %s", err, o.stderr)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("the chain recorded %q", raw)
	}
	if id, found, err := agents.Identify(pid); err != nil {
		t.Fatalf("reading the identity of the chain's last process %d: %v", pid, err)
	} else if found {
		_ = syscall.Kill(id.PID, syscall.SIGKILL)
		t.Errorf("the chain's last process %d was still running when its owner had exited; want it ended by the owner's check", pid)
	}
	if code := o.cmd.ProcessState.ExitCode(); code != exitLeft {
		t.Errorf("the owner whose processes kept starting others exited %d, want %d from a failed check: %s", code, exitLeft, o.stderr)
	}
	want := fmt.Sprintf("FAIL: process %d (", pid)
	still := fmt.Sprintf("still being started %d listings after", sweepRounds)
	if !strings.Contains(o.stderr.String(), want) || !strings.Contains(o.stderr.String(), still) {
		t.Errorf("the check's report does not name the chain's last process with %q and %q:\n%s", want, still, o.stderr)
	}
}

// Once RunOwned has returned, the binary that ran it starts no process, so
// nothing it starts after its check's last listing can outlive it unseen
// once that check retired its sweep (P15): a goroutine a test left running
// would start one there. The owner starts a process of its binary once
// RunOwned has returned, after its check passed, and the start never
// returns before the owner exits, so nothing is started. The control is the
// same start inside RunOwned's run, which returns within the same bound in
// the same owner, or the owner exits exitBroken.
func TestNothingStartsOnceRunOwnedHasReturned(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	o := startTmuxOwnerCopy(t, ownerLate)
	<-o.done
	if raw, err := os.ReadFile(filepath.Join(filepath.Dir(o.record), lateChildRecord)); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			if id, found, err := agents.Identify(pid); err == nil && found {
				t.Cleanup(func() {
					if presence, err := id.Presence(); err == nil && presence == agents.PresencePresent {
						_ = syscall.Kill(id.PID, syscall.SIGKILL)
					}
				})
			}
		}
		t.Errorf("the owner started process %s after RunOwned returned; want the start held back until the owner exited", raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reading what the owner started after RunOwned returned: %v", err)
	}
	if code := o.cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("the owner exited %d, want 0 from a passed check and a control start that returned: %s", code, o.stderr)
	}
}

// Tmux refuses a test that does not run inside RunOwned, in a binary that
// recorded no ownership, so a package cannot start terminal servers with
// nothing to end the ones its tests leave; the refusal names the call to
// add. The control is every other test in this package, which calls Tmux
// inside RunOwned.
func TestTmuxRefusesABinaryThatDoesNotOwnWhatItStarts(t *testing.T) {
	t.Parallel()
	o := startTmuxOwner(t, ownerUnowned)
	<-o.done
	if code := o.cmd.ProcessState.ExitCode(); code != exitRefused {
		t.Fatalf("the unowned binary exited %d, want %d from Tmux's refusal: %s", code, exitRefused, o.stderr)
	}
	if !strings.Contains(o.stderr.String(), "standin.RunOwned(") {
		t.Errorf("the refusal does not name the call to add:\n%s", o.stderr)
	}
}

// Tmux refuses a test in a binary that recorded itself as the owner of what
// its tests start and did not wire the check that runs after them, so a
// TestMain cannot start the sweep and leave a server its tests leave running
// unreported, which #423's first form of the guard let through. The control
// is every other test in this package, which calls Tmux inside RunOwned.
func TestTmuxRefusesABinaryThatDoesNotRunTheCheckAfterItsTests(t *testing.T) {
	t.Parallel()
	o := startTmuxOwner(t, ownerUnchecked)
	<-o.done
	if code := o.cmd.ProcessState.ExitCode(); code != exitRefused {
		t.Fatalf("the binary that skipped the check exited %d, want %d from Tmux's refusal: %s", code, exitRefused, o.stderr)
	}
	if !strings.Contains(o.stderr.String(), "standin.RunOwned(") {
		t.Errorf("the refusal does not name the call to add:\n%s", o.stderr)
	}
}
