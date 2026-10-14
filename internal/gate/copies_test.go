package gate_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/gate"
)

// TestAWriteIntoTheGateWaitsForACopyBeingCutAndProceedsOnceItIsDone is the
// copies lock's first promise: a receive or a fetch that arrives while a copy
// is being cut or removed waits for it, and takes the lock as soon as the cut
// lets go. The cut lets go only after the waiter has said it is waiting, so
// what is checked is a wait, not a lock that happened to be free.
func TestAWriteIntoTheGateWaitsForACopyBeingCutAndProceedsOnceItIsDone(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	cut, err := gate.LockCopies(ctx(t), repository)
	if err != nil {
		t.Fatalf("taking the copies lock for a cut: %v", err)
	}
	waiting := make(chan struct{})
	type result struct {
		lock *gate.CopiesLock
		err  error
	}
	got := make(chan result, 1)
	go func() {
		lock, err := gate.AwaitCopies(context.Background(), repository, time.Minute, func() { close(waiting) })
		got <- result{lock, err}
	}()
	select {
	case <-waiting:
	case r := <-got:
		t.Fatalf("a write arriving during a cut did not wait for it: %v", r.err)
	}
	if err := cut.Release(); err != nil {
		t.Fatalf("finishing the cut: %v", err)
	}
	r := <-got
	if r.err != nil {
		t.Fatalf("the write that waited for the cut was refused once the cut was done: %v", r.err)
	}
	if err := r.lock.Release(); err != nil {
		t.Fatalf("releasing the write's lock: %v", err)
	}
}

// TestAWriteHeldPastTheBoundIsRefusedWithTheTypedReason is the second: a
// write the cut holds longer than the bound is refused with
// ErrCopyInTransit, carried by a *CopyInTransitError naming what it waited
// on, and is told once that it is waiting.
func TestAWriteHeldPastTheBoundIsRefusedWithTheTypedReason(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	cut, err := gate.LockCopies(ctx(t), repository)
	if err != nil {
		t.Fatalf("taking the copies lock for a cut: %v", err)
	}
	defer func() { _ = cut.Release() }()
	announced := 0
	_, err = gate.AwaitCopies(ctx(t), repository, 20*time.Millisecond, func() { announced++ })
	var refused *gate.CopyInTransitError
	if !errors.Is(err, gate.ErrCopyInTransit) || !errors.As(err, &refused) {
		t.Fatalf("a write held past the bound returned %v, want a *gate.CopyInTransitError", err)
	}
	if refused.Repository != repository || refused.Waited != 20*time.Millisecond {
		t.Fatalf("the refusal names %q after %s, want %q after 20ms", refused.Repository, refused.Waited, repository)
	}
	if announced != 1 {
		t.Fatalf("the write was told it was waiting %d times, want once", announced)
	}
}

// TestWritesIntoTheGateDoNotWaitOnEachOther is the lock being shared: two
// pushes into one gate proceed side by side, and only a cut waits for them.
// The cut's wait is bounded by its context, which is the only bound it has.
func TestWritesIntoTheGateDoNotWaitOnEachOther(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	first, err := gate.AwaitCopies(ctx(t), repository, time.Minute, nil)
	if err != nil {
		t.Fatalf("taking the lock for a first write: %v", err)
	}
	defer func() { _ = first.Release() }()
	second, err := gate.AwaitCopies(ctx(t), repository, 0, func() {
		t.Error("a second write waited on the first")
	})
	if err != nil {
		t.Fatalf("a second write was refused beside the first: %v", err)
	}
	defer func() { _ = second.Release() }()

	bounded, cancel := context.WithTimeout(ctx(t), 50*time.Millisecond)
	defer cancel()
	if _, err := gate.LockCopies(bounded, repository); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a cut during two writes returned %v, want to have waited until its context ended", err)
	}
}

// TestAReceiveAddressedElsewhereIsRefused is the receive command refusing a
// push git addressed to a repository other than the gate the command was
// written for, before anything is locked or received.
func TestAReceiveAddressedElsewhereIsRefused(t *testing.T) {
	home, opts := newHome(t)
	wc := newWorkingCopy(t)
	command, _ := recorderCommand(t, 0)
	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("initializing: %v", err)
	}
	var out strings.Builder
	err = gate.Receive(ctx(t), gate.ReceiveSpec{
		Home: home, ID: g.ID(), Target: t.TempDir(),
		Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out, Wait: time.Second,
	})
	if !errors.Is(err, gate.ErrReceiveMismatch) {
		t.Fatalf("a receive addressed elsewhere returned %v, want ErrReceiveMismatch", err)
	}
	if out.Len() != 0 {
		t.Fatalf("a refused receive wrote %q", out.String())
	}
}
