package supervisor

import "github.com/dayamjz/assistant/internal/taskstate"

// PassMoves reports whether something moves a housekeeping pass, PRD
// section 6's housekeeping: a run attributed to it that has not ended, or
// the worker of its latest attempt, when that attempt has a session. Each is
// read from the record or probe that owns it and never from the resolved
// state, because the hold tier sits above the run tier: a promoted pass with
// an open report decision resolves Held while its run still validates. A
// pass in the terminal tier, and a task that is not a pass, never moves on
// these terms.
//
// For an open pass the worker moves it whenever the probe did not read its
// terminal dead, whether it read it live or could not read it. A reading
// taken with no probe, as a hook's is, reads every launched pass's terminal
// Unobserved and so as moving, which is toward in flight.
//
// A retired pass's retirement was written only on a reading of its latest
// attempt's terminal as dead, or of that attempt as having no session, and
// every attempt claim refuses it afterwards, so for one the worker moves it
// only while the probe reads it live: a session recorded after that reading
// by a launch the attempt began before it, the residual gap
// internal/service's housekeeping names. An unobservable terminal of a
// retired pass is not read as moving: it is the one the retirement read
// dead, or that launch's, which the probe cannot read, a gap inside that
// gap, and reading it as moving would keep every retired pass in flight
// forever under a hook's reading. A run of the pass's branch started after
// its retirement moves it on the same terms as an open pass's.
//
// A pass something moves is in flight whatever it resolves (RegardOf), so
// its terminal is read on every supervision pass and the stop guard counts
// it (P10).
func PassMoves(o taskstate.Observed) bool {
	snap := o.Snapshot
	if !snap.Task.Housekeeping || o.State.Source == taskstate.SourceRecord {
		return false
	}
	for _, r := range snap.Runs {
		if !taskstate.RunSettled(r.Status) {
			return true
		}
	}
	n := len(snap.Attempts)
	if n == 0 || !snap.Attempts[n-1].SessionRef.IsKnown() {
		return false
	}
	if snap.Retirement.IsKnown() {
		return o.State.Basis.Process == taskstate.Live
	}
	return o.State.Basis.Process != taskstate.Dead
}
