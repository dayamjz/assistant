package supervisor

import (
	"time"

	"github.com/dayamjz/assistant/internal/home"
)

// InFlight is whether any task is in flight, as the hook or the guard read
// it. The set is closed, and the zero value is InFlightUnknown so that a
// reading nobody filled in is not read as nothing in flight: an unreadable
// database is InFlightUnknown, never a zero-valued none. Which states are in
// flight is #105's table, not this package's.
type InFlight int

// The three in-flight readings.
const (
	// InFlightUnknown is a reading that could not be taken. Assess blocks on
	// it, because "no answer" must mean work may be under way.
	InFlightUnknown InFlight = iota
	// InFlightNone is a successful read that found no task in flight.
	InFlightNone
	// InFlightSome is a successful read that found at least one.
	InFlightSome
)

// String names the reading for a report.
func (f InFlight) String() string {
	switch f {
	case InFlightNone:
		return "none"
	case InFlightSome:
		return "some"
	default:
		return "unknown"
	}
}

// Identity is who a supervisor is: the service's instance identity, which
// the instance file holds, and the incarnation epoch of the loop inside it,
// which #100 bumps when the watchdog replaces the loop. A beacon carries
// one, and Assess compares it to the one the hook expects.
type Identity struct {
	Instance string
	Epoch    uint64
}

// Beacon is what the beacon file said, as #105's reader decoded it. A beacon
// that could not be read is the zero value, whose WrittenAt is zero, and
// Assess blocks on it.
type Beacon struct {
	Identity
	// WrittenAt is the wall-clock time the loop wrote the beacon.
	WrittenAt time.Time
}

// Freshness is how a beacon's age is judged. Both are values #105 computes
// from its key and its constant; this package owns neither number.
type Freshness struct {
	// Window is how old a beacon may be and still count as live. A beacon
	// written exactly Window ago is stale.
	Window time.Duration
	// Ahead is how far past now a beacon's timestamp may sit before the
	// beacon is unreadable: a clock set back, or a beacon written under a
	// skewed clock, would otherwise make a wedged loop look fresh for as
	// long as the skew lasts.
	Ahead time.Duration
}

// StopReason is why a stop verdict is what it is, as a closed set.
type StopReason string

// The reasons. The first two allow; every other one blocks.
const (
	// StopNothingInFlight is no task in flight, so no supervision is owed.
	StopNothingInFlight StopReason = "nothing-in-flight"
	// StopSupervised is work in flight and a live supervisor holding the
	// home: the lock held, the beacon's identity the expected one, and the
	// beacon fresh.
	StopSupervised StopReason = "supervised"
	// StopInFlightUnknown is a reading of what is in flight that could not
	// be taken.
	StopInFlightUnknown StopReason = "in-flight-unknown"
	// StopLockUnknown is a probe of the lock that could not establish
	// anything, or a reading outside internal/home's set.
	StopLockUnknown StopReason = "lock-unknown"
	// StopLockFree is a lock no process holds: the service that wrote the
	// beacon is gone, however fresh the beacon.
	StopLockFree StopReason = "lock-free"
	// StopNoBeacon is a beacon that could not be read.
	StopNoBeacon StopReason = "no-beacon"
	// StopInstanceMismatch is a beacon from a service other than the one the
	// instance file names, or an identity with no instance at all.
	StopInstanceMismatch StopReason = "instance-mismatch"
	// StopEpochMismatch is a beacon from a loop incarnation other than the
	// current one.
	StopEpochMismatch StopReason = "epoch-mismatch"
	// StopBeaconAhead is a beacon timestamped further past now than the
	// tolerance allows.
	StopBeaconAhead StopReason = "beacon-ahead"
	// StopBeaconStale is a beacon older than the freshness window: the loop
	// is wedged or stopped, whether or not its process lives.
	StopBeaconStale StopReason = "beacon-stale"
)

// StopVerdict is Assess's answer: whether the coordinator may end its turn,
// and why. Allowed is derived from Reason and never set beside it, so the
// two cannot disagree.
type StopVerdict struct {
	Reason StopReason
}

// Allowed reports whether the turn may end.
func (v StopVerdict) Allowed() bool {
	return v.Reason == StopNothingInFlight || v.Reason == StopSupervised
}

// Assess decides whether a coordinator may end a turn, P10's stop guard as
// a function over what the hook read. It is pure except that now is
// injected: freshness is a wall-clock comparison across processes, so the
// clock has to come from the caller. lock is internal/home's own reading,
// taken by the caller's probe; this function performs no probe and reads
// only its State, which keeps one owner for that vocabulary.
//
// The answer is allowed in exactly two cases: nothing is in flight, or work
// is in flight and a live supervisor holds this home, which means every one
// of these holds: the lock is held, the beacon was read, its instance is the
// expected one and not empty, its epoch is the expected one, its timestamp
// is not ahead of now by more than fresh.Ahead, and its age is less than
// fresh.Window. Every other reading blocks, with the first failing check as
// the reason, in the order just listed; an unknown reading of what is in
// flight blocks before anything else is read.
//
// Two gaps are deliberate. A suspended machine, a clock jump, or a loop that
// is merely late makes a fresh beacon read stale and blocks a turn that could
// have ended; that is the safe direction, and nothing here tries to tell the
// cases apart. And a held lock beside a matching fresh beacon is read as the
// service that wrote the beacon holding the lock, which is what PRD section
// 5 says live means, while a lone held lock may be another probe's instant;
// internal/home's LockState states that gap and the consumer rule, and this
// function applies the rule rather than closing the gap.
func Assess(inFlight InFlight, beacon Beacon, lock home.LockReading, expect Identity, fresh Freshness, now time.Time) StopVerdict {
	switch inFlight {
	case InFlightNone:
		return StopVerdict{Reason: StopNothingInFlight}
	case InFlightSome:
	default:
		return StopVerdict{Reason: StopInFlightUnknown}
	}
	switch lock.State {
	case home.LockHeld:
	case home.LockFree:
		return StopVerdict{Reason: StopLockFree}
	default:
		return StopVerdict{Reason: StopLockUnknown}
	}
	if beacon.WrittenAt.IsZero() {
		return StopVerdict{Reason: StopNoBeacon}
	}
	if beacon.Instance == "" || beacon.Instance != expect.Instance {
		return StopVerdict{Reason: StopInstanceMismatch}
	}
	if beacon.Epoch != expect.Epoch {
		return StopVerdict{Reason: StopEpochMismatch}
	}
	age := now.Sub(beacon.WrittenAt)
	if age < 0 && -age > fresh.Ahead {
		return StopVerdict{Reason: StopBeaconAhead}
	}
	if age >= fresh.Window {
		return StopVerdict{Reason: StopBeaconStale}
	}
	return StopVerdict{Reason: StopSupervised}
}
