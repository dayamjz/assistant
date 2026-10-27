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
		regard         map[string]supervisor.Regard
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
		"a held task with an open stall hold and no queued wake": {
			states: map[string]taskstate.State{"a": taskstate.Held}, unacknowledged: map[string]int{"a": 0},
			regard: map[string]supervisor.Regard{"a": supervisor.Regardless}, want: supervisor.InFlightSome,
		},
		"a stall hold on another task leaves a held one out": {
			states: map[string]taskstate.State{"a": taskstate.Held, "b": taskstate.Landed},
			regard: map[string]supervisor.Regard{"b": supervisor.ByState}, want: supervisor.InFlightNone,
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
		"an unknown state whose state does not count": {
			states: map[string]taskstate.State{"a": taskstate.Unknown}, regard: map[string]supervisor.Regard{"a": supervisor.QueueOnly},
			want: supervisor.InFlightNone,
		},
		"an unknown state whose state does not count, with a queued wake": {
			states: map[string]taskstate.State{"a": taskstate.Unknown}, unacknowledged: map[string]int{"a": 1},
			regard: map[string]supervisor.Regard{"a": supervisor.QueueOnly}, want: supervisor.InFlightSome,
		},
		"a regard outside the three beside a running task": {
			states: map[string]taskstate.State{"a": taskstate.Running, "b": taskstate.Held},
			regard: map[string]supervisor.Regard{"b": 9}, want: supervisor.InFlightUnknown,
		},
		"a state outside the table beside a running one": {
			states: map[string]taskstate.State{"a": taskstate.Running, "b": "merged"}, want: supervisor.InFlightUnknown,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := supervisor.InFlightOf(c.states, c.unacknowledged, c.regard, c.domainWakes); got != c.want {
				t.Fatalf("InFlightOf = %s, want %s", got, c.want)
			}
		})
	}
}

// TaskInFlight is the per-task rule InFlightOf applies over a fleet: for
// every state the table decides, a task is in flight with a queued wake, or
// on its state as its regard says the state counts: ByState as the table
// decides, Regardless always, QueueOnly never. A lone task's fleet reading
// agrees with it in every case, so the catch-up read and the guard cannot
// count different work. A state the table does not decide, and a regard
// outside the three, are undecided either way.
func TestTaskInFlightIsTheRuleInFlightOfApplies(t *testing.T) {
	for _, state := range taskstate.States() {
		for _, queued := range []int{0, 1} {
			for _, regard := range []supervisor.Regard{supervisor.ByState, supervisor.Regardless, supervisor.QueueOnly} {
				inFlight, decided := supervisor.TaskInFlight(state, queued, regard)
				alone, _ := supervisor.StateInFlight(state)
				if !decided {
					t.Fatalf("%s regarded %d is not decided", state, regard)
				}
				byState := map[supervisor.Regard]bool{supervisor.ByState: alone, supervisor.Regardless: true, supervisor.QueueOnly: false}[regard]
				if want := byState || queued > 0; inFlight != want {
					t.Errorf("%s with %d queued, regarded %d: in flight %v, want %v", state, queued, regard, inFlight, want)
				}
				fleet := supervisor.InFlightOf(map[string]taskstate.State{"t": state}, map[string]int{"t": queued},
					map[string]supervisor.Regard{"t": regard}, 0)
				if (fleet == supervisor.InFlightSome) != inFlight {
					t.Errorf("%s with %d queued, regarded %d: the fleet reads %v and the task in flight %v",
						state, queued, regard, fleet, inFlight)
				}
			}
		}
		if _, decided := supervisor.TaskInFlight(state, 1, supervisor.QueueOnly+1); decided {
			t.Errorf("%s with a regard outside the three is decided", state)
		}
	}
	if _, decided := supervisor.TaskInFlight("undecided", 1, supervisor.ByState); decided {
		t.Error("a state the table has no row for is decided")
	}
}
