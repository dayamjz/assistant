package supervisor_test

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// passObserved is an open pass as the loop reads it: its runs, its latest
// attempt's session, and the probe's reading of it.
func passObserved(runs []store.Run, ref string, process taskstate.Liveness) taskstate.Observed {
	snap := store.TaskSnapshot{Task: store.Task{ID: "t", Housekeeping: true}, Runs: runs}
	attempt := store.TaskAttempt{TaskID: "t", Sequence: 1}
	if ref != "" {
		attempt.SessionRef = store.Known(ref)
	}
	snap.Attempts = []store.TaskAttempt{attempt}
	return taskstate.Observed{Snapshot: snap, State: taskstate.Resolution{State: taskstate.Held, Source: taskstate.SourceHold,
		Basis: taskstate.Basis{Process: process}}}
}

// PRD section 6's "nothing moves the pass", read from the records and the
// probe and never from the resolved state: a run not ended moves it, a
// worker read live or unobservable moves it, and a pass whose every run
// ended and whose worker reads dead, or that never had a session, does not;
// a retired pass, one in the terminal tier, and a task that is no pass are
// never moving on these terms.
func TestPassMovesIsReadFromTheRecordsAndTheProbe(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	run := func(status store.RunStatus) []store.Run { return []store.Run{{ID: "r", Status: status, CreatedAt: at}} }
	for name, c := range map[string]struct {
		o    taskstate.Observed
		want bool
	}{
		"a running run":        {passObserved(run(store.RunRunning), "", taskstate.Unobserved), true},
		"a held run":           {passObserved(run(store.RunHeld), "", taskstate.Unobserved), true},
		"a live worker":        {passObserved(run(store.RunFailed), "pane", taskstate.Live), true},
		"an unobserved worker": {passObserved(run(store.RunFailed), "pane", taskstate.Unobserved), true},
		"a dead worker":        {passObserved(run(store.RunTerminated), "pane", taskstate.Dead), false},
		"no session":           {passObserved(nil, "", taskstate.Unobserved), false},
	} {
		if got := supervisor.PassMoves(c.o); got != c.want {
			t.Errorf("%s: PassMoves = %v, want %v", name, got, c.want)
		}
		if got := supervisor.InFlightRegardless(c.o); got != c.want {
			t.Errorf("%s: InFlightRegardless = %v, want %v", name, got, c.want)
		}
	}
	retired := passObserved(run(store.RunRunning), "pane", taskstate.Live)
	retired.Snapshot.Retirement = store.Known(store.HousekeepingRetirement{Task: "t"})
	landed := passObserved(run(store.RunRunning), "pane", taskstate.Live)
	landed.State.Source = taskstate.SourceRecord
	plain := passObserved(run(store.RunRunning), "pane", taskstate.Live)
	plain.Snapshot.Task.Housekeeping = false
	for name, o := range map[string]taskstate.Observed{"retired": retired, "delivered": landed, "no pass": plain} {
		if supervisor.PassMoves(o) {
			t.Errorf("%s: PassMoves = true, want false", name)
		}
	}
}

// A task in flight regardless of its state counts in the fleet's reading
// whatever its state and its queue say, and the same Held task not so
// counted does not, which is the control that the reading looks at it.
func TestInFlightOfCountsATaskInFlightRegardless(t *testing.T) {
	t.Parallel()
	states := map[string]taskstate.State{"t": taskstate.Held}
	if got := supervisor.InFlightOf(states, nil, map[string]bool{"t": true}, 0); got != supervisor.InFlightSome {
		t.Errorf("a held task in flight regardless reads %s, want some", got)
	}
	if got := supervisor.InFlightOf(states, nil, nil, 0); got != supervisor.InFlightNone {
		t.Errorf("the same held task reads %s, want none", got)
	}
}
