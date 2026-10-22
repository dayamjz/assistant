//go:build linux || darwin

package procgroup_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// An abandoned program's process state is never handed to the caller: once
// Run answers ErrAbandoned, cmd.ProcessState is nil and stays nil after the
// goroutine Run left behind has reaped the program, so a caller reading it,
// as every caller reading an exit status does, reads no exit status and
// races nothing. Before, that goroutine reaped through cmd.Wait, which
// writes the field, and the read raced it; the race detector is what sees
// that, so this is meant to run under -race, as make test runs it.
//
// A one-nanosecond grace makes the abandonment likely rather than certain,
// so the call repeats until it is abandoned and fails loudly if it never
// is. The reap is waited for by the identifier answering ESRCH, which it
// does only once nothing holds the process unreaped.
func TestAnAbandonedProgramsStateIsNeverTheCallers(t *testing.T) {
	const attempts = 200
	for i := 0; i < attempts; i++ {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), standInMode+"="+standInHang)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		err := procgroup.Run(ctx, cmd, time.Nanosecond)
		cancel()
		if !errors.Is(err, procgroup.ErrAbandoned) {
			continue
		}
		pid := cmd.Process.Pid
		deadline := time.Now().Add(30 * time.Second)
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				t.Fatalf("the abandoned program %d was not reaped within 30s", pid)
			}
			time.Sleep(time.Millisecond)
		}
		if cmd.ProcessState != nil {
			t.Fatalf("after the reap, an abandoned program's state reads %v, want nil", cmd.ProcessState)
		}
		return
	}
	t.Fatalf("Run did not abandon the program in %d attempts: the test no longer reaches the abandoned path", attempts)
}
