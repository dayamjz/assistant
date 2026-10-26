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
// afterwards every attempt claim refuses it and so does the record of a
// session for the attempt claimed before it (internal/store's
// ErrPassRetired, with the retirement's own session anchor), so its latest
// attempt's terminal is the one the retirement read dead, or none, and the
// worker never moves a retired pass again, whatever a probe reads or
// cannot read. Only a run of the pass's branch started after its
// retirement moves it, on the same terms as an open pass's. That is why a
// reading with no probe, as a hook's, reads a retired pass exactly as the
// service does, where it reads an open pass toward in flight.
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
		return false
	}
	return o.State.Basis.Process != taskstate.Dead
}
