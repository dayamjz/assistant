//go:build linux || darwin

package agents_test

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// ReadLineage reports what the operating system's own calls report for this
// process, its parent and its process group, beside the identity Identify
// reads for it; a process this one started names this one as its parent and
// reads as started no earlier, which is the order agents.Lineage states; and
// an identifier no running process holds, one a reaped child released, is
// not found rather than an error or a lineage.
func TestReadLineageAgreesWithTheOperatingSystem(t *testing.T) {
	self, found, err := agents.ReadLineage(os.Getpid())
	if err != nil || !found {
		t.Fatalf("ReadLineage(this process) = %+v, %v, %v", self, found, err)
	}
	group, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	identity, _, err := agents.Identify(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if self.Identity != identity || self.Parent != os.Getppid() || self.Group != group {
		t.Fatalf("ReadLineage(this process) = %+v, want %v, parent %d, group %d", self, identity, os.Getppid(), group)
	}

	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	read, found, err := agents.ReadLineage(child.Process.Pid)
	if err != nil || !found || read.Parent != os.Getpid() || read.Start < self.Start {
		t.Fatalf("ReadLineage(child) = %+v, %v, %v; want this process %+v as its parent and no earlier start",
			read, found, err, self)
	}

	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := agents.ReadLineage(gone.Process.Pid); err != nil || found {
		t.Fatalf("ReadLineage(a reaped child) = found %v, %v; want not found", found, err)
	}
}
