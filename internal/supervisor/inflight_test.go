package supervisor_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// The in-flight table is closed over taskstate.States() in both directions:
// every state the resolver can name is decided, and a state the table
// decides is one the resolver can name. The oracle is written here from the
// critique's definition rather than read off the table, so a row that drifts
// fails against it, and a state added to taskstate with no row here fails
// the build's tests until it is placed. The counterfeit is a word outside
// the set, which must be undecided rather than read as either answer.
func TestEveryStateIsDecidedInFlightOrNot(t *testing.T) {
	t.Parallel()
	oracle := map[taskstate.State]bool{
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
	states := taskstate.States()
	if len(oracle) != len(states) {
		t.Fatalf("the oracle decides %d states and taskstate names %d: decide the new one here and in the table", len(oracle), len(states))
	}
	for _, s := range states {
		want, known := oracle[s]
		if !known {
			t.Fatalf("taskstate names %q and this test has no oracle for it", s)
		}
		got, decided := supervisor.StateInFlight(s)
		if !decided {
			t.Errorf("StateInFlight(%q) is undecided; the table needs a row", s)
			continue
		}
		if got != want {
			t.Errorf("StateInFlight(%q) = %t, want %t", s, got, want)
		}
	}
	for _, counterfeit := range []taskstate.State{"", "merged", "Running"} {
		if counterfeit.Recognized() {
			t.Fatalf("%q is a State now; place it in the table and the oracle", counterfeit)
		}
		if _, decided := supervisor.StateInFlight(counterfeit); decided {
			t.Errorf("StateInFlight(%q) decided a state taskstate cannot produce", counterfeit)
		}
	}
}

// InFlightOf over a fleet: nothing is none, any in-flight state is some, a
// state the person is the only waiter on is none until a queued wake puts
// it back under the guard, a task known only by its queued wake counts, and
// a state the table does not decide is unknown even beside a task that is
// plainly in flight, because a guess about one task is a guess.
func TestInFlightOfReadsTheStatesAndTheQueueTogether(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		states         map[string]taskstate.State
		unacknowledged map[string]int
		domainWakes    int
		want           supervisor.InFlight
	}{
		"an empty fleet": {want: supervisor.InFlightNone},
		"a running worker": {
			states: map[string]taskstate.State{"a": taskstate.Running}, want: supervisor.InFlightSome,
		},
		"a held task nobody is told about by the queue": {
			states: map[string]taskstate.State{"a": taskstate.Held}, want: supervisor.InFlightNone,
		},
		"a held task with a queued wake": {
			states: map[string]taskstate.State{"a": taskstate.Held}, unacknowledged: map[string]int{"a": 1},
			want: supervisor.InFlightSome,
		},
		"a failed task whose wake was acknowledged": {
			states: map[string]taskstate.State{"a": taskstate.Failed}, unacknowledged: map[string]int{"a": 0},
			want: supervisor.InFlightNone,
		},
		"a ready task beside a queued one": {
			states: map[string]taskstate.State{"a": taskstate.Ready, "b": taskstate.Queued}, want: supervisor.InFlightSome,
		},
		"a landed and a complete task": {
			states: map[string]taskstate.State{"a": taskstate.Landed, "b": taskstate.Complete}, want: supervisor.InFlightNone,
		},
		"a task known only by its queued wake": {
			unacknowledged: map[string]int{"gone": 2}, want: supervisor.InFlightSome,
		},
		"a queued wake about a domain beside a fleet nobody waits on": {
			states: map[string]taskstate.State{"a": taskstate.Held}, domainWakes: 1, want: supervisor.InFlightSome,
		},
		"an unknown state": {
			states: map[string]taskstate.State{"a": taskstate.Unknown}, want: supervisor.InFlightSome,
		},
		"a state outside the table beside a running one": {
			states: map[string]taskstate.State{"a": taskstate.Running, "b": "merged"}, want: supervisor.InFlightUnknown,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := supervisor.InFlightOf(c.states, c.unacknowledged, c.domainWakes); got != c.want {
				t.Fatalf("InFlightOf = %s, want %s", got, c.want)
			}
		})
	}
}

// TaskInFlight is the per-task rule InFlightOf applies over a fleet: for
// every state the table decides, a task is in flight on its state alone or
// with a queued wake, and a lone task's fleet reading agrees with it in
// every case, so the catch-up read and the guard cannot count different
// work. A state the table does not decide is undecided either way.
func TestTaskInFlightIsTheRuleInFlightOfApplies(t *testing.T) {
	for _, state := range taskstate.States() {
		for _, queued := range []int{0, 1} {
			inFlight, decided := supervisor.TaskInFlight(state, queued)
			alone, _ := supervisor.StateInFlight(state)
			if !decided {
				t.Fatalf("%s is not decided", state)
			}
			if want := alone || queued > 0; inFlight != want {
				t.Errorf("%s with %d queued: in flight %v, want %v", state, queued, inFlight, want)
			}
			fleet := supervisor.InFlightOf(map[string]taskstate.State{"t": state}, map[string]int{"t": queued}, 0)
			if (fleet == supervisor.InFlightSome) != inFlight {
				t.Errorf("%s with %d queued: the fleet reads %v and the task in flight %v", state, queued, fleet, inFlight)
			}
		}
	}
	if _, decided := supervisor.TaskInFlight("undecided", 1); decided {
		t.Error("a state the table has no row for is decided")
	}
}
