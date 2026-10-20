//go:build linux || darwin

package standin

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

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
