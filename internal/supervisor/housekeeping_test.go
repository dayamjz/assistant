package supervisor_test

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
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

// resolving is the observed pass resolving state, from the terminal tier
// for Landed and Complete, the only states it gives, as the resolver pairs
// them, and from the hold tier otherwise.
func resolving(o taskstate.Observed, state taskstate.State) taskstate.Observed {
	o.State.State, o.State.Source = state, taskstate.SourceHold
	if state == taskstate.Landed || state == taskstate.Complete {
		o.State.Source = taskstate.SourceRecord
	}
	return o
}

// retire gives the observed pass its retirement record.
func retire(o taskstate.Observed) taskstate.Observed {
	o.Snapshot.Retirement = store.Known(store.HousekeepingRetirement{Task: o.Snapshot.Task.ID})
	return o
}

// PRD section 6's "nothing moves the pass", read from the records and the
// probe and never from the resolved state: a run not ended moves an open
// pass, a worker read live or unobservable moves it, and a pass whose every
// run ended and whose worker reads dead, or that never had a session, does
// not. A retired pass is moved by a run not ended and by nothing else: its
// retirement read its worker dead or before it had a session, and no
// session is recorded for an attempt of a retired pass after that, so a
// probe's reading of its terminal, live or unobservable, moves it no more
// than a reading taken with no probe does, which is what lets the stop
// hook's reading agree with the service's (P10). A pass in the terminal
// tier, and a task that is no pass, are never moving on these terms. A pass
// something moves is in flight whatever it resolves (Regardless); a retired
// pass nothing moves is in flight on its queue only.
func TestPassMovesIsReadFromTheRecordsAndTheProbe(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P10)
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	run := func(status store.RunStatus) []store.Run { return []store.Run{{ID: "r", Status: status, CreatedAt: at}} }
	for name, c := range map[string]struct {
		o             taskstate.Observed
		open, retired bool
	}{
		"a running run":        {passObserved(run(store.RunRunning), "", taskstate.Unobserved), true, true},
		"a held run":           {passObserved(run(store.RunHeld), "", taskstate.Unobserved), true, true},
		"a live worker":        {passObserved(run(store.RunFailed), "pane", taskstate.Live), true, false},
		"an unobserved worker": {passObserved(run(store.RunFailed), "pane", taskstate.Unobserved), true, false},
		"a dead worker":        {passObserved(run(store.RunTerminated), "pane", taskstate.Dead), false, false},
		"no session":           {passObserved(nil, "", taskstate.Unobserved), false, false},
	} {
		for _, side := range []struct {
			name string
			o    taskstate.Observed
			want bool
			rest supervisor.Regard
		}{
			{"open", c.o, c.open, supervisor.ByState},
			{"retired", retire(c.o), c.retired, supervisor.QueueOnly},
		} {
			if got := supervisor.PassMoves(side.o); got != side.want {
				t.Errorf("%s, %s: PassMoves = %v, want %v", name, side.name, got, side.want)
			}
			want := side.rest
			if side.want {
				want = supervisor.Regardless
			}
			if got := supervisor.RegardOf(side.o); got != want {
				t.Errorf("%s, %s: RegardOf = %d, want %d", name, side.name, got, want)
			}
		}
	}
	landed := resolving(passObserved(run(store.RunRunning), "pane", taskstate.Live), taskstate.Landed)
	plain := passObserved(run(store.RunRunning), "pane", taskstate.Live)
	plain.Snapshot.Task.Housekeeping = false
	for name, o := range map[string]taskstate.Observed{"delivered": landed, "retired and delivered": retire(landed), "no pass": plain} {
		if supervisor.PassMoves(o) {
			t.Errorf("%s: PassMoves = true, want false", name)
		}
	}
	if got := supervisor.RegardOf(plain); got != supervisor.ByState {
		t.Errorf("a task that is no pass is regarded %d, want ByState", got)
	}
}

// PRD section 6's "a retired pass is not in flight by its state alone",
// over every state the resolver produces: a retired pass nothing moves is
// in flight with a queued wake and never on its state, Unknown, Queued and
// Dispatched among them, and the same pass not retired is in flight exactly
// as the state table says, which is the control that the retirement is what
// takes it out. A retired pass a run not ended moves is in flight whatever
// it resolves, short of the terminal tier, which nothing moves. A lone
// task's fleet reading agrees in every case.
func TestARetiredPassIsInFlightOnlyWhileSomethingMovesItOrAWakeIsQueued(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	ended := []store.Run{{ID: "r", Status: store.RunTerminated, CreatedAt: at}}
	running := []store.Run{{ID: "r", Status: store.RunRunning, CreatedAt: at}}
	fleet := func(o taskstate.Observed, queued int) supervisor.InFlight {
		return supervisor.InFlightOf(map[string]taskstate.State{"t": o.State.State}, map[string]int{"t": queued},
			map[string]supervisor.Regard{"t": supervisor.RegardOf(o)}, 0)
	}
	for _, state := range taskstate.States() {
		still := resolving(passObserved(ended, "pane", taskstate.Dead), state)
		alone, decided := supervisor.StateInFlight(state)
		if !decided {
			t.Fatalf("%s is not decided", state)
		}
		if got, _ := supervisor.TaskInFlight(state, 0, supervisor.RegardOf(still)); got != alone {
			t.Errorf("an open pass nothing moves resolving %s: in flight %v, want the table's %v", state, got, alone)
		}
		retired := retire(still)
		for queued, want := range map[int]bool{0: false, 1: true} {
			got, decided := supervisor.TaskInFlight(state, queued, supervisor.RegardOf(retired))
			if !decided || got != want {
				t.Errorf("a retired pass nothing moves resolving %s with %d queued: in flight %v (decided %v), want %v",
					state, queued, got, decided, want)
			}
			if (fleet(retired, queued) == supervisor.InFlightSome) != want {
				t.Errorf("a retired pass nothing moves resolving %s with %d queued: the fleet reads %s", state, queued, fleet(retired, queued))
			}
		}
		moved := retire(resolving(passObserved(running, "pane", taskstate.Dead), state))
		want := moved.State.Source != taskstate.SourceRecord
		if got, _ := supervisor.TaskInFlight(state, 0, supervisor.RegardOf(moved)); got != want || (fleet(moved, 0) == supervisor.InFlightSome) != want {
			t.Errorf("a retired pass whose run has not ended, resolving %s: in flight %v, fleet %s, want %v", state, got, fleet(moved, 0), want)
		}
	}
}

// A task in flight regardless of its state counts in the fleet's reading
// whatever its state and its queue say, and the same Held task not so
// counted does not, which is the control that the reading looks at it.
func TestInFlightOfCountsATaskInFlightRegardless(t *testing.T) {
	t.Parallel()
	states := map[string]taskstate.State{"t": taskstate.Held}
	if got := supervisor.InFlightOf(states, nil, map[string]supervisor.Regard{"t": supervisor.Regardless}, 0); got != supervisor.InFlightSome {
		t.Errorf("a held task in flight regardless reads %s, want some", got)
	}
	if got := supervisor.InFlightOf(states, nil, nil, 0); got != supervisor.InFlightNone {
		t.Errorf("the same held task reads %s, want none", got)
	}
}
