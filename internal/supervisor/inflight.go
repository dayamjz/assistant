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
// Landed and Complete, resolved from the merge record and the cleanup
// record, are not in flight on state alone for Ready's reason: the work has
// landed, and what waits, if anything, is a person.
//
// The table decides a state; whether a task's state counts at all is its
// Regard's, which keeps a few tasks in flight whatever they resolve and
// takes the say from a retired housekeeping pass.
var inFlightByState = map[taskstate.State]bool{
	taskstate.Queued:     true,
	taskstate.Dispatched: true,
	taskstate.Running:    true,
	taskstate.Paused:     true,
	taskstate.Validating: true,
	taskstate.Unknown:    true,
	taskstate.Held:       false,
	taskstate.Ready:      false,
	taskstate.Landed:     false,
	taskstate.Reported:   false,
	taskstate.Complete:   false,
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

// Regard is how one task's resolved state counts toward its being in
// flight, beside the queue, which counts for every task: PRD section 6's
// "work is in flight" decides most tasks on their state, keeps a few in
// flight whatever they resolve, and takes the state's say from one kind of
// task altogether. RegardOf is the one statement of which task has which.
type Regard uint8

const (
	// ByState is every task but the two below: inFlightByState decides it
	// on its resolved state.
	ByState Regard = iota
	// Regardless is a task in flight whatever it resolves: one with an
	// open stall hold, PRD section 6's "a held stall stays watched", whose
	// worker is still running though the task resolves Held, or a
	// housekeeping pass something moves (PassMoves), which section 6's
	// housekeeping counts in flight whatever it resolves.
	Regardless
	// QueueOnly is a task whose resolved state never puts it in flight, so
	// only a queued wake does: a retired housekeeping pass nothing moves.
	// Its retirement was written only once nothing moved it, and every
	// attempt claim refuses it after, so a state such as Unknown, Queued
	// or Dispatched that it can go on resolving names nothing that will
	// move it, which is section 6's "a retired pass is not in flight by its
	// state alone".
	QueueOnly
)

// RegardOf is the observed task's Regard: Regardless for an open stall hold
// or a housekeeping pass something moves, retired or not, QueueOnly for a
// retired pass nothing moves, and ByState for every other task. It is the
// one statement of that set, which the supervisor loop's watched set, the
// stop guard and the catch-up read all count.
func RegardOf(o taskstate.Observed) Regard {
	switch {
	case StallHeld(o) || PassMoves(o):
		return Regardless
	case o.Snapshot.Task.Housekeeping && o.Snapshot.Retirement.IsKnown():
		return QueueOnly
	}
	return ByState
}

// TaskInFlight reports whether one task is in flight: it has a queued wake,
// or its state is in flight on inFlightByState's terms as regard says it
// counts: ByState as the table decides, Regardless always, and QueueOnly
// never. The second result is false for a state the table does not decide,
// whatever the regard, and for a regard outside the three, which a caller
// reads as a reading it cannot take and never as not in flight. InFlightOf
// is this rule over a fleet, and PRD section 9's catch-up lists the tasks it
// holds true for, so the two cannot count different work.
func TaskInFlight(s taskstate.State, unacknowledged int, regard Regard) (inFlight, decided bool) {
	inFlight, decided = StateInFlight(s)
	switch regard {
	case ByState:
	case Regardless:
		inFlight = true
	case QueueOnly:
		inFlight = false
	default:
		decided = false
	}
	return inFlight || unacknowledged > 0, decided
}

// InFlightOf is the in-flight reading over a fleet: states is each task's
// resolved state keyed by task identifier; unacknowledged is how many
// queued wakes each task has, keyed the same way, which
// store.UnacknowledgedWakes returns; regard is how each task's state counts,
// keyed the same way, which RegardOf reads off each task's observation, a
// task it does not name counting ByState; and untasked is how many queued
// wakes about no task there are: about a domain, which
// store.UnacknowledgedDomainWakes returns, and about a kept work entry no
// task names, which store.UnacknowledgedKeptWorkWakes returns, and about a
// repository's housekeeping, which store.UnacknowledgedHousekeepingWakes
// returns. A task is in flight on TaskInFlight's terms: a hold nobody has
// been told about keeps the guard, and acknowledging the wake is what
// releases it: a drain hands a row out and releases nothing, so a wake keeps
// its task in flight until the coordinator acknowledges it acted. A task
// present only in unacknowledged counts too, and so does a queued wake
// about no task, on the same terms: a subordinate coordinator gone, a
// delegation holding for a person, or kept work no task names, that nobody
// has acknowledged keeps the home in flight whatever every task says.
//
// The answer is InFlightSome when any task is in flight, InFlightNone when
// none is, including over an empty fleet, and InFlightUnknown when any task's
// reading is one TaskInFlight does not decide, because a reading that
// guessed about one task would be a reading, and the safe direction is to
// block. It is pure, and the four inputs are the caller's reads, taken as of
// whatever consistency the caller arranged.
func InFlightOf(states map[string]taskstate.State, unacknowledged map[string]int, regard map[string]Regard, untasked int) InFlight {
	some := untasked > 0
	for task, state := range states {
		inFlight, decided := TaskInFlight(state, unacknowledged[task], regard[task])
		if !decided {
			return InFlightUnknown
		}
		if inFlight {
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
