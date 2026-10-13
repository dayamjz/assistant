package taskstate

import (
	"fmt"
	"slices"

	"github.com/dayamjz/assistant/internal/store"
)

// Inputs is everything Resolve decides from. A Snapshot read from the store
// converts to one with inputsOf.
type Inputs struct {
	// Task is the task record.
	Task store.Task
	// Runs are the runs attributed to the task, newest first. Resolve reads
	// the first and does not re-sort, so an order other than newest first is a
	// different question.
	Runs []store.Run
	// Holds are the task's holds; only an open one is read.
	Holds []store.Hold
	// Events is the whole history in append order.
	Events []store.TaskEvent
	// Process is what a probe of the task's terminal found.
	Process Liveness
}

// Resolve answers what is true of a task now, from its inputs alone. It is
// pure: no context, no store, no clock, so equal inputs give equal
// resolutions, and nothing it does can be a side effect.
//
// PRD section 6's precedence, first match wins:
//
//  1. The newest run attributed to the task, whether active or finished.
//     pending and running read as Validating, held as Held, passed as Ready,
//     failed and terminated as Failed. A status this build does not define
//     answers Unknown from the run rather than falling through to a lower
//     rung, because falling through would let an event outrank a run the
//     resolver could not read.
//  2. A live terminal reads as Running. A dead one with no attributed run
//     reads as Unknown from the process and does not fall back to the log. An
//     unobserved terminal skips the rung.
//  3. The log, and only when its newest event's kind claims a state in
//     kind.go. An event that closes a decision, a note, and a kind kind.go
//     does not list never answer.
//  4. Otherwise Unknown, with source none.
//
// Superseded lists every unclosed decision-open event and the newest event
// that claims a state, when a run or the process answered something else.
// Unclosed means no decision-closed event follows it anywhere later in the
// log, so the whole log is read and the tail alone decides nothing.
func Resolve(in Inputs) Resolution {
	res := Resolution{TaskID: in.Task.ID, Basis: Basis{Process: in.Process}}
	if n := len(in.Events); n > 0 {
		res.Basis.EventSeq = in.Events[n-1].Sequence
	}
	if len(in.Runs) > 0 {
		res.Basis.RunID = in.Runs[0].ID
		res.Basis.RunStatus = string(in.Runs[0].Status)
	}

	switch {
	case len(in.Runs) > 0:
		res.Source = SourceRun
		res.State, res.Detail = fromRun(in.Runs[0], in.Holds)
	case in.Process == Live:
		res.Source, res.State = SourceProcess, Running
		res.Detail = store.Known("the task's terminal is live")
	case in.Process == Dead:
		res.Source, res.State = SourceProcess, Unknown
		res.Detail = store.Known("the task's terminal is dead and no validation run is attributed to it")
	default:
		res.Source, res.State, res.Detail = fromEvents(in.Events)
	}

	if res.Source == SourceRun || res.Source == SourceProcess {
		res.Superseded = supersededBy(in.Events, res.State, res.Source)
	}
	return res
}

// runStates maps a run's status to the state of the task it belongs to.
var runStates = map[store.RunStatus]State{
	store.RunPending:    Validating,
	store.RunRunning:    Validating,
	store.RunHeld:       Held,
	store.RunPassed:     Ready,
	store.RunFailed:     Failed,
	store.RunTerminated: Failed,
}

func fromRun(run store.Run, holds []store.Hold) (State, store.Optional[string]) {
	state, ok := runStates[run.Status]
	if !ok {
		return Unknown, store.Known(fmt.Sprintf("run %s has status %q, which this resolver does not know", run.ID, run.Status))
	}
	if state == Held {
		for _, h := range holds {
			if id, known := h.RunID.Get(); known && id == run.ID && h.Open() {
				return state, store.Known(fmt.Sprintf("run %s is held: %s", run.ID, h.Subject))
			}
		}
	}
	return state, store.Known(fmt.Sprintf("run %s is %s", run.ID, run.Status))
}

func fromEvents(events []store.TaskEvent) (Source, State, store.Optional[string]) {
	if len(events) == 0 {
		return SourceNone, Unknown, store.Unknown[string]()
	}
	newest := events[len(events)-1]
	state, ok := claims(newest.Kind)
	if !ok {
		return SourceNone, Unknown, store.Unknown[string]()
	}
	if newest.Detail == "" {
		return SourceEvent, state, store.Unknown[string]()
	}
	return SourceEvent, state, store.Known(newest.Detail)
}

// supersededBy lists the events the answer outranked, in sequence order.
func supersededBy(events []store.TaskEvent, answer State, by Source) []Superseded {
	var out []Superseded
	add := func(e store.TaskEvent, claimed State) {
		if claimed != answer && !slices.ContainsFunc(out, func(s Superseded) bool { return s.Sequence == e.Sequence }) {
			out = append(out, Superseded{Sequence: e.Sequence, Kind: e.Kind, Claimed: claimed, By: by})
		}
	}

	closedAfter := false
	newestClaim := -1
	for i := len(events) - 1; i >= 0; i-- {
		row, ok := lookup(events[i].Kind)
		if !ok {
			continue
		}
		switch row.role {
		case roleDecisionClosed:
			closedAfter = true
		case roleDecisionOpen:
			if !closedAfter {
				add(events[i], row.state)
			}
			if newestClaim < 0 {
				newestClaim = i
			}
		case roleState:
			if newestClaim < 0 {
				newestClaim = i
			}
		}
	}
	if newestClaim >= 0 {
		state, _ := claims(events[newestClaim].Kind)
		add(events[newestClaim], state)
	}
	slices.SortFunc(out, func(a, b Superseded) int { return int(a.Sequence - b.Sequence) })
	return out
}
