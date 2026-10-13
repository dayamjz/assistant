package supervisor

import "github.com/dayamjz/assistant/internal/taskstate"

// inFlightByState is the one owner of which resolved states owe a
// supervision cycle, which is what P10's "work is in flight" means here. It
// is closed over taskstate.States(): a test derives the set from that
// function and fails on a state with no row, so a new state cannot land
// without this table deciding it.
//
// In flight is a state in which something other than a person is expected
// to move next: a worker running or dispatched, a task queued behind one, a
// validation run advancing, a declared wait that will clear on its own, and
// Unknown, because a dead terminal with no run is a crashed worker and not
// a settled one. Not in flight on state alone is a state whose only waiter
// is a person: held at a decision, ready, reported, or failed. What keeps
// such a task under the guard until the person has been told is not its
// state but the queue, which InFlightOf reads beside this table.
//
// Landed and Complete are not States yet (#107 adds them, resolved from the
// merge and cleanup records); when they land, both belong on the not-in-flight
// side for the same reason as Ready, and the test will refuse the build until
// they are placed.
var inFlightByState = map[taskstate.State]bool{
	taskstate.Queued:     true,
	taskstate.Dispatched: true,
	taskstate.Running:    true,
	taskstate.Paused:     true,
	taskstate.Validating: true,
	taskstate.Unknown:    true,
	taskstate.Held:       false,
	taskstate.Ready:      false,
	taskstate.Reported:   false,
	taskstate.Failed:     false,
}

// StateInFlight reports whether a task in state s is in flight on its state
// alone, and whether the table decides s at all. A state the table has no
// row for is not decided, which InFlightOf reads as a reading it cannot
// take; it is never read as not in flight.
func StateInFlight(s taskstate.State) (inFlight, decided bool) {
	inFlight, decided = inFlightByState[s]
	return inFlight, decided
}

// InFlightOf is the in-flight reading over a fleet: states is each task's
// resolved state keyed by task identifier, and unacknowledged is how many
// queued wakes each task has, keyed the same way, which store.UnacknowledgedWakes
// returns. A task is in flight when its state is, on inFlightByState's
// terms, or when it has a queued wake, whatever its state: a hold nobody has
// been told about keeps the guard, and acknowledging the wake is what
// releases it. A task present only in unacknowledged counts too.
//
// The answer is InFlightSome when any task is in flight, InFlightNone when
// none is, including over an empty fleet, and InFlightUnknown when any task's
// state is one the table does not decide, because a reading that guessed
// about one task would be a reading, and the safe direction is to block.
// It is pure, and the two inputs are the caller's reads, taken as of
// whatever consistency the caller arranged.
func InFlightOf(states map[string]taskstate.State, unacknowledged map[string]int) InFlight {
	some := false
	for task, state := range states {
		inFlight, decided := StateInFlight(state)
		if !decided {
			return InFlightUnknown
		}
		if inFlight || unacknowledged[task] > 0 {
			some = true
		}
	}
	for _, n := range unacknowledged {
		if n > 0 {
			some = true
		}
	}
	if some {
		return InFlightSome
	}
	return InFlightNone
}
