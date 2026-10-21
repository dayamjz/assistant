//go:build linux || darwin

package procgroup_test

import (
	"syscall"
	"testing"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// AwaitExit returns once a child has exited and leaves it unreaped, which is
// what lets Run signal the child's group after it exited: the child still
// answers signal zero, as a zombie does, and the one reap that follows,
// cmd.Wait's, still finds it. An AwaitExit that reaped would fail the second
// check, since a child can be reaped once.
func TestAwaitExitObservesAnExitWithoutReapingIt(t *testing.T) {
	cmd := standIn(t)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the stand-in: %v", err)
	}
	pid := cmd.Process.Pid
	if err := procgroup.AwaitExit(pid); err != nil {
		_ = cmd.Wait()
		t.Fatalf("AwaitExit = %v; want the exit observed", err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("the child no longer answers signal zero after AwaitExit (%v), so it was reaped", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("cmd.Wait after AwaitExit = %v; want the reap AwaitExit left to it", err)
	}
}
