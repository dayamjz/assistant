package supervisor_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// stallAt is a stall of a Running task whose terminal was captured, at the
// given count, with stall_inspection_count and stall_hold_count as stated.
func stallAt(repetitions, inspect, hold int) supervisor.Stall {
	return supervisor.Stall{Repetitions: repetitions, InspectAt: inspect, HoldAt: hold, State: taskstate.Running, Captured: true}
}

// PRD section 6's "a stalled worker holds for you", the classifier's half:
// with stall_inspection_count and stall_hold_count both one, the first
// surface of an unchanged screen is a surface, the second demands an
// inspection, and the third also holds; with the defaults, three and three,
// the seventh holds and the sixth does not. Only a task that resolves
// Running with its terminal captured holds: a declared wait, a gate run
// being advanced, a task already held, a task with no live reading, and a
// terminal that could not be captured each stop at the inspection, which is
// the section's "a declared wait is not one ... a worker whose gate run the
// service is advancing is not one either ... a terminal that reads gone or
// Unobserved is section 11's". An absorb is never raised, and no other kind
// reads the count.
func TestAStallHoldsOnlyARunningCapturedWorkerPastBothCounts(t *testing.T) {
	t.Parallel()
	unchanged := wakeOf(t, supervisor.KindTerminalUnchanged)
	for i, want := range []supervisor.Level{supervisor.Surface, supervisor.SurfaceAndInspect, supervisor.SurfaceInspectAndHold, supervisor.SurfaceInspectAndHold} {
		if got := supervisor.Classify(unchanged, proof, elapsed, true, stallAt(i, 1, 1)); got.Level != want {
			t.Errorf("surface %d at counts one and one: got %+v, want %s", i+1, got, want)
		}
	}
	if got := supervisor.Classify(unchanged, proof, elapsed, true, stallAt(5, 3, 3)); got.Level != supervisor.SurfaceAndInspect {
		t.Errorf("the sixth surface at the defaults: got %+v, want an inspection and no hold", got)
	}
	if got := supervisor.Classify(unchanged, proof, elapsed, true, stallAt(6, 3, 3)); got.Level != supervisor.SurfaceInspectAndHold {
		t.Errorf("the seventh surface at the defaults: got %+v, want the hold", got)
	}
	// A Running worker with no evidence of progress still holds past both
	// counts; a task already held, which reads no evidence either, is the
	// loop over the states below, and never holds again.
	if got := supervisor.Classify(unchanged, absent, elapsed, true, stallAt(9, 1, 1)); got.Level != supervisor.SurfaceInspectAndHold {
		t.Errorf("a Running worker with no evidence past both counts: got %+v, want the hold", got)
	}
	for _, state := range taskstate.States() {
		if state == taskstate.Running {
			continue
		}
		st := stallAt(9, 1, 1)
		st.State = state
		if got := supervisor.Classify(unchanged, absent, elapsed, true, st); got.Level != supervisor.SurfaceAndInspect {
			t.Errorf("a task resolving %s past both counts: got %+v, want an inspection and no hold", state, got)
		}
	}
	uncaptured := stallAt(9, 1, 1)
	uncaptured.Captured = false
	if got := supervisor.Classify(unchanged, proof, elapsed, true, uncaptured); got.Level != supervisor.SurfaceAndInspect {
		t.Errorf("a Running task whose terminal was not captured: got %+v, want an inspection and no hold", got)
	}
	if got := supervisor.Classify(unchanged, proof, open, true, stallAt(9, 1, 1)); got.Level != supervisor.Absorb {
		t.Errorf("an absorb past both counts: got %+v, want it left an absorb", got)
	}
	if got := supervisor.Classify(unchanged, proof, elapsed, true, stallAt(1, 1, 0)); got.Level != supervisor.SurfaceInspectAndHold {
		t.Errorf("a hold count of zero: got %+v, want the hold on the first inspecting surface", got)
	}
	for _, row := range supervisor.Kinds() {
		if row.Kind == supervisor.KindTerminalUnchanged {
			continue
		}
		for _, evidence := range []supervisor.Evidence{absent, proof} {
			for _, window := range []supervisor.WindowState{open, elapsed} {
				if got := supervisor.Classify(wakeOf(t, row.Kind), evidence, window, true, stallAt(99, 1, 1)); got.Level == supervisor.SurfaceInspectAndHold {
					t.Errorf("%s evidence=%s window=%s held on the stall count: %+v", row.Kind, evidence, window, got)
				}
			}
		}
	}
}

// The stall hold's key is the one composition every reader parses: a key
// round-trips, the components are the attempt and the surface's epoch and
// number, and a key of another space, with a component missing, extra or
// not a canonical number, or naming a nested task is not a stall hold.
func TestAStallHoldKeyRoundTripsAndNothingElseParses(t *testing.T) {
	t.Parallel()
	h := supervisor.StallHold{Task: "abcdefghijkl", Attempt: 2, Epoch: 7, Surface: 3}
	if got := h.Key(); got != "task/abcdefghijkl/stalled/2/7/3" {
		t.Fatalf("Key = %q", got)
	}
	back, ok := supervisor.ParseStallHold(h.Key())
	if !ok || back != h {
		t.Fatalf("ParseStallHold(%q) = %+v, %v, want %+v", h.Key(), back, ok, h)
	}
	for _, key := range []string{
		"task/abcdefghijkl/stopped/2",
		"task/abcdefghijkl/stalled/2/7",
		"task/abcdefghijkl/stalled/2/7/3/1",
		"task/abcdefghijkl/stalled/2/07/3",
		"task/abcdefghijkl/stalled/2/x/3",
		"task/abcdefghijkl/stalled/2/-1/3",
		"task/a/b/stalled/2/7/3",
		"task//stalled/2/7/3",
		"run/abcdefghijkl/stalled/2/7/3",
	} {
		if got, ok := supervisor.ParseStallHold(key); ok {
			t.Errorf("ParseStallHold(%q) = %+v, want no stall hold", key, got)
		}
	}
}

// OpenStallHolds reads the task's own snapshot: an open hold in the stall
// space naming the task and no run counts, and a resolved one, another
// task's, one naming a run, and an open hold of another space do not, so a
// task held on its stop or a report's decision is not stall held.
func TestOpenStallHoldsReadsOnlyTheTasksOpenStallHolds(t *testing.T) {
	t.Parallel()
	const task = "abcdefghijkl"
	stall := supervisor.StallHold{Task: task, Attempt: 1, Epoch: 2, Surface: 3}
	answered := supervisor.StallHold{Task: task, Attempt: 1, Epoch: 2, Surface: 1}
	other := supervisor.StallHold{Task: "mnopqrstuvwx", Attempt: 1, Epoch: 2, Surface: 3}
	observed := func(holds ...store.Hold) taskstate.Observed {
		return taskstate.Observed{Snapshot: store.TaskSnapshot{Task: store.Task{ID: task}, Holds: holds}}
	}
	open := store.Hold{Key: stall.Key(), TaskID: store.Known(task), Subject: "s"}
	resolved := store.Hold{Key: answered.Key(), TaskID: store.Known(task), Subject: "s", Resolution: store.Known("approve")}
	another := store.Hold{Key: other.Key(), TaskID: store.Known(task), Subject: "s"}
	withRun := store.Hold{Key: stall.Key(), TaskID: store.Known(task), RunID: store.Known("run"), Subject: "s"}
	stopped := store.Hold{Key: "task/" + task + "/stopped/1", TaskID: store.Known(task), Subject: "s"}

	if got := supervisor.OpenStallHolds(observed(resolved, another, withRun, stopped, open)); len(got) != 1 || got[0] != stall {
		t.Fatalf("OpenStallHolds = %+v, want only %+v", got, stall)
	}
	if !supervisor.StallHeld(observed(open)) {
		t.Error("a task with an open stall hold is not stall held")
	}
	if supervisor.StallHeld(observed(resolved, another, withRun, stopped)) {
		t.Error("a task with no open stall hold of its own is stall held")
	}
}

// A hold-opened wake's marker is the one composition: it names the hold's
// key and reads back to it, and a marker of anything else, a run moved to
// held among them, names no hold.
func TestAHoldOpenedMarkerNamesItsHold(t *testing.T) {
	t.Parallel()
	key := supervisor.StallHold{Task: "abcdefghijkl", Attempt: 1, Epoch: 2, Surface: 3}.Key()
	if got, ok := supervisor.HoldOfMarker(supervisor.HoldOpenedMarker(key)); !ok || got != key {
		t.Fatalf("HoldOfMarker(HoldOpenedMarker(%q)) = %q, %v", key, got, ok)
	}
	for _, marker := range []string{"run-held:run:1", "hold:", "kept-work:3", "terminal:abc@1#2"} {
		if got, ok := supervisor.HoldOfMarker(marker); ok {
			t.Errorf("HoldOfMarker(%q) = %q, want no hold", marker, got)
		}
	}
}
