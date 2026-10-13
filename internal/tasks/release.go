package tasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Result is what Release did about one queued task.
type Result string

const (
	// Released is the task was dispatched: its blocker had cleared and the
	// dispatched entry was appended.
	Released Result = "released"
	// Waiting is the task stays queued: its blocker is live, or its blocker
	// cleared and another task still holds its branch. Outcome.Holding names
	// which.
	Waiting Result = "waiting"
	// Refused is nothing was appended and Outcome.Err says why: the task's
	// state was decided above the event tier (*OutrankedError), its history
	// moved under the release (*store.TaskMovedError), or its blocker could
	// not be read.
	Refused Result = "refused"
)

// Outcome is Release's answer for one task it considered.
type Outcome struct {
	// Task is the queued task.
	Task string
	// Blocker is the task it is queued behind.
	Blocker string
	// BlockerState is the blocker's state as this release read it, and empty
	// when the blocker could not be read.
	BlockerState taskstate.State
	// Holding names the task a Waiting task waits on: the blocker while it is
	// live, otherwise the task that holds the branch, which the floor at
	// release found. HoldingState is that task's state. Both are empty
	// unless Result is Waiting.
	Holding      string
	HoldingState taskstate.State
	// Result says what was done.
	Result Result
	// Err is the typed refusal when Result is Refused, and nil otherwise.
	Err error
	// Event is the dispatched entry when Result is Released.
	Event store.TaskEvent
}

// Release dispatches every queued task whose blocker has cleared, and reports
// what it did about every task it considered: one Outcome per task that
// names a blocker and still stands at its queued entry, in no particular
// order. It is the program's act and not a wake (PRD section 7): the
// dispatched entry it appends is itself a wake, which is how the coordinator
// learns of it. It is idempotent, so a caller that cannot tell whether
// anything cleared calls it anyway; internal/service does so when a run
// settles and when it opens.
//
// A task is considered when its record names a blocker and its resolution is
// Queued from the event tier. The dispatched entry is then appended with
// store.AppendTaskEventAfter anchored to the sequence that resolution read
// (Resolution.Basis.EventSequence), so two releases that read the same tip
// append one entry between them and the other is refused with a
// *store.TaskMovedError on its outcome (P6). A task whose history still
// claims queued while a higher tier decided its state is refused with an
// *OutrankedError rather than appended past: the anchor protects the history,
// and a run or a terminal can move without an entry. A task that names a
// blocker and has moved on, Dispatched or anything after it, is not
// considered and gets no outcome.
//
// Whether a blocker has cleared is Clears, read off the blocker's resolution
// from the same read as the queued task's; Clears' doc says what that set
// leaves out today. A blocker that cleared is not the whole of it: the floor
// Create applies is run again here, from the same read, and a task whose
// repository and branch another task still holds stays Waiting on that task.
// Holding is a live task that is not itself queued; a queued task waits its
// own turn and holds nothing, or two tasks queued on one branch would each
// wait on the other. That is what lets the record name one blocker while a
// claim of overlap and the floor are both honoured. A chain of queued tasks
// releases one link per call, since a successor's blocker is read as it
// stood before this call's writes.
//
// It returns an error only when the records could not be read or an append
// failed for a reason other than the anchor; the outcomes gathered before
// that failure come back with it.
func (q *Queue) Release(ctx context.Context) ([]Outcome, error) {
	resolved, err := q.reader.All(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]taskstate.Resolved, len(resolved))
	for _, r := range resolved {
		byID[r.Record.ID] = r
	}
	var out []Outcome
	for _, r := range resolved {
		blocker, queued := r.Record.BlockerTask.Get()
		if !queued {
			continue
		}
		o := Outcome{Task: r.Record.ID, Blocker: blocker}
		switch {
		case r.State.State == taskstate.Queued && r.State.Source == taskstate.SourceEvent:
		case claimsQueued(r.State):
			o.Result, o.Err = Refused, &OutrankedError{Task: r.Record.ID, State: r.State.State, Source: r.State.Source}
			out = append(out, o)
			continue
		default:
			continue
		}
		b, ok := byID[blocker]
		if !ok {
			o.Result, o.Err = Refused, fmt.Errorf("tasks: the blocker %s of task %s: %w", blocker, r.Record.ID, store.ErrNotFound)
			out = append(out, o)
			continue
		}
		o.BlockerState = b.State.State
		if !Clears(b.State.State) {
			o.Result, o.Holding, o.HoldingState = Waiting, blocker, b.State.State
			out = append(out, o)
			continue
		}
		if holder, held := holdsBranch(r, resolved); held {
			o.Result, o.Holding, o.HoldingState = Waiting, holder.Record.ID, holder.State.State
			out = append(out, o)
			continue
		}
		if q.betweenReadAndWrite != nil {
			q.betweenReadAndWrite()
		}
		event, err := q.store.AppendTaskEventAfter(ctx, r.Record.ID, r.State.Basis.EventSequence,
			store.DispatchedEventKind, fmt.Sprintf("released: blocker %s resolved %s", blocker, b.State.State))
		switch {
		case errors.Is(err, store.ErrTaskMoved):
			o.Result, o.Err = Refused, err
		case err != nil:
			return out, err
		default:
			o.Result, o.Event = Released, event
		}
		out = append(out, o)
	}
	return out, nil
}

// holdsBranch is the floor at release: the newest task other than r on r's
// repository and branch that is live and not itself queued, if there is one.
// A task with no branch is held by nothing.
func holdsBranch(r taskstate.Resolved, resolved []taskstate.Resolved) (taskstate.Resolved, bool) {
	branch, ok := r.Record.Branch.Get()
	if !ok {
		return taskstate.Resolved{}, false
	}
	var holder taskstate.Resolved
	found := false
	for _, other := range resolved {
		if other.Record.ID == r.Record.ID || other.Record.RepositoryID != r.Record.RepositoryID ||
			other.Record.Branch.Or("") != branch {
			continue
		}
		if !Live(other.State.State) || other.State.State == taskstate.Queued {
			continue
		}
		if !found || other.Record.Row > holder.Record.Row {
			holder, found = other, true
		}
	}
	return holder, found
}

// claimsQueued reports whether the resolution overrode a queued claim: its
// history's newest entry claims Queued and a higher tier answered. It reads
// what the resolver reported as superseded and never the history itself.
func claimsQueued(r taskstate.Resolution) bool {
	for _, s := range r.Superseded {
		if s.Claimed == taskstate.Queued {
			return true
		}
	}
	return false
}
