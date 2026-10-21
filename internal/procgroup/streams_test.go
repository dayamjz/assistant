//go:build linux || darwin

package procgroup_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// streamGrace is the grace these tests give Run, and streamLimit how long a
// test waits for Run before failing it: well past the three graces Run may
// spend after a deadline, and well short of the minute a hanging stand-in
// waits.
const (
	streamGrace = 200 * time.Millisecond
	streamLimit = 10 * time.Second
)

// runWithin runs cmd through Run in a goroutine, under a deadline of
// streamGrace when deadline is set and under none otherwise, and fails the
// test when Run has not returned by streamLimit.
func runWithin(t *testing.T, cmd *exec.Cmd, deadline bool) error {
	t.Helper()
	ctx := context.Background()
	if deadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, streamGrace)
		defer cancel()
	}
	done := make(chan error, 1)
	go func() { done <- procgroup.Run(ctx, cmd, streamGrace) }()
	select {
	case err := <-done:
		return err
	case <-time.After(streamLimit):
		t.Fatalf("Run had not returned after %s", streamLimit)
		return nil
	}
}

// A caller's reader that never returns does not hold Run past its bound: the
// program is ended at its deadline and Run answers, leaving only the
// goroutine blocked in that read, which the cleanup releases by closing the
// pipe. Handed to os/exec instead, the read would be one cmd.Wait waits for
// without a bound, which is how a push into the gate with an idle pusher
// would have outlived its deadline.
func TestRunIsNotHeldByACallersIdleReader(t *testing.T) {
	principles.Cite(t, principles.P15)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), standInMode+"="+standInHang)
	cmd.Stdin = pr
	if err := runWithin(t, cmd, true); err == nil {
		t.Errorf("Run of a program its deadline ended = nil; want the kill it reported")
	}
}

// A caller's writer that blocks does not hold Run past its bound: the
// program's output pipe is cut and Run answers exec.ErrWaitDelay, saying a
// copier was left blocked in that write, rather than waiting for the write.
func TestRunIsNotHeldByACallersBlockedWriter(t *testing.T) {
	principles.Cite(t, principles.P15)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pr.Close() })
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), standInMode+"="+standInPrint)
	cmd.Stdout = pw
	err := runWithin(t, cmd, false)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Errorf("Run with a writer that never accepts = %v; want exec.ErrWaitDelay", err)
	}
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 0 {
		t.Errorf("the stand-in did not exit 0 as it was written to: %v", cmd.ProcessState)
	}
}

// The copying Run does in place of os/exec's delivers what the program wrote
// to an ordinary writer, whole, and reads an ordinary reader, so owning the
// copy costs no output in the case that holds nothing open.
func TestRunCopiesOrdinaryStreamsWhole(t *testing.T) {
	var out writerOnly
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), standInMode+"="+standInPrint)
	cmd.Stdout = &out
	cmd.Stdin = readerOnly{}
	if err := procgroup.Run(context.Background(), cmd, streamGrace); err != nil {
		t.Fatalf("Run = %v; want the stand-in's answer", err)
	}
	if got := string(out.b); got != "stand-in output\n" {
		t.Errorf("Run delivered %q; want the line the stand-in wrote", got)
	}
}

// writerOnly is an io.Writer that is not an *os.File, so Run copies to it.
type writerOnly struct{ b []byte }

func (w *writerOnly) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}

// readerOnly is an io.Reader that is not an *os.File and is empty.
type readerOnly struct{}

func (readerOnly) Read([]byte) (int, error) { return 0, io.EOF }
