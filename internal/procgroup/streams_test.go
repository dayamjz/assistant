//go:build linux || darwin

package procgroup_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
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

// Once Run has returned, the input copier reads the caller's reader no more:
// the read in flight at the deadline is released afterwards with (0, nil),
// a valid answer io.Copy would follow with another read, and no second read
// arrives.
func TestRunReadsACallersReaderNoMoreOnceItReturned(t *testing.T) {
	principles.Cite(t, principles.P15)
	r := newGatedReader()
	t.Cleanup(r.close)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), standInMode+"="+standInHang)
	cmd.Stdin = r
	if err := runWithin(t, cmd, true); err == nil {
		t.Errorf("Run of a program its deadline ended = nil; want the kill it reported")
	}
	if !r.awaitReads(1, streamLimit) {
		t.Fatalf("the copier never read the caller's reader, so nothing was checked")
	}
	r.release()
	if r.awaitReads(2, time.Second) {
		t.Errorf("the copier read the caller's reader again after Run had returned")
	}
}

// gatedReader blocks every read until release or close, then answers the
// first released read with (0, nil); it counts the reads it was asked for.
type gatedReader struct {
	reads    chan struct{}
	released chan struct{}
	closed   chan struct{}
	once     sync.Once
	n        atomic.Int32
}

func newGatedReader() *gatedReader {
	return &gatedReader{reads: make(chan struct{}, 16), released: make(chan struct{}, 1), closed: make(chan struct{})}
}

func (g *gatedReader) Read([]byte) (int, error) {
	g.n.Add(1)
	g.reads <- struct{}{}
	select {
	case <-g.released:
		return 0, nil
	case <-g.closed:
		return 0, io.EOF
	}
}

func (g *gatedReader) release() { g.released <- struct{}{} }

func (g *gatedReader) close() { g.once.Do(func() { close(g.closed) }) }

// awaitReads reports whether at least n reads have been asked for within
// limit.
func (g *gatedReader) awaitReads(n int32, limit time.Duration) bool {
	end := time.Now().Add(limit)
	for g.n.Load() < n {
		if time.Now().After(end) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
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

// A caller's writer that fails is reported even though the program exited 0,
// as os/exec reports it: output that never reached the caller is not an
// answer. The writer fails the way a closed file does, so the error is the
// same os.ErrClosed that Run's own cut of a pipe produces, and only where it
// came from tells the two apart.
func TestRunReportsACallersWriterThatFails(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), standInMode+"="+standInPrint)
	cmd.Stdout = closedWriter(t)
	err := runWithin(t, cmd, false)
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("Run with a writer over a closed file = %v; want the writer's os.ErrClosed", err)
	}
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 0 {
		t.Errorf("the stand-in did not exit 0: %v", cmd.ProcessState)
	}
}

// A caller's reader that fails is reported too, on the same terms: the
// failure is os.ErrClosed, which Run's own closing of the input pipe also
// produces, and it is reported because it came from the caller's reader. The
// stand-in takes a moment to start and exit, so the failed read has ended
// before Run reads the copy's result.
func TestRunReportsACallersReaderThatFails(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), standInMode+"="+standInPrint)
	cmd.Stdin = failingReader{}
	err := runWithin(t, cmd, false)
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("Run with a reader that fails = %v; want the reader's os.ErrClosed", err)
	}
}

// failingReader is an io.Reader that is not an *os.File and fails every read
// the way a closed file does.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, &os.PathError{Op: "read", Path: "closed", Err: os.ErrClosed}
}

// closedWriter returns an io.Writer that is not an *os.File, so Run copies
// to it, over a file that is already closed, so every write fails with
// os.ErrClosed.
func closedWriter(t *testing.T) io.Writer {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatalf("creating the file: %v", err)
	}
	_ = f.Close()
	return struct{ io.Writer }{f}
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
