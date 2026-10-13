package supervisor_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/supervisor"
)

var (
	now    = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fresh  = supervisor.Freshness{Window: 30 * time.Second, Ahead: 5 * time.Second}
	expect = supervisor.Identity{Instance: "svc-1", Epoch: 2}
)

// Assess, exhaustively: every in-flight reading including unknown and a value
// outside the set, every lock reading including a value outside the set, the
// instance matching, mismatching and empty, the epoch matching and not, and
// a beacon unread, fresh, exactly at the window, past it, ahead within the
// tolerance and ahead beyond it. The oracle is P10's sentence: a turn may end
// when nothing is in flight, or when work is in flight and the service that
// wrote a fresh beacon is the one holding the lock. Allowed is checked for
// every cell, and the reason for every cell with at most one fault, so a
// precedence change is visible too.
func TestAssessAllowsOnlyASupervisedFleetOrAnEmptyOne(t *testing.T) {
	t.Parallel()
	type fault struct {
		name   string
		reason supervisor.StopReason
	}
	inFlights := map[supervisor.InFlight]fault{
		supervisor.InFlightSome:    {"in flight", ""},
		supervisor.InFlightNone:    {"nothing in flight", supervisor.StopNothingInFlight},
		supervisor.InFlightUnknown: {"in flight unknown", supervisor.StopInFlightUnknown},
		supervisor.InFlight(7):     {"in flight outside the set", supervisor.StopInFlightUnknown},
	}
	locks := map[supervisor.Lock]fault{
		supervisor.LockHeld:    {"lock held", ""},
		supervisor.LockFree:    {"lock free", supervisor.StopLockFree},
		supervisor.LockUnknown: {"lock unknown", supervisor.StopLockUnknown},
		supervisor.Lock(9):     {"lock outside the set", supervisor.StopLockUnknown},
	}
	instances := map[string]fault{
		expect.Instance: {"instance matches", ""},
		"svc-2":         {"instance mismatch", supervisor.StopInstanceMismatch},
		"":              {"instance empty", supervisor.StopInstanceMismatch},
	}
	epochs := map[uint64]fault{
		expect.Epoch:     {"epoch matches", ""},
		expect.Epoch + 1: {"epoch mismatch", supervisor.StopEpochMismatch},
	}
	writtenAts := map[time.Time]fault{
		{}:                                     {"beacon unread", supervisor.StopNoBeacon},
		now.Add(-fresh.Window + time.Second):   {"beacon fresh", ""},
		now:                                    {"beacon just written", ""},
		now.Add(-fresh.Window):                 {"beacon exactly at the window", supervisor.StopBeaconStale},
		now.Add(-time.Hour):                    {"beacon stale", supervisor.StopBeaconStale},
		now.Add(fresh.Ahead):                   {"beacon ahead within the tolerance", ""},
		now.Add(fresh.Ahead + time.Nanosecond): {"beacon ahead beyond the tolerance", supervisor.StopBeaconAhead},
	}

	cells := 0
	for inFlight, f1 := range inFlights {
		for lock, f2 := range locks {
			for instance, f3 := range instances {
				for epoch, f4 := range epochs {
					for writtenAt, f5 := range writtenAts {
						cells++
						name := fmt.Sprintf("%s, %s, %s, %s, %s", f1.name, f2.name, f3.name, f4.name, f5.name)
						beacon := supervisor.Beacon{Identity: supervisor.Identity{Instance: instance, Epoch: epoch}, WrittenAt: writtenAt}
						got := supervisor.Assess(inFlight, beacon, lock, expect, fresh, now)

						// P10's sentence, as the oracle for Allowed.
						supervised := lock == supervisor.LockHeld && !writtenAt.IsZero() &&
							instance == expect.Instance && instance != "" && epoch == expect.Epoch &&
							!writtenAt.After(now.Add(fresh.Ahead)) && now.Sub(writtenAt) < fresh.Window
						want := inFlight == supervisor.InFlightNone || (inFlight == supervisor.InFlightSome && supervised)
						if got.Allowed() != want {
							t.Errorf("%s: allowed=%t (%s), want %t", name, got.Allowed(), got.Reason, want)
						}

						// The reason, where at most one thing is wrong.
						var faults []supervisor.StopReason
						for _, f := range []fault{f2, f3, f4, f5} {
							if f.reason != "" {
								faults = append(faults, f.reason)
							}
						}
						switch {
						case f1.reason != "":
							if got.Reason != f1.reason {
								t.Errorf("%s: reason %s, want %s before anything else is read", name, got.Reason, f1.reason)
							}
						case len(faults) == 0:
							if got.Reason != supervisor.StopSupervised {
								t.Errorf("%s: reason %s, want supervised", name, got.Reason)
							}
						case len(faults) == 1:
							if got.Reason != faults[0] {
								t.Errorf("%s: reason %s, want %s", name, got.Reason, faults[0])
							}
						}
					}
				}
			}
		}
	}
	if cells != len(inFlights)*len(locks)*len(instances)*len(epochs)*len(writtenAts) {
		t.Fatalf("walked %d cells, which is not every combination", cells)
	}
}

// A zero StopVerdict blocks: Allowed is derived from a reason, and no reason
// nobody wrote allows. The zero in-flight and lock readings are the unknown
// ones, so a reading nobody filled in blocks too.
func TestTheZeroValuesBlock(t *testing.T) {
	t.Parallel()
	if (supervisor.StopVerdict{}).Allowed() {
		t.Error("a zero verdict allows")
	}
	var inFlight supervisor.InFlight
	var lock supervisor.Lock
	if inFlight != supervisor.InFlightUnknown || lock != supervisor.LockUnknown {
		t.Errorf("the zero readings are %s and %s, want unknown and unknown", inFlight, lock)
	}
	got := supervisor.Assess(inFlight, supervisor.Beacon{}, lock, supervisor.Identity{}, supervisor.Freshness{}, now)
	if got.Allowed() {
		t.Errorf("all zero inputs allowed: %+v", got)
	}
	empty := supervisor.Beacon{WrittenAt: now}
	if got := supervisor.Assess(supervisor.InFlightSome, empty, supervisor.LockHeld, supervisor.Identity{}, fresh, now); got.Allowed() {
		t.Errorf("an empty instance matched an empty expectation: %+v", got)
	}
}

// Lock restates internal/home's LockState because this package imports
// nothing that does I/O, and the two must not drift: same values, same
// order, same zero, same words.
func TestTheLockVocabularyIsHomes(t *testing.T) {
	t.Parallel()
	pairs := []struct {
		theirs home.LockState
		ours   supervisor.Lock
	}{
		{home.LockUnknown, supervisor.LockUnknown},
		{home.LockFree, supervisor.LockFree},
		{home.LockHeld, supervisor.LockHeld},
	}
	for _, p := range pairs {
		if int(p.theirs) != int(p.ours) || p.theirs.String() != p.ours.String() {
			t.Errorf("home.%s (%d) and supervisor.%s (%d) disagree", p.theirs, p.theirs, p.ours, p.ours)
		}
	}
	if home.LockState(len(pairs)).String() != "unknown" || supervisor.Lock(len(pairs)).String() != "unknown" {
		t.Errorf("one side has a fourth value this test does not pair")
	}
}
