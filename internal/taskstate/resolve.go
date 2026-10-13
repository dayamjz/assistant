package taskstate

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/dayamjz/assistant/internal/store"
)

// runStates is how a run's status reads in the task lifecycle. It is coarser
// than the run's own vocabulary on purpose: a failed and a terminated run are
// both a task that stopped, and nothing here tells a parked run from a running
// one. A status with no entry resolves to Unknown rather than to a guess.
var runStates = map[store.RunStatus]State{
	store.RunPending:    Validating,
	store.RunRunning:    Validating,
	store.RunHeld:       Held,
	store.RunPassed:     Ready,
	store.RunFailed:     Failed,
	store.RunTerminated: Failed,
}

// Resolve answers what is true of a task now, from the records that own the
// facts, in PRD section 6's fixed precedence and first match wins:
//
//  1. The newest attributed run, whatever its status: SourceRun. A run that
//     is pending or running reads as Validating, held as Held, passed as
//     Ready, failed or terminated as Failed. A status outside the run
//     vocabulary reads as Unknown from SourceRun and does not fall through,
//     because a run exists and the log is exactly what it outranks.
//  2. The terminal: Live reads as Running, positive evidence. Dead with no run
//     reads as Unknown from SourceProcess and does not fall through to the
//     log, because a stale event is what a dead terminal must not be trusted
//     for. Unobserved skips the tier.
//  3. The log: only when its most recent event's kind has a row in this
//     package's table that claims a state, or says a decision is open (Held).
//     A kind that closes a decision, a note, an unrecognized kind, or an empty
//     log reads as Unknown from SourceNone.
//
// Resolve is a pure function of in: the same Inputs give the same Resolution,
// whatever order Runs, Holds and Events arrive in. It reads no clock, so no
// timestamp decides anything except which run is newest.
//
// Superseded is computed from the whole log, never from its tail alone, and
// only when a higher source decided. See Resolution.Superseded.
func Resolve(in Inputs) Resolution {
	events := slices.Clone(in.Events)
	slices.SortFunc(events, func(a, b store.TaskEvent) int { return cmp.Compare(a.Sequence, b.Sequence) })

	out := Resolution{TaskID: in.Task.ID, Basis: Basis{Process: Unobserved}}
	if in.Process == Live || in.Process == Dead {
		out.Basis.Process = in.Process
	}
	if n := len(events); n > 0 {
		out.Basis.EventSequence = events[n-1].Sequence
	}

	switch run, ok := newest(in.Runs); {
	case ok:
		out.Basis.RunID, out.Basis.RunStatus, out.Basis.RunUpdatedAt = run.ID, run.Status, run.UpdatedAt
		out.State, out.Source, out.Detail = fromRun(run, in.Holds)
	case in.Process == Live:
		out.State, out.Source = Running, SourceProcess
		out.Detail = store.Known("the task's terminal is live")
	case in.Process == Dead:
		out.State, out.Source = Unknown, SourceProcess
		out.Detail = store.Known("the task's terminal has stopped and no run is attributed to the task")
	default:
		out.State, out.Source, out.Detail = fromEvents(events)
	}

	if out.Source == SourceRun || out.Source == SourceProcess {
		out.Superseded = supersededBy(events, out.State, out.Source)
	}
	return out
}

// newest picks the newest run by creation time, with the identifier breaking a
// tie, so the choice does not depend on the order the runs were handed in.
func newest(runs []store.Run) (store.Run, bool) {
	var best store.Run
	for i, r := range runs {
		if i == 0 || r.CreatedAt.After(best.CreatedAt) || (r.CreatedAt.Equal(best.CreatedAt) && r.ID > best.ID) {
			best = r
		}
	}
	return best, len(runs) > 0
}

func fromRun(run store.Run, holds []store.Hold) (State, Source, store.Optional[string]) {
	state, ok := runStates[run.Status]
	if !ok {
		return Unknown, SourceRun, store.Known(fmt.Sprintf("run %s is in status %q, which has no task state", run.ID, run.Status))
	}
	if state == Held {
		if h, ok := oldestOpen(holds, run.ID); ok {
			return state, SourceRun, store.Known(h.Subject)
		}
	}
	return state, SourceRun, store.Known(fmt.Sprintf("run %s is %s", run.ID, run.Status))
}

// oldestOpen picks the oldest open hold that names the run itself: a hold of
// another attributed run, or one naming only the task, is not this run's.
func oldestOpen(holds []store.Hold, runID string) (store.Hold, bool) {
	var best store.Hold
	found := false
	for _, h := range holds {
		if id, ok := h.RunID.Get(); !h.Open() || !ok || id != runID {
			continue
		}
		if !found || h.OpenedAt.Before(best.OpenedAt) || (h.OpenedAt.Equal(best.OpenedAt) && h.Key < best.Key) {
			best, found = h, true
		}
	}
	return best, found
}

// fromEvents is the log tier. events is in sequence order. The detail is a
// fixed sentence naming the kind's role and never the worker's text.
func fromEvents(events []store.TaskEvent) (State, Source, store.Optional[string]) {
	if len(events) == 0 {
		return Unknown, SourceNone, store.Known("the task has no history")
	}
	row, ok := Lookup(events[len(events)-1].Kind)
	switch {
	case !ok:
		return Unknown, SourceNone, store.Known("the most recent event is of a kind with no defined meaning")
	case row.Role == RoleState || row.Role == RoleDecisionOpen:
		return row.State, SourceEvent, store.Known("from the most recent event, with no run or terminal to outrank it")
	default:
		return Unknown, SourceNone, store.Known("the most recent event claims no state")
	}
}

// supersededBy lists the events whose claim differs from state: every decision
// opened since the last decision was closed, and the newest event when it
// claims a state of its own. events is in sequence order. A close clears every
// open before it, because one decision is open at a time.
func supersededBy(events []store.TaskEvent, state State, by Source) []Superseded {
	var out []Superseded
	add := func(e store.TaskEvent, claimed State) {
		if claimed != state {
			out = append(out, Superseded{Sequence: e.Sequence, Kind: e.Kind, Claimed: claimed, By: by})
		}
	}
	var open []store.TaskEvent
	for _, e := range events {
		switch row, ok := Lookup(e.Kind); {
		case !ok:
		case row.Role == RoleDecisionOpen:
			open = append(open, e)
		case row.Role == RoleDecisionClosed:
			open = nil
		}
	}
	for _, e := range open {
		add(e, Held)
	}
	if n := len(events); n > 0 {
		tail := events[n-1]
		if row, ok := Lookup(tail.Kind); ok && row.Role == RoleState {
			add(tail, row.State)
		}
	}
	return out
}
