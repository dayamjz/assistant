package home

import (
	"testing"
)

// Two probes overlapping make the second answer Held with no service running,
// because a probe holds the lock for the instant it asks and the second asks
// inside that instant. This is the gap LockState's doc names and the reason a
// lone Held is not evidence of a live service; the test is the positive
// demonstration that it can happen, through the real probe both times. It is
// an internal test because the instant is not otherwise reachable on purpose:
// the hook runs between taking the lock and letting go, and nothing else
// sets it. It is not parallel because the hook is package state.
func TestTwoOverlappingProbesCanAnswerHeldWithNoHolder(t *testing.T) {
	h, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	// Serve the home once so the lock file exists, then let go.
	lock, err := h.Acquire(t.Context(), 0)
	if err != nil {
		t.Fatalf("taking the lock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("releasing the lock: %v", err)
	}
	var inner LockReading
	var innerErr error
	probeHolding = func() {
		probeHolding = nil
		inner, innerErr = h.LockState(t.Context())
	}
	t.Cleanup(func() { probeHolding = nil })
	outer, err := h.LockState(t.Context())
	if err != nil {
		t.Fatalf("the outer probe: %v", err)
	}
	if outer.State != LockFree {
		t.Fatalf("the outer probe answered %v with no holder, want free", outer.State)
	}
	if innerErr != nil {
		t.Fatalf("the inner probe: %v", innerErr)
	}
	if inner.State != LockHeld {
		t.Fatalf("a probe overlapping another answered %v, want held: the gap this test demonstrates has closed, so the doc is now overstating it", inner.State)
	}
	// With nothing overlapping, the same home answers free, so the held
	// above came from the probe and not from the fixture.
	after, err := h.LockState(t.Context())
	if err != nil || after.State != LockFree {
		t.Fatalf("after the overlap the lock probed as (%+v, %v), want free", after, err)
	}
}
