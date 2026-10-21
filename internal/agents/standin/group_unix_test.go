//go:build unix

package standin

import (
	"io"
	"os/exec"
	"syscall"
	"testing"
)

// #360: groupOf reads the group of a child runCommand started in a group of
// its own, and it reads it after the child may already have exited, since
// nothing holds a command short enough to finish first. So the read is
// made here only once the child is exiting and before it is reaped, which
// is the window the reads of group 0 came from: the child's standard output
// is read to its end, which a shell running "exit 0" reaches only by
// exiting, then the group is read, and only then is the child waited for.
// The group read must still be the one the child leads. Swapping
// macOS's read for getpgid failed this in each of 20 runs.
func TestGroupOfReadsAnExitingChildBeforeItIsReaped(t *testing.T) {
	t.Parallel()
	for range 50 {
		cmd := exec.Command("/bin/sh", "-c", "exit 0")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, out); err != nil {
			t.Fatal(err)
		}
		group := groupOf(cmd.Process.Pid)
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
		if group != cmd.Process.Pid {
			t.Fatalf("the exited child's group read %d, want the group it leads, %d", group, cmd.Process.Pid)
		}
	}
}
