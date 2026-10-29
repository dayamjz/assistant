//go:build linux || darwin

package standin

import (
	"errors"
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
	"github.com/dayamjz/assistant/internal/principles"
)

// trapShell is a shell that, like a link of trapExitMain's chain, clears
// its EXIT trap on SIGTERM and otherwise runs it as it exits, waiting on a
// foreground child: the trap writes record rather than starting a process,
// so whether it ran is read from the file.
type trapShell struct {
	cmd    *exec.Cmd
	shell  Process
	child  Process
	record string
}

// startTrapShell starts a trapShell and returns once its child is running
// and both identities are read.
func startTrapShell(t *testing.T) trapShell {
	t.Helper()
	record := filepath.Join(t.TempDir(), "exit-trap-ran")
	cmd := exec.Command("/bin/sh", "-c",
		`trap 'trap - EXIT; exit 143' TERM; trap 'echo ran >"$0"' EXIT; sleep 3600`, record)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := trapShell{cmd: cmd, record: record}
	t.Cleanup(func() {
		for _, p := range []Process{s.child, s.shell} {
			if p.PID != 0 {
				if presence, err := p.Presence(); err == nil && presence == agents.PresencePresent {
					_ = syscall.Kill(p.PID, syscall.SIGKILL)
				}
			}
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	id, found, err := agents.Identify(cmd.Process.Pid)
	if err != nil || !found {
		t.Fatalf("reading the identity of shell %d: found %v, %v", cmd.Process.Pid, found, err)
	}
	s.shell = Process{Identity: id}
	deadline := time.Now().Add(10 * time.Second)
	for {
		sleeps, err := ProcessesMatching(func(args []string) bool {
			return slices.Equal(args, []string{"sleep", "3600"})
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range sleeps {
			if lineage, found, err := agents.ReadLineage(p.PID); err == nil && found && lineage.Identity == p.Identity && lineage.Parent == cmd.Process.Pid {
				s.child = p
				return s
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("shell %d started no sleep within 10s", cmd.Process.Pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// trapRan reports whether the shell's EXIT trap has written its record.
func (s trapShell) trapRan(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(s.record)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// awaitActed returns once the shell has run its EXIT trap or is no longer
// running, and fails the test when neither is read within 10s. It is how a
// test holds a sender for as long as the shell needs to act on what the
// sender just did, which a scheduler can do to a sender it deschedules
// there.
func (s trapShell) awaitActed(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if s.trapRan(t) {
			return
		}
		presence, err := s.shell.Presence()
		if err == nil && presence != agents.PresencePresent {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("shell %v neither ran its EXIT trap nor exited within 10s of its child's SIGTERM (%v, %v)", s.shell, presence, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A process ended as one set with a child of its own has been sent its own
// SIGTERM before it can act on that child's ending (P15, #558): a shell
// whose EXIT trap would start a process no listing has seen, waiting on a
// foreground child, is ended with that child by endSet, given the child
// first, and the sender is held after the child's SIGTERM until the shell
// has acted, as a descheduled sender can be. The trap never runs, the shell
// exits on its TERM trap's status, and both are gone. Each sent SIGTERM
// from a goroutine of its own, the child's ending could run the shell on to
// its trap before the shell's own signal, which is how a check ending the
// last of a chain started the next process itself; signalled in the order
// given, the held sender makes that certain. The control is the same shell
// with its child alone sent SIGTERM: the trap runs and writes its record, so
// the record this test reads as absent is one the trap writes.
func TestEndingASetLetsNoneActOnAnothersEnding(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)

	control := startTrapShell(t)
	if !control.child.signal(syscall.SIGTERM) {
		t.Fatalf("the control's child %v was not running to be sent SIGTERM", control.child)
	}
	_ = control.cmd.Wait()
	if !control.trapRan(t) {
		t.Fatalf("the control shell exited %v without running its EXIT trap when its child alone was ended; this test's record reads nothing", control.cmd.ProcessState)
	}

	s := startTrapShell(t)
	endSet([]Process{s.child, s.shell}, func(p Process, sig syscall.Signal) bool {
		sent := p.signal(sig)
		if sent && sig == syscall.SIGTERM && p.Identity == s.child.Identity {
			s.awaitActed(t)
		}
		return sent
	})
	_ = s.cmd.Wait()
	if s.trapRan(t) {
		t.Errorf("the shell ran its EXIT trap when it was ended as one set with its child; want it sent its own SIGTERM first")
	}
	if code := s.cmd.ProcessState.ExitCode(); code != 143 {
		t.Errorf("the shell ended as one set exited %v, want 143 from its TERM trap", s.cmd.ProcessState)
	}
	if !s.child.GoneWithin(EndGrace) {
		t.Errorf("the child %v ended as one set with its shell was still running %v after endSet returned", s.child, EndGrace)
	}
}

// heldChain is a shell, the process a listing finds, whose child shell no
// listing is given waits on a held process and, once that is released,
// starts a second held process that records its identity, as a shell's
// trap starting a process on its own timer does (#645); an orphaning chain's
// child starts it in the background and exits, so its parent is gone
// (#712's review).
type heldChain struct {
	dir     string
	cmd     *exec.Cmd
	shell   Process
	child   agents.Identity
	held    agents.Identity
	release string
	started string
}

// startHeldChain starts a heldChain, an orphaning one when orphans is set,
// and returns once the first held process is running and its identity and
// its parent's are read.
func startHeldChain(t *testing.T, orphans bool) heldChain {
	t.Helper()
	dir := t.TempDir()
	c := heldChain{dir: dir, release: filepath.Join(dir, "release"), started: filepath.Join(dir, "started")}
	heldID := filepath.Join(dir, "held")
	second := HeldWaitIdentified(t, filepath.Join(dir, "never"), c.started)
	if orphans {
		second = "{ " + second + " & }"
	}
	child := HeldWaitIdentified(t, c.release, heldID) + " && " + second
	c.cmd = exec.Command("/bin/sh", "-c", `/bin/sh -c "$CHILD" & wait`)
	c.cmd.Env = append(os.Environ(), "CHILD="+child)
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ids := []agents.Identity{c.shell.Identity, c.child, c.held}
		if started, err := ReadHeldIdentity(c.started); err == nil {
			ids = append(ids, started)
		}
		for _, id := range ids {
			if presence, err := id.Presence(); err == nil && presence == agents.PresencePresent {
				_ = syscall.Kill(id.PID, syscall.SIGKILL)
			}
		}
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	})
	id, found, err := agents.Identify(c.cmd.Process.Pid)
	if err != nil || !found {
		t.Fatalf("reading the identity of shell %d: found %v, %v", c.cmd.Process.Pid, found, err)
	}
	c.shell = Process{Identity: id}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if held, err := ReadHeldIdentity(heldID); err == nil {
			c.held = held
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("shell %d started no held process within 10s", c.cmd.Process.Pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
	lineage, found, err := agents.ReadLineage(c.held.PID)
	if err != nil || !found || lineage.Identity != c.held {
		t.Fatalf("reading the parent of held process %v: found %v, %v", c.held, found, err)
	}
	parent, found, err := agents.Identify(lineage.Parent)
	if err != nil || !found {
		t.Fatalf("reading the identity of the held process's shell %d: found %v, %v", lineage.Parent, found, err)
	}
	c.child = parent
	return c
}

// list is the chain's processes as a listing reads them: every process
// whose arguments name the chain's directory, which its held processes'
// do, so a listing in a test finds them and no other test's.
func (c heldChain) list() ([]Process, error) {
	return ProcessesMatching(func(args []string) bool {
		return slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, c.dir) })
	})
}

// startedWithin reports whether the chain's second held process recorded
// its identity within wait.
func (c heldChain) startedWithin(wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if _, err := os.Stat(c.started); err == nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A set the check ends once processes were still being started is stopped
// whole, a child no listing found among it, before any of it is named or
// ended, so nothing in it starts a process once named (P15, #645): a shell
// is given to endStarting alone, while its child shell waits on a held
// process to start another. The set handed to the report holds the shell,
// its child and the held process, each reads as stopped there, and the
// release made then starts nothing before the set is killed; all three are
// gone once endStarting returns. The control is the same chain ended with
// endAll, which sends the listed shell alone SIGTERM: the same release then
// has the child start the second process, as the check's SIGTERM once let
// a chain's unseen shell start one.
func TestEndingWhatKeepsStartingStopsItFirst(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)

	control := startHeldChain(t, false)
	endAll([]Process{control.shell})
	if err := os.WriteFile(control.release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !control.startedWithin(EndGrace) {
		t.Fatalf("the control's child started nothing within %v of its release once its shell alone was ended; this test's record reads nothing", EndGrace)
	}

	c := startHeldChain(t, false)
	var named []agents.Identity
	err := endStarting([]Process{c.shell}, c.list, nil, func(frozen []Process) {
		for _, p := range frozen {
			named = append(named, p.Identity)
			if stopped, err := stoppedProcess(p.PID); err != nil || !stopped {
				t.Errorf("%v was handed to the report reading stopped %v (%v); want every process of the set stopped first", p, stopped, err)
			}
		}
		if err := os.WriteFile(c.release, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []agents.Identity{c.shell.Identity, c.child, c.held} {
		if !slices.Contains(named, want) {
			t.Errorf("the set handed to the report, %v, does not hold %v; want the shell and everything descended from it", named, want)
		}
		if presence, err := want.Presence(); err != nil || presence == agents.PresencePresent {
			t.Errorf("%v reads %v (%v) once endStarting returned; want it ended", want, presence, err)
		}
	}
	if _, err := os.Stat(c.started); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the chain's child started its second process (%v) though its set was stopped before the release", err)
	}
	_ = c.cmd.Wait()
}

// A process an unseen child starts as it exits, while the check is held
// after stopping that child's parent and before reading its children, is
// stopped, named and ended all the same (P15, #712's review): its parent
// gone, it descends from nothing in the set, so endStarting lists again
// once the tree is stopped and stops what that listing finds. The shell is
// given to endStarting alone; once it reads stopped, its child shell is
// released, starts the second held process in the background and exits,
// before the shell's children are read. The second process is in the set
// handed to the report, and gone once endStarting returns. The control is
// the same chain whose listing finds nothing: the second process is then
// left running, which shows the listing is what finds it.
func TestEndingWhatKeepsStartingFindsWhatAnExitingChildStarted(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	run := func(c heldChain, list func() ([]Process, error)) (agents.Identity, []agents.Identity) {
		t.Helper()
		var second agents.Identity
		var named []agents.Identity
		held := false
		err := endStarting([]Process{c.shell}, list, func(batch []Process) {
			if held || !slices.ContainsFunc(batch, func(p Process) bool { return p.Identity == c.shell.Identity }) {
				return
			}
			held = true
			if err := os.WriteFile(c.release, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if !c.startedWithin(EndGrace) {
				t.Fatalf("the chain's child started nothing within %v of its release", EndGrace)
			}
			id, err := ReadHeldIdentity(c.started)
			if err != nil {
				t.Fatal(err)
			}
			second = id
			if !(Process{Identity: c.child}).GoneWithin(EndGrace) {
				t.Fatalf("the chain's child %v had not exited %v after starting its second process", c.child, EndGrace)
			}
		}, func(frozen []Process) {
			for _, p := range frozen {
				named = append(named, p.Identity)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		return second, named
	}

	control := startHeldChain(t, true)
	second, named := run(control, func() ([]Process, error) { return nil, nil })
	if slices.Contains(named, second) {
		t.Fatalf("the control named its second process %v with a listing that finds nothing; this test's listing reads nothing", second)
	}
	if presence, err := second.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the control's second process %v reads %v (%v); want it left running when nothing lists it", second, presence, err)
	}

	c := startHeldChain(t, true)
	second, named = run(c, c.list)
	if !slices.Contains(named, second) {
		t.Errorf("the set handed to the report, %v, does not hold %v, which the chain's child started as it exited", named, second)
	}
	if presence, err := second.Presence(); err != nil || presence == agents.PresencePresent {
		t.Errorf("%v reads %v (%v) once endStarting returned; want it ended", second, presence, err)
	}
}

// TestTestListArgvMatchesGoParsing is the gate for RunOwned's list-only skip
// matching Go's flag parser rather than a hand scan of os.Args (#727 round 3).
// A scan that trusted the first -test.list= token skipped the leftover check
// on -test.list=^Test -test.list= and on -- -test.list=^Test while tests still
// ran, leaving a child alive (P15). Nested RunOwned deadlocks on ForkLock, so
// each row re-execs this package binary the way go test starts it.
//
// Non-list rows plant a child from the probe body (STANDIN_LIST_ARGV_PROBE)
// and require CHILD=<pid> plus the leftover check's FAIL naming it: that is
// the positive control that the check stays on when tests still run.
//
// List-only rows plant a same-binary sibling in TestMain before RunOwned
// (STANDIN_LIST_SKIP_PROBE). Go does not run bodies under a non-empty
// -test.list, so a body probe cannot observe the skip. Exit 0 with the
// listed name, CHILD=<pid> printed, and no FAIL naming that child proves
// the check was skipped: without the skip, leftAfterTests would name the
// planted sibling and fail the package. An empty successful check is silent,
// so exit 0 alone is not evidence.
func TestTestListArgvMatchesGoParsing(t *testing.T) {
	principles.Cite(t, principles.P15)
	if os.Getenv("STANDIN_LIST_ARGV_PROBE") == "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "STANDIN_TEST_HOLD_OPEN=1")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(os.Stderr, "CHILD=%d\n", cmd.Process.Pid)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		args     []string
		listOnly bool
	}{
		{
			name: "last empty wins",
			args: []string{
				"-test.run=^TestTestListArgvMatchesGoParsing$",
				"-test.list=^Test",
				"-test.list=",
				"-test.count=1",
				"-test.v=true",
			},
		},
		{
			name: "token after terminator",
			args: []string{
				"-test.run=^TestTestListArgvMatchesGoParsing$",
				"-test.count=1",
				"-test.v=true",
				"--",
				"-test.list=^Test",
			},
		},
		{
			name: "empty list still runs",
			args: []string{
				"-test.run=^TestTestListArgvMatchesGoParsing$",
				"-test.list=",
				"-test.count=1",
				"-test.v=true",
			},
		},
		{
			name:     "equals list",
			args:     []string{"-test.list=^Test"},
			listOnly: true,
		},
		{
			name:     "space list",
			args:     []string{"-test.list", "^Test"},
			listOnly: true,
		},
		{
			name:     "long equals list",
			args:     []string{"--test.list=^Test"},
			listOnly: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(exe, tc.args...)
			if tc.listOnly {
				cmd.Env = append(os.Environ(), "STANDIN_LIST_SKIP_PROBE=1")
			} else {
				cmd.Env = append(os.Environ(), "STANDIN_LIST_ARGV_PROBE=1")
			}
			out, err := cmd.CombinedOutput()
			text := string(out)
			var child int
			for _, line := range strings.Split(text, "\n") {
				if rest, ok := strings.CutPrefix(line, "CHILD="); ok {
					if pid, convErr := strconv.Atoi(rest); convErr == nil {
						child = pid
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			}
			if tc.listOnly {
				// Planted before RunOwned; exit 0 with no FAIL naming it is
				// the positive control that the list-only skip ran.
				if child == 0 {
					t.Fatalf("list-only argv %v: TestMain did not print CHILD=<pid>; plant failed:\n%s", tc.args, text)
				}
				if err != nil {
					t.Fatalf("list-only argv %v: want exit 0 with planted child %d skipped, got %v\n%s", tc.args, child, err, text)
				}
				if !strings.Contains(text, "TestTestListArgvMatchesGoParsing") {
					t.Fatalf("list-only argv %v: want listed test name, got:\n%s", tc.args, text)
				}
				wantFail := fmt.Sprintf("FAIL: process %d (", child)
				if strings.Contains(text, wantFail) {
					t.Fatalf("list-only argv %v: leftover check named planted child %d; skip did not run:\n%s", tc.args, child, text)
				}
				return
			}
			// A probe that fails during setup (nonzero exit, no child) must
			// not pass: only CHILD=<pid> plus the leftover check's own FAIL
			// for that pid proves the check ran (#727 round 4).
			if child == 0 {
				t.Fatalf("argv %v: probe did not print CHILD=<pid>; setup failed before planting:\n%s", tc.args, text)
			}
			wantFail := fmt.Sprintf("FAIL: process %d (", child)
			if !strings.Contains(text, wantFail) || !strings.Contains(text, "was started by this package's tests and was still running") {
				t.Fatalf("argv %v: want leftover check naming planted child %d, got err=%v:\n%s", tc.args, child, err, text)
			}
			if err == nil {
				t.Fatalf("argv %v exited 0 after leaving child %d; want the leftover check to fail:\n%s", tc.args, child, text)
			}
		})
	}
}
