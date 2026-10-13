package supervisor

import (
	"time"

	"github.com/dayamjz/assistant/internal/home"
)

// ArmOutcome is what arm verification found, as the closed set PRD section
// 5's arm verification row names.
type ArmOutcome string

// The outcomes.
const (
	// ArmStarted is a loop whose first beacon under the expected instance
	// appeared after the verification asked: the loop came up while it
	// watched.
	ArmStarted ArmOutcome = "started"
	// ArmAttached is a loop that was already live when the verification
	// asked: a fresh beacon under the expected instance, with the lock held,
	// was there to read on the first look. Sequence on the Arm is that
	// beacon's, which is what tells this from a beacon written since.
	ArmAttached ArmOutcome = "attached"
	// ArmFailed is a verification that could not establish a live loop, for
	// the reason beside it.
	ArmFailed ArmOutcome = "failed"
)

// ArmReason is why an ArmFailed is one, as a closed set. An Arm that is not
// failed carries the empty reason.
type ArmReason string

// The reasons.
const (
	// ArmServiceDown is a lock no process holds, or a probe or an instance
	// file that could not be read: there is no service to have a loop.
	ArmServiceDown ArmReason = "service-down"
	// ArmNoBeacon is a service holding the lock whose loop wrote no readable
	// beacon under its instance within the first-beacon wait.
	ArmNoBeacon ArmReason = "no-beacon"
	// ArmInstanceMismatch is a beacon under an instance other than the one
	// the instance file names, still there when the wait ended: a beacon
	// from a service that is gone, which the running one has not replaced.
	ArmInstanceMismatch ArmReason = "instance-mismatch"
	// ArmLoopStale is a beacon under the expected instance that is older
	// than the freshness window when the wait ends and was not rewritten
	// during it: the service is up and its loop is not writing.
	ArmLoopStale ArmReason = "loop-stale"
)

// Arm is arm verification's answer. Reason is set exactly when Outcome is
// ArmFailed, and Sequence is the beacon's when the outcome rests on one.
type Arm struct {
	Outcome  ArmOutcome
	Reason   ArmReason
	Sequence uint64
}

// ArmOf is arm verification's decision over one look at the home, pure on
// Assess's terms: asked is the beacon that was there when the verification
// began, zero when none was readable, and obs is the look being judged. It
// answers settled true with the verdict when the look decides the question,
// and settled false when the look is inconclusive and a caller still inside
// the first-beacon wait should look again; a caller whose wait has elapsed
// passes waitOver true and is always answered.
//
// The decision reads the look in this order. A lock not held is
// ArmServiceDown, settled at once, because waiting cannot make a service
// appear. A readable beacon under the expected instance that is fresh is a
// live loop: ArmAttached when it is the one that was there at the start,
// ArmStarted when its sequence is past that one's or no beacon was there at
// the start. Anything else is inconclusive until the wait is over, and then
// it is ArmInstanceMismatch for a readable beacon under another instance,
// ArmLoopStale for one under the expected instance that is not fresh, and
// ArmNoBeacon when none was readable.
//
// The epoch is not compared: a verification has no incarnation to expect,
// and a loop restarted in place during the wait is still the loop that came
// up. A beacon ahead of now beyond the tolerance is not fresh, on Assess's
// terms, and so reads as stale here.
func ArmOf(asked Beacon, obs Observation, fresh Freshness, now time.Time, waitOver bool) (arm Arm, settled bool) {
	if obs.Lock.State != home.LockHeld || obs.Instance == "" {
		return Arm{Outcome: ArmFailed, Reason: ArmServiceDown}, true
	}
	readable := !obs.Beacon.WrittenAt.IsZero()
	matching := readable && obs.Beacon.Instance == obs.Instance
	if matching && isFresh(obs.Beacon, fresh, now) {
		if !asked.WrittenAt.IsZero() && asked.Instance == obs.Instance && obs.Beacon.Sequence <= asked.Sequence {
			return Arm{Outcome: ArmAttached, Sequence: obs.Beacon.Sequence}, true
		}
		return Arm{Outcome: ArmStarted, Sequence: obs.Beacon.Sequence}, true
	}
	if !waitOver {
		return Arm{}, false
	}
	switch {
	case matching:
		return Arm{Outcome: ArmFailed, Reason: ArmLoopStale, Sequence: obs.Beacon.Sequence}, true
	case readable:
		return Arm{Outcome: ArmFailed, Reason: ArmInstanceMismatch, Sequence: obs.Beacon.Sequence}, true
	default:
		return Arm{Outcome: ArmFailed, Reason: ArmNoBeacon}, true
	}
}

// isFresh is Assess's freshness reading of a beacon on its own: not ahead of
// now past the tolerance, and younger than the window.
func isFresh(b Beacon, fresh Freshness, now time.Time) bool {
	age := now.Sub(b.WrittenAt)
	if age < 0 && -age > fresh.Ahead {
		return false
	}
	return age < fresh.Window
}
