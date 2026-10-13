package service

import (
	"testing"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// reconcileActions is the closed action set the table may answer with, and
// every answer over the derived domain is held to it.
var reconcileActions = map[reconcileAction]bool{reconcileRelaunch: true, reconcileSurface: true, reconcileNothing: true}

// The table decides every row of its domain to exactly one action of the
// closed set, and the domain is derived from taskstate's own enumerations
// rather than listed here, so a state, a source or a liveness added later
// reaches this test with no row written for it. The oracle is PRD section
// 11's table written down independently: relaunch is a dead terminal with
// the work not landed, which is Unknown from the process tier (a crashed
// worker with no run) and Failed; every other state with a dead terminal
// is a worker that handed off or a person's; Live and Unobserved relaunch
// nothing, Unobserved being the ambiguous liveness that is left alone.
func TestTheReconciliationTableDecidesEveryDerivedRow(t *testing.T) {
	states, sources, livenesses := taskstate.States(), taskstate.Sources(), taskstate.Livenesses()
	if len(states) == 0 || len(sources) == 0 || len(livenesses) != 3 {
		t.Fatalf("the derived domain is %d states, %d sources and %d liveness values", len(states), len(sources), len(livenesses))
	}
	relaunchRows := 0
	for _, state := range states {
		for _, source := range sources {
			for _, process := range livenesses {
				got := reconcileRow(state, source, process)
				if !reconcileActions[got] {
					t.Fatalf("(%s, %s, %s) decides %q, which is outside the closed action set", state, source, process, got)
				}
				want := reconcileNothing
				if process == taskstate.Dead && ((state == taskstate.Unknown && source == taskstate.SourceProcess) || state == taskstate.Failed) {
					want = reconcileRelaunch
				}
				if got != want {
					t.Errorf("(%s, %s, %s) decides %s, want %s", state, source, process, got, want)
				}
				if got == reconcileRelaunch {
					relaunchRows++
				}
				if got == reconcileSurface {
					t.Errorf("(%s, %s, %s) surfaces from the table; surface is a decision over the row, never a row", state, source, process)
				}
			}
		}
	}
	// One Unknown row and one Failed row per source, so the oracle above
	// did not degenerate into agreeing with an empty table.
	if want := 1 + len(sources); relaunchRows != want {
		t.Fatalf("the table relaunches %d rows, want %d: Unknown from the process tier and Failed from every source", relaunchRows, want)
	}
}

// The positive controls PRD section 11's critique asked for by name: a dead
// terminal in each of the states a handed-off or delivered worker leaves
// relaunches nothing, from the source each is resolved from.
func TestADeadTerminalInAHandedOffStateIsLeftAlone(t *testing.T) {
	for _, row := range []struct {
		state  taskstate.State
		source taskstate.Source
	}{
		{taskstate.Validating, taskstate.SourceRun},
		{taskstate.Ready, taskstate.SourceRun},
		{taskstate.Reported, taskstate.SourceEvent},
		{taskstate.Held, taskstate.SourceRun},
		{taskstate.Landed, taskstate.SourceRecord},
		{taskstate.Complete, taskstate.SourceRecord},
		{taskstate.Queued, taskstate.SourceEvent},
	} {
		if got := reconcileRow(row.state, row.source, taskstate.Dead); got != reconcileNothing {
			t.Errorf("(%s, %s, dead) decides %s, want nothing", row.state, row.source, got)
		}
	}
	// And the control for the controls: the two rows that do relaunch.
	if got := reconcileRow(taskstate.Unknown, taskstate.SourceProcess, taskstate.Dead); got != reconcileRelaunch {
		t.Errorf("a crashed worker with no run decides %s, want relaunch", got)
	}
	if got := reconcileRow(taskstate.Failed, taskstate.SourceRun, taskstate.Dead); got != reconcileRelaunch {
		t.Errorf("a failed run with a dead terminal decides %s, want relaunch", got)
	}
}

// The decision over a relaunch row: within the bound it relaunches, at the
// bound it surfaces naming the bound, and the bound counts relaunches and
// not attempts, so the first attempt is free. A bound of zero surfaces the
// first dead terminal, which is the floor an operator may choose. A Failed
// decided by the isolation assertion's own refusal surfaces whatever the
// count, read off Basis.EventKind.
func TestTheRelaunchBoundCountsRelaunchesFromAttemptRows(t *testing.T) {
	dead := taskstate.Resolution{State: taskstate.Failed, Source: taskstate.SourceRun, Basis: taskstate.Basis{Process: taskstate.Dead}}
	attempts := func(n int) []store.TaskAttempt {
		out := make([]store.TaskAttempt, n)
		for i := range out {
			out[i].Sequence = int64(i + 1)
		}
		return out
	}
	for _, row := range []struct {
		attempts, bound int
		want            reconcileAction
		surfaced        string
	}{
		{1, 2, reconcileRelaunch, ""},
		{2, 2, reconcileRelaunch, ""},
		{3, 2, reconcileSurface, surfacedRelaunchBound},
		{1, 0, reconcileSurface, surfacedRelaunchBound},
		{0, 0, reconcileSurface, surfacedRelaunchBound},
		{0, 1, reconcileRelaunch, ""},
	} {
		got := decideReconciliation(dead, attempts(row.attempts), row.bound)
		if got.action != row.want || got.surfaced != row.surfaced {
			t.Errorf("%d attempts under a bound of %d decide %+v, want %s %q", row.attempts, row.bound, got, row.want, row.surfaced)
		}
		if got.reason == "" {
			t.Errorf("%d attempts under a bound of %d decide with no reason", row.attempts, row.bound)
		}
	}
	refused := dead
	refused.Source, refused.Basis.EventKind = taskstate.SourceEvent, isolationRefusedKind
	if got := decideReconciliation(refused, attempts(1), 2); got.action != reconcileSurface || got.surfaced != surfacedIsolationRefused {
		t.Errorf("a Failed decided by the isolation refusal decides %+v, want a surface naming the refusal", got)
	}
	live := dead
	live.Basis.Process = taskstate.Live
	if got := decideReconciliation(live, attempts(5), 2); got.action != reconcileNothing || got.surfaced != "" {
		t.Errorf("a live terminal past the bound decides %+v, want nothing: the bound is read only on a relaunch row", got)
	}
}

// A superseded wake is a terminal-gone row of the task whose marker begins
// with the replaced session's reference; a row of another task, another
// kind, or a marker composed another way is not.
func TestASupersededWakeIsMatchedByTaskKindAndReference(t *testing.T) {
	const ref = "terminal1:0123456789abcdef0123456789abcdef:taskaaaaaaaa:42:7"
	gone := store.Wake{TaskID: "taskaaaaaaaa", Kind: "terminal-gone", Marker: terminalGoneMarker(ref, "died")}
	if !supersededBy(gone, "taskaaaaaaaa", ref) {
		t.Fatal("the row written for the replaced session is not matched")
	}
	for name, w := range map[string]store.Wake{
		"another task":  {TaskID: "taskbbbbbbbb", Kind: "terminal-gone", Marker: terminalGoneMarker(ref, "died")},
		"another kind":  {TaskID: "taskaaaaaaaa", Kind: "hold-opened", Marker: terminalGoneMarker(ref, "died")},
		"another ref":   {TaskID: "taskaaaaaaaa", Kind: "terminal-gone", Marker: terminalGoneMarker(ref+"1", "died")},
		"ref not first": {TaskID: "taskaaaaaaaa", Kind: "terminal-gone", Marker: "died:" + ref},
	} {
		if supersededBy(w, "taskaaaaaaaa", ref) {
			t.Errorf("%s is matched as superseded: %+v", name, w)
		}
	}
}
