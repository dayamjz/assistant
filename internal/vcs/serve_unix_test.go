//go:build unix

package vcs_test

import (
	"context"
	"errors"
	"io"
	"path/filepath"
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
