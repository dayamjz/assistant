package supervisor

import "github.com/dayamjz/assistant/internal/taskstate"

// terminalWatchedByState is the one owner of which resolved states have
// their terminal read by a supervision pass, and so can raise
// KindTerminalGone and KindTerminalUnchanged. It is closed over
// taskstate.States() on inflight.go's terms: a test derives the set from
// that function and fails on a state with no row.
//
// Every state is watched but Reported, Landed and Complete. PRD section 6
// has a stored report outrank the terminal's liveness, so a task that
// resolves Reported has delivered, and a terminal that ends after the report
// is the work finishing rather than a worker that crashed; surfacing it as
// terminal gone would be the delivered investigation read as a crash, which
// is what the rule exists to stop. What the report raised still reaches a
// person as its holds, which every pass detects for every task whether or
// not its terminal is read. Reported reaches this table from
// internal/taskstate's report tier, or from the log where nothing above it
// answered, which no production path produces and taskstate's doc states.
//
// Landed and Complete are left unread on the same ground, by the owner's
// decision on #320: internal/taskstate's terminal tier outranks every run
// and the terminal, so a task in either has finished its work, is owed no
// relaunch, and keeps no worker, and a terminal that ends then is the work
// finishing too. Neither is in flight on its state alone, so before this a
// terminal-gone wake reached either only while a queued wake kept the task
// watched, which a coordinator that drains and never acknowledges makes
// permanent (#268). The table decides only what this loop surfaces: what a
// landed task's Dead terminal still triggers, its worker's end and its
// cleanup decision, reads the resolution's own probe and never this table,
// which internal/service's landed.go owns.
//
// The table answers which terminals are read and nothing else: whether a
// task is watched at all is InFlightOf's table and the queue, so a task in
// any of the three with a queued wake still has its events past the marker read.
var terminalWatchedByState = map[taskstate.State]bool{
	taskstate.Queued:     true,
	taskstate.Dispatched: true,
	taskstate.Running:    true,
	taskstate.Paused:     true,
	taskstate.Validating: true,
	taskstate.Unknown:    true,
	taskstate.Held:       true,
	taskstate.Ready:      true,
	taskstate.Landed:     false,
	taskstate.Reported:   false,
	taskstate.Complete:   false,
	taskstate.Failed:     true,
}

// TerminalWatched reports whether a watched task in state s has its
// terminal read, and so can surface a terminal-gone or terminal-unchanged
// wake, and whether the table decides s at all. A state the table does not
// decide is answered watched, the safe direction: a terminal nobody reads
// is a crash nobody is told about.
func TerminalWatched(s taskstate.State) (watched, decided bool) {
	watched, decided = terminalWatchedByState[s]
	if !decided {
		return true, false
	}
	return watched, true
}
