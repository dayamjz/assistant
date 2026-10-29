//go:build linux || darwin

package stages

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A command whose own time limit had already passed when it was to start is
// not run, and is reported as having run out of that limit, the way a
// command its limit ended once it had started is: the bound is this call's,
// so the caller's ctx says nothing about it. The limit is handed in already
// passed rather than made short, so no load decides which path the call
// takes.
func TestACommandWhoseLimitPassedBeforeItStartedSaysItTimedOut(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := t.Context()
	runCtx, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	result := runBounded(ctx, runCtx, commandSpec{
		command: "touch ran",
		dir:     dir,
		record:  io.Discard,
		grace:   time.Second,
		timeout: 10 * time.Millisecond,
	})
	if _, err := os.Stat(filepath.Join(dir, "ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a command whose limit had passed ran (stat of its marker: %v)", err)
	}
	if result.exited || result.code != -1 {
		t.Fatalf("a command that was not started reported exited=%t status %d, want no status (-1)", result.exited, result.code)
	}
	if !errors.Is(result.err, errCommandTimedOut) {
		t.Fatalf("a command whose limit passed before it started answered %v, want errCommandTimedOut", result.err)
	}
}

// The other half of that rule: when the caller's ctx had ended before the
// command could start, the caller's ending is the explanation, so the result
// carries ctx's error and not errCommandTimedOut, though the bound made from
// that ctx has ended with it.
func TestACommandWhoseCallerEndedBeforeItStartedSaysSo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancelCaller := context.WithCancel(t.Context())
	cancelCaller()
	runCtx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	result := runBounded(ctx, runCtx, commandSpec{
		command: "touch ran",
		dir:     dir,
		record:  io.Discard,
		grace:   time.Second,
		timeout: time.Hour,
	})
	if _, err := os.Stat(filepath.Join(dir, "ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a command whose caller had ended ran (stat of its marker: %v)", err)
	}
	if result.exited || result.code != -1 {
		t.Fatalf("a command that was not started reported exited=%t status %d, want no status (-1)", result.exited, result.code)
	}
	if errors.Is(result.err, errCommandTimedOut) {
		t.Fatalf("a command whose caller ended before it started answered %v, which blames its own limit", result.err)
	}
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("a command whose caller ended before it started answered %v, want the caller's context.Canceled", result.err)
	}
}
