//go:build linux || darwin

package procgroup_test

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// RunStarted hands started the identifier of the program Run started, and
// nothing reaps that program before started returns: started waits here for
// the program to exit, and it still answers signal zero then, as an exited
// and unreaped child does, after which Run's own reap succeeds. A RunStarted that called started after the reap, or that
// reaped first, fails the signal-zero check.
func TestRunStartedObservesTheProgramBeforeItIsReaped(t *testing.T) {
	cmd := standIn(t)
	calls, pid := 0, 0
	var observeErr, signalErr error
	err := procgroup.RunStarted(context.Background(), cmd, time.Second, func(p int) {
		calls++
		pid = p
		observeErr = procgroup.AwaitExit(p)
		signalErr = syscall.Kill(p, 0)
	})
	if err != nil {
		t.Fatalf("RunStarted = %v; want the program run and reaped", err)
	}
	if calls != 1 || cmd.Process == nil || pid != cmd.Process.Pid {
		t.Fatalf("started was called %d times with %d; want once with the program's identifier %v", calls, pid, cmd.Process)
	}
	if observeErr != nil {
		t.Fatalf("observing the program's exit inside started: %v", observeErr)
	}
	if signalErr != nil {
		t.Errorf("the program no longer answers signal zero inside started (%v), so it was reaped first", signalErr)
	}
}

// A command RunStarted refuses, or one under an ended context, is never
// started, and started is never called for it.
func TestRunStartedCallsNothingForAProgramItDidNotStart(t *testing.T) {
	called := false
	started := func(int) { called = true }
	if err := procgroup.RunStarted(context.Background(), standIn(t), 0, started); !errors.Is(err, procgroup.ErrMisconfigured) {
		t.Errorf("RunStarted with no grace = %v; want ErrMisconfigured", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := procgroup.RunStarted(ctx, standIn(t), time.Second, started); !errors.Is(err, context.Canceled) {
		t.Errorf("RunStarted under an ended context = %v; want the context's error", err)
	}
	if called {
		t.Errorf("started was called for a program that was never started")
	}
}
