//go:build linux || darwin

package procgroup_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// openDescriptors counts this process's open file descriptors. A pipe is
// opened and closed first, so a descriptor the runtime opens for its first
// pipe, such as its poller's, is open in every count rather than only in a
// later one.
func openDescriptors(t *testing.T) int {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("opening a pipe: %v", err)
	}
	_ = r.Close()
	_ = w.Close()
	d, err := os.Open("/dev/fd")
	if err != nil {
		t.Fatalf("listing descriptors: %v", err)
	}
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(-1)
	if err != nil {
		t.Fatalf("listing descriptors: %v", err)
	}
	return len(names)
}

// awaitDescriptors waits until this process holds want descriptors, and
// fails, naming what it saw last, if it does not within 10s. The command is
// kept reachable until then: without that, a file os/exec opened for it and
// nobody closed was closed by the runtime once it was unreachable, and this
// wait passed on the very leak it is for.
func awaitDescriptors(t *testing.T, want int, cmd *exec.Cmd) {
	t.Helper()
	defer runtime.KeepAlive(cmd)
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := openDescriptors(t)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d descriptors are open, want %d: one opened for the command was left open", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// awaitReap waits until the abandoned program pid has been reaped, which the
// identifier answering ESRCH says, since it does so only once nothing holds
// the process unreaped.
func awaitReap(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the abandoned program %d was not reaped within 30s", pid)
		}
		time.Sleep(time.Millisecond)
	}
}

// A stream from StdinPipe, StdoutPipe or StderrPipe is refused, with nothing
// started and no descriptor opened or closed, so a caller cannot build a
// command whose pipe end in this process is left open. Only cmd.Wait closes
// that end, and the reap Run leaves for ErrAbandoned does not call it: before
// this refusal, the reviewer's scenario here (a StdoutPipe, a 10ms deadline,
// a 1ns grace) reached ErrAbandoned and left the read end open after the
// reap, every time. The ends os/exec opened stay the caller's, and closing
// them returns the process to the descriptors it held before.
func TestRunRefusesAStreamFromOSExecsOwnPipe(t *testing.T) {
	cases := map[string]func(cmd *exec.Cmd) (io.Closer, *os.File, error){
		"StdinPipe": func(cmd *exec.Cmd) (io.Closer, *os.File, error) {
			p, err := cmd.StdinPipe()
			child, _ := cmd.Stdin.(*os.File)
			return p, child, err
		},
		"StdoutPipe": func(cmd *exec.Cmd) (io.Closer, *os.File, error) {
			p, err := cmd.StdoutPipe()
			child, _ := cmd.Stdout.(*os.File)
			return p, child, err
		},
		"StderrPipe": func(cmd *exec.Cmd) (io.Closer, *os.File, error) {
			p, err := cmd.StderrPipe()
			child, _ := cmd.Stderr.(*os.File)
			return p, child, err
		},
	}
	for name, pipe := range cases {
		t.Run(name, func(t *testing.T) {
			baseline := openDescriptors(t)
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), standInMode+"="+standInHang)
			parentEnd, childEnd, err := pipe(cmd)
			if err != nil || childEnd == nil {
				t.Fatalf("%s = %v, child end %v", name, err, childEnd)
			}
			before := openDescriptors(t)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			err = procgroup.Run(ctx, cmd, time.Nanosecond)
			cancel()
			if cmd.Process != nil {
				awaitReap(t, cmd.Process.Pid)
			}
			if !errors.Is(err, procgroup.ErrMisconfigured) {
				t.Errorf("Run = %v; want ErrMisconfigured", err)
			}
			if cmd.Process != nil {
				t.Errorf("Run started a command whose %s it refuses", name)
			}
			if got := openDescriptors(t); got != before {
				t.Errorf("%d descriptors are open after Run, %d before it: Run must open and close none for a command it refuses", got, before)
			}

			_ = parentEnd.Close()
			_ = childEnd.Close()
			awaitDescriptors(t, baseline, cmd)
		})
	}
}

// An abandoned program in every shape Run accepts, a reader for its input
// and a writer for each output, leaves no descriptor open once the reap Run
// left behind has reaped it: what Run opened it closed, and the reap holds
// nothing os/exec would have closed in cmd.Wait. It is the reviewer's
// scenario with the stream handed to Run as a writer instead of through
// StdoutPipe, and it is repeated until abandoned, as a 1ns grace makes
// abandonment likely rather than certain.
func TestAnAbandonedProgramLeavesNoDescriptorOpen(t *testing.T) {
	const attempts = 200
	for i := 0; i < attempts; i++ {
		baseline := openDescriptors(t)
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), standInMode+"="+standInHang)
		cmd.Stdin = strings.NewReader("input")
		cmd.Stdout = &bytes.Buffer{}
		cmd.Stderr = &bytes.Buffer{}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		err := procgroup.Run(ctx, cmd, time.Nanosecond)
		cancel()
		if cmd.Process == nil {
			t.Fatalf("Run started nothing: %v", err)
		}
		awaitReap(t, cmd.Process.Pid)
		awaitDescriptors(t, baseline, cmd)
		if errors.Is(err, procgroup.ErrAbandoned) {
			return
		}
	}
	t.Fatalf("Run did not abandon the program in %d attempts: the test no longer reaches the abandoned path", attempts)
}
