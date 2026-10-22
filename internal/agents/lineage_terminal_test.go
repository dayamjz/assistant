//go:build linux || darwin

package agents_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// ReadLineage reads the foreground process group of a process's controlling
// terminal out of the same entry as its parent and group. For this process
// it agrees with what the terminal itself answers to TIOCGPGRP, or is zero
// when this process has no controlling terminal to ask, so the reading is
// held to the operating system's whichever way the test is run; and a child
// started in a session of its own has no controlling terminal, so it reads
// zero and is not Foreground, which is the shape a host agent's tool gives
// the command it runs.
func TestReadLineageReadsTheTerminalsForegroundGroup(t *testing.T) {
	self, found, err := agents.ReadLineage(os.Getpid())
	if err != nil || !found {
		t.Fatalf("ReadLineage(this process) = %+v, %v, %v", self, found, err)
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		if self.TerminalGroup != 0 || self.Foreground() {
			t.Fatalf("this process has no controlling terminal (%v) and reads terminal group %d, want zero", err, self.TerminalGroup)
		}
	} else {
		defer func() { _ = tty.Close() }()
		group, err := unix.IoctlGetInt(int(tty.Fd()), unix.TIOCGPGRP)
		if err != nil {
			t.Fatalf("asking the controlling terminal for its foreground group: %v", err)
		}
		if self.TerminalGroup != group {
			t.Fatalf("ReadLineage(this process) reads terminal group %d, and the terminal answers %d", self.TerminalGroup, group)
		}
		if self.Foreground() != (group == self.Group) {
			t.Fatalf("Foreground() = %v for group %d and terminal group %d", self.Foreground(), self.Group, group)
		}
	}

	child := exec.Command("sleep", "60")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	read, found, err := agents.ReadLineage(child.Process.Pid)
	if err != nil || !found {
		t.Fatalf("ReadLineage(a child in its own session) = %+v, %v, %v", read, found, err)
	}
	if read.TerminalGroup != 0 || read.Foreground() {
		t.Fatalf("a child in a session of its own reads %+v, want no terminal group and not Foreground", read)
	}
}

// The process of a tmux pane is in the foreground of the pseudo-terminal
// tmux allocated for it, and reads so. That is the positive control for
// Foreground, and it is also the gap the predicate's doc states: a program
// that allocates a terminal of its own puts a command in a terminal's
// foreground, so the reading is evidence of a terminal and not of a person.
func TestATmuxPanesProcessIsInTheForegroundOfItsTerminal(t *testing.T) {
	tmux := standin.Tmux(t)
	dir, err := standin.TempDir(standin.TempTagAgents, "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	if out, err := exec.Command(tmux, "-S", socket, "new-session", "-d", "-x", "80", "-y", "24", "--", "sleep", "60").CombinedOutput(); err != nil {
		t.Fatalf("starting a tmux server: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(tmux, "-S", socket, "kill-server").Run() })
	out, err := exec.Command(tmux, "-S", socket, "display-message", "-p", "#{pane_pid}").Output()
	if err != nil {
		t.Fatalf("reading the pane's process: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("the pane's process reads %q", out)
	}
	pane, found, err := agents.ReadLineage(pid)
	if err != nil || !found {
		t.Fatalf("ReadLineage(the pane's process %d) = %+v, %v, %v", pid, pane, found, err)
	}
	if !pane.Foreground() || pane.TerminalGroup != pane.Group {
		t.Fatalf("the pane's process reads %+v, want it in the foreground of its terminal", pane)
	}
}
