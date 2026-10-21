//go:build unix

package vcs_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/vcs"
)

// serveWithin runs a real git receive-pack into a fresh bare repository with
// a one-second invocation deadline, reading in and writing out, and fails the
// test when the call has not returned by hangLimit.
func serveWithin(t *testing.T, in io.Reader, out io.Writer) error {
	t.Helper()
	gitEnvironment(t)
	bare, err := vcs.InitBare(ctx(t), filepath.Join(t.TempDir(), "gate.git"), vcs.WithInvocationDeadline(time.Second))
	if err != nil {
		t.Fatalf("InitBare: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- bare.ServeReceivePack(context.WithoutCancel(context.Background()), in, out, io.Discard)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(hangLimit):
		t.Fatalf("ServeReceivePack had not returned after %s", hangLimit)
		return nil
	}
}

// A pushing side that never sends anything leaves receive-pack waiting on
// its input, and the call is still ended at its deadline: the reader the
// caller handed in is copied by procgroup.Run, so a read that never returns
// is not one the call waits for.
func TestAReceiveFromAnIdlePusherEndsAtItsDeadline(t *testing.T) {
	principles.Cite(t, principles.P15)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	err := serveWithin(t, pr, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ServeReceivePack = %v; want the deadline that ended it", err)
	}
}

// A receive whose output never reached the pushing side is not a success,
// even though receive-pack itself exits 0: the pusher's writer failing, here
// over a closed file, is reported as os/exec reports it, so undelivered
// protocol output does not read as a completed receive.
//
// The writer waits before it fails, so receive-pack has written its whole
// advertisement into the pipe, read the flush packet and exited 0 by then.
// Failing at once would close the pipe under a receive-pack still writing,
// which then dies of SIGPIPE, an exit that takes precedence and would let
// this test pass whether or not the writer's failure is reported.
func TestAReceiveWhoseOutputCannotBeDeliveredIsReported(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatalf("creating the file: %v", err)
	}
	_ = f.Close()
	// A flush packet is a push that sends no commands, which receive-pack
	// answers after its advertisement and then exits 0.
	err = serveWithin(t, strings.NewReader("0000"), bufio.NewWriterSize(lateWriter{f}, 16))
	var cmdErr *vcs.CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("ServeReceivePack = %v; want a *CommandError", err)
	}
	if cmdErr.ExitCode != 0 {
		t.Fatalf("receive-pack exited %d, so the writer's failure was not what this checked: %v", cmdErr.ExitCode, err)
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("ServeReceivePack = %v; want the pusher's writer's os.ErrClosed", err)
	}
}

// lateWriter writes to w after half a second, which is how long the receive
// in TestAReceiveWhoseOutputCannotBeDeliveredIsReported is given to finish.
type lateWriter struct{ w io.Writer }

func (l lateWriter) Write(p []byte) (int, error) {
	time.Sleep(500 * time.Millisecond)
	return l.w.Write(p)
}

// A pushing side that never reads what receive-pack writes, as well as never
// sending, does not hold the call past its bound either: the blocked write
// is left to its goroutine and the call answers.
func TestAReceiveToAPusherThatNeverReadsEndsAtItsDeadline(t *testing.T) {
	principles.Cite(t, principles.P15)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	t.Cleanup(func() {
		_ = inW.Close()
		_ = outR.Close()
	})
	err := serveWithin(t, inR, outW)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ServeReceivePack = %v; want the deadline that ended it", err)
	}
}
