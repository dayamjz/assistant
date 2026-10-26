package supervisor

import "github.com/dayamjz/assistant/internal/taskstate"

// PassMoves reports whether something moves an open housekeeping pass, PRD
// section 6's housekeeping: a run attributed to it that has not ended, or a
// latest attempt with a session whose terminal the probe did not read dead,
// whether it read it live or could not read it. Each is read from the
// record or probe that owns it and never from the resolved state, because
// the hold tier sits above the run tier: a promoted pass with an open report
// decision resolves Held while its run still validates. A pass that is
// retired, or in the terminal tier, is not open and nothing moves it here; a
// task that is not a pass never moves on these terms.
//
// A pass something moves is in flight whatever it resolves, so its terminal
// is read on every supervision pass and the stop guard counts it (P10). A
// reading taken with no probe, as a hook's is, reads every launched pass's
// terminal Unobserved and so as moving, which is toward in flight.
func PassMoves(o taskstate.Observed) bool {
	snap := o.Snapshot
	if !snap.Task.Housekeeping || snap.Retirement.IsKnown() || o.State.Source == taskstate.SourceRecord {
		return false
	}
	for _, r := range snap.Runs {
		if !taskstate.RunSettled(r.Status) {
			return true
		}
	}
	if n := len(snap.Attempts); n > 0 && snap.Attempts[n-1].SessionRef.IsKnown() {
		return o.State.Basis.Process != taskstate.Dead
	}
	return false
}

// InFlightRegardless reports whether the observed task is in flight whatever
// it resolves and whatever its queue holds: it has an open stall hold, PRD
// section 6's "a held stall stays watched", or it is a housekeeping pass
// that something moves. It is the one statement of that set, which the
// supervisor loop's watched set, the stop guard and the catch-up read all
// count.
func InFlightRegardless(o taskstate.Observed) bool { return StallHeld(o) || PassMoves(o) }
