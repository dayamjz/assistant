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
	locks := map[home.LockState]fault{
		home.LockHeld:     {"lock held", ""},
		home.LockFree:     {"lock free", supervisor.StopLockFree},
		home.LockUnknown:  {"lock unknown", supervisor.StopLockUnknown},
		home.LockState(9): {"lock outside the set", supervisor.StopLockUnknown},
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
						got := supervisor.Assess(inFlight, beacon, home.LockReading{State: lock}, expect, fresh, now)

						// P10's sentence, as the oracle for Allowed.
						supervised := lock == home.LockHeld && !writtenAt.IsZero() &&
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
	var lock home.LockReading
	if inFlight != supervisor.InFlightUnknown || lock.State != home.LockUnknown {
		t.Errorf("the zero readings are %s and %s, want unknown and unknown", inFlight, lock.State)
	}
	got := supervisor.Assess(inFlight, supervisor.Beacon{}, lock, supervisor.Identity{}, supervisor.Freshness{}, now)
	if got.Allowed() {
		t.Errorf("all zero inputs allowed: %+v", got)
	}
	live := supervisor.Beacon{Identity: expect, WrittenAt: now}
	for _, state := range []home.LockState{home.LockUnknown, home.LockState(9)} {
		got := supervisor.Assess(supervisor.InFlightSome, live, home.LockReading{State: state}, expect, fresh, now)
		if got.Allowed() || got.Reason != supervisor.StopLockUnknown {
			t.Errorf("lock state %d beside an otherwise live supervisor: %+v, want blocked as lock unknown", state, got)
		}
	}
	empty := supervisor.Beacon{WrittenAt: now}
	if got := supervisor.Assess(supervisor.InFlightSome, empty, home.LockReading{State: home.LockHeld}, supervisor.Identity{}, fresh, now); got.Allowed() {
		t.Errorf("an empty instance matched an empty expectation: %+v", got)
	}
}

// The reason is the first failing check in Assess's stated order, so a cell
// with two adjacent faults names the earlier one: lock before instance,
// instance before epoch, epoch before the beacon's timestamp.
func TestTheReasonIsTheFirstFailingCheckInOrder(t *testing.T) {
	t.Parallel()
	held := home.LockReading{State: home.LockHeld}
	cases := []struct {
		name   string
		beacon supervisor.Beacon
		lock   home.LockReading
		want   supervisor.StopReason
	}{
		{"lock free and instance mismatch", supervisor.Beacon{Identity: supervisor.Identity{Instance: "svc-2", Epoch: expect.Epoch}, WrittenAt: now},
			home.LockReading{State: home.LockFree}, supervisor.StopLockFree},
		{"instance mismatch and epoch mismatch", supervisor.Beacon{Identity: supervisor.Identity{Instance: "svc-2", Epoch: expect.Epoch + 1}, WrittenAt: now},
			held, supervisor.StopInstanceMismatch},
		{"epoch mismatch and beacon stale", supervisor.Beacon{Identity: supervisor.Identity{Instance: expect.Instance, Epoch: expect.Epoch + 1}, WrittenAt: now.Add(-time.Hour)},
			held, supervisor.StopEpochMismatch},
	}
	for _, c := range cases {
		if got := supervisor.Assess(supervisor.InFlightSome, c.beacon, c.lock, expect, fresh, now); got.Reason != c.want {
			t.Errorf("%s: reason %s, want %s", c.name, got.Reason, c.want)
		}
	}
}
