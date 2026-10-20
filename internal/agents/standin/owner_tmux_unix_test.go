//go:build linux || darwin

package standin

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// The variables a test hands this binary when it starts it as a test binary
// that owns a tmux server (tmuxOwnerMain): the directory it records itself
// in, the tmux it starts, the socket it starts it on, and what it does then.
const (
	tmuxOwnerDirVar    = "STANDIN_TEST_TMUX_OWNER_DIR"
	tmuxOwnerTmuxVar   = "STANDIN_TEST_TMUX_OWNER_TMUX"
	tmuxOwnerSocketVar = "STANDIN_TEST_TMUX_OWNER_SOCKET"
	tmuxOwnerModeVar   = "STANDIN_TEST_TMUX_OWNER_MODE"
	tmuxOwnerRecord    = "record"
	// tmuxPaneArg makes this binary the process in the server's pane, which
	// waits forever on its terminal as a stand-in worker does. It is an
	// argument rather than a variable, because the pane inherits the owner's
	// variables through the server.
	tmuxPaneArg = "-standin-test-tmux-pane"
)

// The modes of tmuxOwnerMain. ownerWaits leaves the server running and
// waits forever, so nothing but the owner sweep can end the server once the
// owner is killed; ownerLeaves leaves it running and runs LeftAfterTests, the
// way a package's TestMain does once its tests return; ownerCleans ends it
// first and then runs LeftAfterTests, the control that the check passes when
// nothing is left; ownerUnowned skips OwnProcesses and asks Tmux's refusal.
const (
	ownerWaits   = "wait"
	ownerLeaves  = "leave"
	ownerCleans  = "clean"
	ownerUnowned = "unowned"
)

// exitLeft is the status tmuxOwnerMain exits with when LeftAfterTests
// reported false, and exitRefused the one when Tmux's refusal fired.
const (
	exitLeft    = 3
	exitRefused = 4
)

// tmuxOwnerMain makes this binary a test binary whose test started a tmux
// server and did not end it, when a test started it as one, and returns
// otherwise. It records its owner identity, the server's and its pane's
// process identifiers and its sweep's, and then does what its mode says.
func tmuxOwnerMain() {
	if slices.Contains(os.Args[1:], tmuxPaneArg) {
		for {
			time.Sleep(time.Hour)
		}
	}
	dir := os.Getenv(tmuxOwnerDirVar)
	if dir == "" {
		return
	}
	mode := os.Getenv(tmuxOwnerModeVar)
	if mode == ownerUnowned {
		refused := &refusalTB{}
		Tmux(refused)
		if refused.message != "" {
			fmt.Fprint(os.Stderr, refused.message)
			os.Exit(exitRefused)
		}
		os.Exit(0)
	}
	fail := func(what string, err error) {
		fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
		os.Exit(1)
	}
	if err := OwnProcesses(); err != nil {
		fail("owning what this binary starts", err)
	}
	self, err := os.Executable()
	if err != nil {
		fail("locating this binary", err)
	}
	tmux, socket := os.Getenv(tmuxOwnerTmuxVar), os.Getenv(tmuxOwnerSocketVar)
	if out, err := exec.Command(tmux, "-S", socket, "-f", os.DevNull, "new-session", "-d", "-s", "owned",
		"-x", "80", "-y", "24", "--", self, "-test.run=^$", tmuxPaneArg).CombinedOutput(); err != nil {
		fail("starting a tmux server", fmt.Errorf("%w: %s", err, out))
	}
	pids, err := exec.Command(tmux, "-S", socket, "display-message", "-p", "#{pid} #{pane_pid}").Output()
	if err != nil {
		fail("reading the tmux server's process identifiers", err)
	}
	value, sweeper := owned()
	record := fmt.Sprintf("%s %s %d", value, strings.TrimSpace(string(pids)), sweeper.PID)
	if err := os.WriteFile(filepath.Join(dir, tmuxOwnerRecord), []byte(record), 0o600); err != nil {
		fail("recording the tmux server", err)
	}
	switch mode {
	case ownerWaits:
		for {
			time.Sleep(time.Hour)
		}
	case ownerCleans:
		if err := exec.Command(tmux, "-S", socket, "kill-server").Run(); err != nil {
			fail("ending the tmux server", err)
		}
	}
	if !LeftAfterTests() {
		os.Exit(exitLeft)
	}
	os.Exit(0)
}

// refusalTB is a testing.TB that records the message of a Fatalf, which is
// how tmuxOwnerMain reads Tmux's refusal outside a test. Its other methods
// are the nil TB's and panic, so a Tmux that reached for any other would
// fail the test rather than pass it.
type refusalTB struct {
	testing.TB
	message string
}

func (r *refusalTB) Helper() {}

func (r *refusalTB) Fatalf(format string, args ...any) { r.message = fmt.Sprintf(format, args...) }

// withOwner is env with this process's owner identity added, for a test
// that starts a tmux server with an environment it builds from nothing,
// which would otherwise carry no identity for the sweep and the check to
// find the server by.
func withOwner(env []string) []string {
	return append(env, OwnerVar+"="+os.Getenv(OwnerVar))
}

// tmuxOwned is what a test binary started by startTmuxOwner recorded: the
// owner identity its processes carry, its tmux server and the server's pane,
// and its owner sweep.
type tmuxOwned struct {
	cmd                  *exec.Cmd
	done                 chan struct{}
	stderr               *bytes.Buffer
	value                string
	server, pane, sweep  agents.Identity
	tmux, socket, record string
}

// startTmuxOwner starts this binary as a test binary owning a tmux server in
// mode, in a session of its own so that a kill of this test's group does not
// reach it and the kill below is of it alone, and waits for its record. Every
// process it names is ended in cleanup where it outlives the test.
func startTmuxOwner(t *testing.T, mode string) *tmuxOwned {
	t.Helper()
	o := &tmuxOwned{tmux: Tmux(t), socket: filepath.Join(SocketDir(t), "o"), done: make(chan struct{}), stderr: &bytes.Buffer{}}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	o.record = filepath.Join(dir, tmuxOwnerRecord)
	o.cmd = exec.Command(self, "-test.run=^$")
	o.cmd.Env = append(os.Environ(), tmuxOwnerDirVar+"="+dir, tmuxOwnerTmuxVar+"="+o.tmux,
		tmuxOwnerSocketVar+"="+o.socket, tmuxOwnerModeVar+"="+mode)
	o.cmd.Stderr = o.stderr
	o.cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := o.cmd.Start(); err != nil {
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
		for _, id := range []agents.Identity{o.sweep, o.pane, o.server} {
			if id.PID == 0 {
				continue
			}
			if presence, err := id.Presence(); err == nil && presence == agents.PresencePresent {
				_ = syscall.Kill(id.PID, syscall.SIGKILL)
			}
		}
	})
	if mode == ownerUnowned {
		return o
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(o.record)
		if err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) != 4 {
				t.Fatalf("the owner recorded %q", raw)
			}
			o.value = fields[0]
			// An owner that ends its server ends it before this read can, so
			// a process already gone is a zero identity, which nothing
			// below signals.
			o.server, o.pane, o.sweep = identifyRunning(t, fields[1]), identifyRunning(t, fields[2]), identifyRunning(t, fields[3])
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
// read as gone within OwnerPoll and a margin. The pane runs this binary, as a
// stand-in worker's does, since on macOS the read finds no environment for
// some programs, an Apple-signed /bin/sleep among them.
func TestATmuxServerATestLeavesEndsWhenItsOwnerIsKilled(t *testing.T) {
	t.Parallel()
	o := startTmuxOwner(t, ownerWaits)
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
}

// LeftAfterTests fails a package whose tests leave a tmux server running,
// although the server runs another executable than the package's binary: the
// owner leaves one and runs the check the way its TestMain would, and the
// check reports false, names the server's process on standard error, and has
// ended it by the time the owner exits, which is before the sweep could have
// (it waits OwnerPoll after the owner is gone). The control is the same owner
// ending its server first, which the check passes, so the failure above is
// the server's doing and not the check's.
func TestLeftAfterTestsFailsAPackageThatLeavesATmuxServer(t *testing.T) {
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

	clean := startTmuxOwner(t, ownerCleans)
	<-clean.done
	if code := clean.cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("the owner that ended its tmux server exited %d, want 0 from a passed check: %s", code, clean.stderr)
	}
}

// Tmux refuses a test in a binary that did not call OwnProcesses, so a
// package cannot start terminal servers with nothing to end the ones its
// tests leave; the refusal names the call to add. The control is every other
// test in this package, which calls Tmux in a binary that did.
func TestTmuxRefusesABinaryThatDoesNotOwnWhatItStarts(t *testing.T) {
	t.Parallel()
	o := startTmuxOwner(t, ownerUnowned)
	<-o.done
	if code := o.cmd.ProcessState.ExitCode(); code != exitRefused {
		t.Fatalf("the unowned binary exited %d, want %d from Tmux's refusal: %s", code, exitRefused, o.stderr)
	}
	if !strings.Contains(o.stderr.String(), "standin.OwnProcesses()") {
		t.Errorf("the refusal does not name the call to add:\n%s", o.stderr)
	}
}
