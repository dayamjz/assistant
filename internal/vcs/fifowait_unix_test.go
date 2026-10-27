//go:build unix

package vcs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// FifoGrace is how long a FIFO test waits for a digest after releasing the
// FIFO. A digest that was waiting there returns at once. Exported for
// vcs_test.
const FifoGrace = 5 * time.Second

// ReleaseFifo ends a read open waiting at path for a writer, by opening the
// FIFO for writing without waiting. With no reader there is nothing to
// release, and the open fails at once with ENXIO, which is not an error
// here. It never waits, so a test cleaning up after a digest that skipped
// the FIFO is not held by it (P15). Exported for vcs_test.
func ReleaseFifo(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ENXIO) {
		return nil
	}
	if err != nil {
		return err
	}
	return f.Close()
}

// AwaitFifoDigest waits up to limit for a digest's result on done. A
// result in time is returned with a nil error. Otherwise the FIFO is
// released with ReleaseFifo, done is waited on for at most grace more, and
// an error says which happened, carrying the result when one came. Every
// step is bounded, so the call returns within limit plus grace whether the
// digest waited on the FIFO, was only slow, or never returns. Exported for
// vcs_test.
func AwaitFifoDigest[T any](done <-chan T, fifo string, limit, grace time.Duration) (T, error) {
	select {
	case got := <-done:
		return got, nil
	case <-time.After(limit):
	}
	if err := ReleaseFifo(fifo); err != nil {
		var zero T
		return zero, fmt.Errorf("the digest did not return within %s, and releasing the FIFO failed: %w", limit, err)
	}
	select {
	case got := <-done:
		return got, fmt.Errorf("the digest did not return within %s; it returned after the FIFO was released", limit)
	case <-time.After(grace):
		var zero T
		return zero, fmt.Errorf("the digest did not return within %s, nor within %s of releasing the FIFO", limit, grace)
	}
}

// fifoWithoutReader is a FIFO nothing has open.
func fifoWithoutReader(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// withinBound runs f and fails the test if it has not returned within
// bound, so a test of a wait that does not end fails rather than hangs.
func withinBound(t *testing.T, bound time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(bound):
		t.Fatalf("the call did not return within %s", bound)
	}
}

// TestReleaseFifoWithNoReaderReturns releases a FIFO nothing reads, which is
// the cleanup after a digest that correctly skipped the FIFO but was slow.
// A blocking write open would wait for a reader forever; this one returns
// at once with nothing released.
func TestReleaseFifoWithNoReaderReturns(t *testing.T) {
	fifo := fifoWithoutReader(t)
	withinBound(t, 5*time.Second, func() {
		if err := ReleaseFifo(fifo); err != nil {
			t.Errorf("releasing a FIFO with no reader: %v", err)
		}
	})
}

// TestAwaitFifoDigestFinishesWithNoReader is a digest that never returns
// and never opened the FIFO. The wait gives up, finds no reader to release,
// gives up again after the grace, and returns an error saying so.
func TestAwaitFifoDigestFinishesWithNoReader(t *testing.T) {
	fifo := fifoWithoutReader(t)
	never := make(chan int)
	withinBound(t, 5*time.Second, func() {
		_, err := AwaitFifoDigest(never, fifo, 50*time.Millisecond, 100*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "nor within") {
			t.Errorf("error %v, want the wait to give up after the grace", err)
		}
	})
}

// TestAwaitFifoDigestReleasesAWaitingOpen is a digest stuck in a read open
// of the FIFO, the failure the FIFO tests exist to catch. The release ends
// that open, the stuck call returns, and the wait reports it as late.
func TestAwaitFifoDigestReleasesAWaitingOpen(t *testing.T) {
	fifo := fifoWithoutReader(t)
	done := make(chan int, 1)
	go func() {
		f, err := os.Open(fifo)
		if err == nil {
			_ = f.Close()
		}
		done <- 7
	}()
	withinBound(t, 5*time.Second, func() {
		got, err := AwaitFifoDigest(done, fifo, 200*time.Millisecond, 2*time.Second)
		if got != 7 || err == nil || !strings.Contains(err.Error(), "after the FIFO was released") {
			t.Errorf("result %d, error %v; want the released call's result and an error saying it was late", got, err)
		}
	})
}

// TestAwaitFifoDigestReportsASlowDigest is a digest that skipped the FIFO
// and returns only after the limit. There is no reader to release, and the
// late result is still returned with an error.
func TestAwaitFifoDigestReportsASlowDigest(t *testing.T) {
	fifo := fifoWithoutReader(t)
	done := make(chan int, 1)
	time.AfterFunc(300*time.Millisecond, func() { done <- 3 })
	withinBound(t, 5*time.Second, func() {
		got, err := AwaitFifoDigest(done, fifo, 50*time.Millisecond, 2*time.Second)
		if got != 3 || err == nil {
			t.Errorf("result %d, error %v; want the late result and an error", got, err)
		}
	})
}
