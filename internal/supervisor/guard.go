package supervisor

import (
	"time"

	"github.com/dayamjz/assistant/internal/home"
)

// Observation is what a reader of a home found: every input Assess takes
// that a process outside the loop can read for itself. supervisor/feed fills
// one in from the files, the lock and the database; the zero value is a
// reading nobody took, and GuardOf blocks on it the way Assess blocks on
// each zero input.
type Observation struct {
	// InFlight is the in-flight reading, InFlightUnknown when the database
	// could not be read.
	InFlight InFlight
	// Beacon is what the beacon file said, zero when it could not be read.
	Beacon Beacon
	// Lock is the probe's answer.
	Lock home.LockReading
	// Instance is the identity the instance file names, empty when it could
	// not be read.
	Instance string
}

// GuardOf is the pull guard's predicate: Assess over an observation, with
// the expected identity taken from the observation itself. The instance
// expected is the one the instance file names, which is PRD section 5's
// rule that a supervisor is live when the service that wrote the beacon is
// the one holding the lock. The epoch expected is the beacon's own, because
// a command outside the service holds no incarnation to compare against: the
// epoch check is the stop hook's and the repair fence's, which do, and the
// pull guard applies every other check Assess makes and not that one. An
// empty Instance blocks, since Assess refuses an expected instance that is
// empty.
func GuardOf(obs Observation, fresh Freshness, now time.Time) StopVerdict {
	expect := Identity{Instance: obs.Instance, Epoch: obs.Beacon.Epoch}
	return Assess(obs.InFlight, obs.Beacon, obs.Lock, expect, fresh, now)
}
