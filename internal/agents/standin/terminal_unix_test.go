//go:build linux || darwin

package standin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// The terminal AtTerminal gives a test binary is read by the real reader as
// the thing the gate's placement looks for: a child started as the session
// leader of the pair's other side reads a controlling terminal whose
// foreground group is its own, and the same child started in a session of
// its own with no terminal reads neither, which is the control that shows
// the reading can come out the other way. What is asserted is the kernel's
// record of the child and never anything the child or this test states.
func TestTheTerminalPutsItsSessionLeaderInTheForeground(t *testing.T) {
	master, slave, err := openTerminal()
	if err != nil {
		t.Fatalf("openTerminal: %v", err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()

	at := exec.Command("sleep", "30")
	at.ExtraFiles = []*os.File{slave}
	at.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 3}
	if err := at.Start(); err != nil {
		t.Fatalf("starting a child at the terminal: %v", err)
	}
	defer func() { _ = at.Process.Kill(); _ = at.Wait() }()
	lineage, found, err := agents.ReadLineage(at.Process.Pid)
	if err != nil || !found {
		t.Fatalf("ReadLineage(the child at the terminal) = %+v, %v, %v", lineage, found, err)
	}
	if !lineage.Foreground() || lineage.Group != at.Process.Pid {
		t.Fatalf("the child at the terminal reads %+v; want it leading its group in the foreground of a terminal", lineage)
	}

	off := exec.Command("sleep", "30")
	off.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := off.Start(); err != nil {
		t.Fatalf("starting a child off any terminal: %v", err)
	}
	defer func() { _ = off.Process.Kill(); _ = off.Wait() }()
	lineage, found, err = agents.ReadLineage(off.Process.Pid)
	if err != nil || !found {
		t.Fatalf("ReadLineage(the child off any terminal) = %+v, %v, %v", lineage, found, err)
	}
	if lineage.Foreground() || lineage.TerminalGroup != 0 {
		t.Fatalf("the child in a session of its own reads %+v; want no terminal, so the reading above was the terminal's doing", lineage)
	}
}

// atTerminalVarHelper names the directory a test hands this binary when it
// starts it as a test binary calling AtTerminal, and atTerminalPIDs the file
// the binary run again writes there: its own process identifier and that of
// a child it started in its group, the shape of a stand-in or a git a test
// leaves running.
const (
	atTerminalVarHelper = "STANDIN_TEST_AT_TERMINAL_DIR"
	atTerminalPIDs      = "pids"
)

// atTerminalMain makes this binary what a package calling AtTerminal is,
// when a test started it as one: it calls AtTerminal, and in the binary run
// again starts a child in its own group, records both, and waits forever,
// so nothing but its owner watch can end it. It returns otherwise.
func atTerminalMain() {
	dir := os.Getenv(atTerminalVarHelper)
	if dir == "" {
		return
	}
	AtTerminal()
	child := exec.Command("/bin/sleep", "3600")
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "starting the child of the binary run again:", err)
		os.Exit(1)
	}
	record := fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)
	if err := os.WriteFile(filepath.Join(dir, atTerminalPIDs), []byte(record), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "recording the binary run again:", err)
		os.Exit(1)
	}
	// A sleep rather than an empty select, which the runtime would end as
	// a deadlock once no watch goroutine is left to run.
	for {
		time.Sleep(time.Hour)
	}
}

// The test binary AtTerminal runs again ends with the outer one when the
// outer is killed outright, which is #414: an outer started the way nohup
// starts a command, with the hangup signal ignored and so inherited ignored
// by the binary run again, is put in a session of its own with no terminal,
// so AtTerminal runs it again on a pseudo-terminal. The binary run again is
// read from the kernel as the outer's child, at the terminal, ignoring the
// hangup signal, which is the control that the terminal's hangup is not what
// ends it below; it stays running while the outer runs, and the package's
// own check (LeftAfterTests, through Stayed) lists it then, which is the
// check shown to see it. Then the outer is sent SIGKILL, and the binary run
// again and the child it started in its group are both read as gone within
// OwnerPoll and a margin. Where the binary run again outlives that, the test
// fails and leaves it running for LeftAfterTests, which then fails the
// package naming it.
func TestTheBinaryRunAgainEndsWhenItsOuterIsKilled(t *testing.T) {
	nohup, err := exec.LookPath("nohup")
	if err != nil {
		t.Fatalf("nohup, which starts the outer the way #414's leak was started, is not on PATH: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	outer := exec.Command(nohup, self, "-test.run=^$")
	outer.Env = append(os.Environ(), atTerminalVarHelper+"="+dir)
	outer.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := outer.Start(); err != nil {
		t.Fatalf("starting the outer: %v", err)
	}
	outerDone := make(chan struct{})
	go func() { _ = outer.Wait(); close(outerDone) }()
	t.Cleanup(func() {
		select {
		case <-outerDone:
		default:
			_ = syscall.Kill(-outer.Process.Pid, syscall.SIGKILL)
			<-outerDone
		}
	})

	var inner, child agents.Identity
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(filepath.Join(dir, atTerminalPIDs))
		if err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) != 2 {
				t.Fatalf("the binary run again recorded %q", raw)
			}
			inner = identify(t, fields[0])
			child = identify(t, fields[1])
			break
		}
		select {
		case <-outerDone:
			t.Fatal("the outer exited before the binary run again recorded itself")
		default:
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the binary run again never recorded itself")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The child is not this binary, so nothing but this cleanup ends it
	// where the binary run again outlives the test.
	t.Cleanup(func() {
		if presence, err := child.Presence(); err == nil && presence == agents.PresencePresent {
			_ = syscall.Kill(child.PID, syscall.SIGKILL)
		}
	})

	lineage, found, err := agents.ReadLineage(inner.PID)
	if err != nil || !found {
		t.Fatalf("ReadLineage(the binary run again) = %+v, %v, %v", lineage, found, err)
	}
	if lineage.Parent != outer.Process.Pid || !lineage.Foreground() || lineage.Group != inner.PID {
		t.Fatalf("the binary run again reads %+v; want the outer %d's child leading its group in the foreground of a terminal",
			lineage, outer.Process.Pid)
	}
	ignored, err := ignoresHangup(inner.PID)
	if err != nil {
		t.Fatalf("reading whether the binary run again ignores the hangup signal: %v", err)
	}
	if !ignored {
		t.Fatal("the binary run again does not ignore the hangup signal, so the terminal's hangup alone would end it and this test would show nothing about the owner watch")
	}

	select {
	case <-outerDone:
		t.Fatal("the outer exited while the binary run again was running")
	case <-time.After(4 * OwnerPoll):
	}
	if presence, err := inner.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the binary run again reads %v (%v) while its outer runs; want it running", presence, err)
	}
	listed, err := Stayed(self, func(p Process) bool { return p.Identity == inner }, 0)
	if err != nil {
		t.Fatalf("listing what this binary runs: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("the package's check lists %v for the running binary run again %v; want it, or the check could not see one that outlived its outer", listed, inner)
	}

	_ = syscall.Kill(outer.Process.Pid, syscall.SIGKILL)
	<-outerDone
	bound := OwnerPoll + 5*time.Second
	for _, p := range []struct {
		name string
		id   agents.Identity
	}{{"the binary run again", inner}, {"the child it started in its group", child}} {
		if !(Process{Identity: p.id}).GoneWithin(bound) {
			t.Errorf("%s, %v, was still running %v after its outer was killed", p.name, p.id, bound)
		}
	}
}

// identify reads the identity of the process numbered by field.
func identify(t *testing.T, field string) agents.Identity {
	t.Helper()
	pid, err := strconv.Atoi(field)
	if err != nil {
		t.Fatalf("%q is not a process identifier", field)
	}
	id, found, err := agents.Identify(pid)
	if err != nil || !found {
		t.Fatalf("reading the identity of process %d (found %v): %v", pid, found, err)
	}
	return id
}

// An owner identity is spelled one way and read back exactly, and a value
// that is not one is refused rather than read as some process.
func TestAnOwnerIdentityReadsBackAndAnythingElseIsRefused(t *testing.T) {
	id := agents.Identity{PID: 4242, Start: 1792519140437}
	got, err := parseOwner(encodeOwner(id))
	if err != nil || got != id {
		t.Fatalf("parseOwner(encodeOwner(%v)) = %v, %v", id, got, err)
	}
	for _, value := range []string{"", "1", "1:", ":1", "x:1", "1:x", "0:1", "-3:1", "1:2:3"} {
		if got, err := parseOwner(value); err == nil {
			t.Errorf("parseOwner(%q) = %v, want a refusal", value, got)
		}
	}
}
