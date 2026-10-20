package service

import (
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// declaredWaitConfig is loopConfig with the recheck window well past the
// escalation window, so a pass can stand past the one and inside the other
// and the two are told apart by which detection a task gets.
const declaredWaitConfig = `{"supervision_beacon_interval": "1s", "escalation_window": "1m",
	"recheck_window": "10m", "stall_inspection_count": 2}` + "\n"

// waitingDetail is the declaration's own text: what the worker said it is
// waiting on.
const waitingDetail = "the upstream API's quota resets at noon"

// plantDeclaredWait plants a task as plantRunning does and then appends the
// worker's own declared external wait, the one kind taskstate reads as
// Paused.
func plantDeclaredWait(t *testing.T, records *store.Store, id, ref string) string {
	t.Helper()
	task := plantRunning(t, records, id, ref).ID
	if _, err := records.AppendTaskEvent(t.Context(), task, "waiting-external", waitingDetail); err != nil {
		t.Fatalf("declaring the wait of %s: %v", task, err)
	}
	return task
}

// verdictsOf is the verdicts of a task's detections of one kind in a pass.
func verdictsOf(r passReport, task string, kind supervisor.Kind) []supervisor.Verdict {
	var out []supervisor.Verdict
	for _, d := range detectionsOf(r, task, kind) {
		out = append(out, d.Verdict)
	}
	return out
}

// PRD section 5's "a declared wait is not a stall", through the real loop
// and its pass over the Terminals seam under an injected clock: a task
// whose worker declared an external wait, and which resolves Paused, gets
// no terminal-unchanged detection on any pass, inside the escalation
// window or past it, and its declared-wait detection absorbs on the open
// recheck window until that window elapses, surfaces once then, and absorbs
// again on the window the surface restarted. Two controls run in the same
// loop over the same passes, so the declaration is what the difference
// rests on. One is the same task without the declaration, which resolves
// Running from its log with nothing to show progress and surfaces terminal
// unchanged on the first unchanged pass, because P9 reads no screen as an
// unchanged one. The other is a Running task whose live terminal is
// unchanged: absorbed on the live probe inside the escalation window and
// surfaced as terminal unchanged once the window has elapsed, which is the
// stall the declaration is the exception to.
//
// The declared task here has no session reference recorded, so its screen
// is never read and the declared-wait branch is reached on the record
// alone. TestALiveWorkersDeclaredWaitIsNotAStall is the same rule for a
// worker whose terminal is live and unchanged.
//
// The pass that first reads the log surfaces the declaration itself as an
// event, the flowchart's event branch with no decision verb and no
// evidence; that is a wake about the declaration, not a stall, and this
// test asserts nothing about it.
func TestADeclaredWaitIsNotAStallAndArmsTheRecheck(t *testing.T) {
	t.Parallel()
	h, records := loopHomeConfigured(t, declaredWaitConfig)
	declared := plantDeclaredWait(t, records, "declaredwait", "")
	undeclared := plantRunning(t, records, "undeclaredaa", "").ID
	live := plantRunning(t, records, "livecontrola", "pane-control").ID
	terminals := newScriptedTerminals()
	terminals.live("pane-control", "compiling\n")
	start := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	l := openLoop(t, h, records, terminals, clock)
	defer func() { _ = l.service.Close() }()
	l.nextPass(t) // the baseline: every screen's first reading

	got, err := l.service.tasks().Task(t.Context(), declared)
	if err != nil || got.State != taskstate.Paused || got.Source != taskstate.SourceEvent || got.Basis.Process != taskstate.Unobserved {
		t.Fatalf("the declared task resolves %+v (%v), want Paused from its log with no terminal observed", got, err)
	}
	if got, err := l.service.tasks().Task(t.Context(), undeclared); err != nil || got.State != taskstate.Running || got.Source != taskstate.SourceEvent {
		t.Fatalf("the undeclared control resolves %+v (%v), want Running from its log", got, err)
	}
	if got := l.observedTask(t, live); got.State != taskstate.Running || got.Source != taskstate.SourceProcess {
		t.Fatalf("the live control resolves %+v, want Running from its live terminal", got)
	}

	// noStall fails the test when a pass raised terminal unchanged for the
	// declared task, which no pass in this test may.
	noStall := func(r passReport, when string) {
		t.Helper()
		if err := r.Tasks[declared].Err; err != nil || !r.Tasks[declared].Watched {
			t.Fatalf("%s the pass over the declared task: watched %v, err %v", when, r.Tasks[declared].Watched, err)
		}
		if got := detectionsOf(r, declared, supervisor.KindTerminalUnchanged); len(got) != 0 {
			t.Fatalf("%s the declared wait raised terminal unchanged %+v: a declared wait is not a stall", when, got)
		}
	}
	waitIs := func(r passReport, when string, want supervisor.Verdict) {
		t.Helper()
		if got := verdictsOf(r, declared, supervisor.KindDeclaredWait); len(got) != 1 || got[0] != want {
			t.Fatalf("%s the declared wait's verdicts = %+v, want exactly %+v", when, got, want)
		}
	}
	windowOpen := supervisor.Verdict{Level: supervisor.Absorb, Reason: supervisor.ReasonWindowOpen}

	// Inside the escalation window.
	clock.Advance(10 * time.Second)
	first := l.pass(t)
	noStall(first, "inside the escalation window")
	waitIs(first, "inside the escalation window", windowOpen)
	if got := verdictsOf(first, undeclared, supervisor.KindTerminalUnchanged); len(got) != 1 ||
		got[0] != (supervisor.Verdict{Level: supervisor.Surface, Reason: supervisor.ReasonNoEvidence}) {
		t.Fatalf("the undeclared control's terminal unchanged = %+v, want surfaced for no evidence", got)
	}
	if got := verdictsOf(first, live, supervisor.KindTerminalUnchanged); len(got) != 1 ||
		got[0] != (supervisor.Verdict{Level: supervisor.Absorb, Reason: supervisor.ReasonProgress}) {
		t.Fatalf("the live control inside the window = %+v, want absorbed on progress", got)
	}

	// Past the escalation window and inside the recheck window: the live
	// control's unchanged screen is a stall now, and the declared wait is
	// still not one.
	clock.Advance(2 * time.Minute)
	second := l.pass(t)
	noStall(second, "past the escalation window")
	waitIs(second, "past the escalation window", windowOpen)
	if got := verdictsOf(second, live, supervisor.KindTerminalUnchanged); len(got) != 1 ||
		got[0] != (supervisor.Verdict{Level: supervisor.Surface, Reason: supervisor.ReasonWindowElapsed}) {
		t.Fatalf("the live control past the escalation window = %+v, want surfaced for the window elapsing", got)
	}
	if rows := wakesOfKind(t, l.records(t), live, supervisor.KindTerminalUnchanged); len(rows) != 1 {
		t.Fatalf("the live control's terminal-unchanged wakes = %+v, want the one stall", rows)
	}

	// Past the recheck window: the forgotten wait surfaces once, and the
	// surface restarts the window, so the next pass absorbs again.
	clock.Advance(8 * time.Minute)
	third := l.pass(t)
	noStall(third, "past the recheck window")
	waitIs(third, "past the recheck window", supervisor.Verdict{Level: supervisor.Surface, Reason: supervisor.ReasonWindowElapsed})
	if d := detectionsOf(third, declared, supervisor.KindDeclaredWait); d[0].Disposition != store.WakeAppended {
		t.Fatalf("the recheck's surface was %q, want appended", d[0].Disposition)
	}
	clock.Advance(time.Minute)
	fourth := l.pass(t)
	noStall(fourth, "after the recheck surfaced")
	waitIs(fourth, "after the recheck surfaced", windowOpen)

	rows := wakesOfKind(t, l.records(t), declared, supervisor.KindDeclaredWait)
	if len(rows) != 1 || !rows[0].Queued() || !strings.HasPrefix(rows[0].Marker, "declared-wait:") {
		t.Fatalf("the declared task's declared-wait wakes = %+v, want the recheck's one queued row", rows)
	}
	if rows := wakesOfKind(t, l.records(t), declared, supervisor.KindTerminalUnchanged); len(rows) != 0 {
		t.Fatalf("the declared task has terminal-unchanged wakes %+v, want none", rows)
	}
}

// PRD principle P9's "absorbing wakes on a timer makes a finished or
// crashed worker invisible", for the one absorb that rests on a timer by
// design: a worker that declared an external wait, and which resolves
// Paused while its terminal is live, is surfaced as terminal gone, as dead,
// on the first pass that probes its terminal Dead, and is no longer read as
// a wait. The task resolves Unknown from the dead terminal rather than
// Paused from the stale declaration, which is the dead-terminal exception
// the owner's decision on #303 keeps, so no pass after the death makes a
// declared-wait detection, and the dead terminal surfaces once and
// is absorbed only for the rest of its escalation window, which
// TestASilentCrashSurfacesAgainOncePerEscalationWindow carries past. Every
// reading is the loop's own pass through the Terminals seam; the double
// states only what the provider's probe answers.
func TestADeadTerminalUnderADeclaredWaitSurfacesTerminalGoneOnce(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P9)
	h, records := loopHomeConfigured(t, declaredWaitConfig)
	task := plantDeclaredWait(t, records, "deadwaitaaaa", "pane-waiting")
	terminals := newScriptedTerminals()
	terminals.live("pane-waiting", "waiting on the upstream\n")
	start := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	l := openLoop(t, h, records, terminals, clock)
	defer func() { _ = l.service.Close() }()
	l.nextPass(t)

	noWait := func(r passReport, when string) {
		t.Helper()
		if got := detectionsOf(r, task, supervisor.KindDeclaredWait); len(got) != 0 {
			t.Fatalf("%s the pass made a declared-wait detection %+v", when, got)
		}
	}
	// While the terminal is live the declaration answers: Paused, and the
	// wait absorbs on its open recheck window.
	clock.Advance(10 * time.Second)
	alive := l.pass(t)
	if got := verdictsOf(alive, task, supervisor.KindDeclaredWait); len(got) != 1 ||
		got[0] != (supervisor.Verdict{Level: supervisor.Absorb, Reason: supervisor.ReasonWindowOpen}) {
		t.Fatalf("the declared wait while the terminal was live = %+v, want absorbed on the open recheck window", got)
	}
	if gone := detectionsOf(alive, task, supervisor.KindTerminalGone); len(gone) != 0 {
		t.Fatalf("a live terminal was read as terminal gone: %+v", gone)
	}

	terminals.dead("pane-waiting")
	clock.Advance(10 * time.Second)
	died := l.pass(t)
	noWait(died, "once the terminal died")
	gone := detectionsOf(died, task, supervisor.KindTerminalGone)
	if len(gone) != 1 || gone[0].Verdict != (supervisor.Verdict{Level: supervisor.Surface, Reason: supervisor.ReasonActionable}) ||
		gone[0].Disposition != store.WakeAppended || !strings.HasPrefix(gone[0].Marker, "terminal-gone:dead:pane-waiting:") {
		t.Fatalf("the dead terminal under a declared wait = %+v, want terminal gone surfaced as dead and appended", gone)
	}
	if got := l.observedTask(t, task); got.State != taskstate.Unknown || got.Source != taskstate.SourceProcess || got.Basis.Process != taskstate.Dead {
		t.Fatalf("the task resolves %+v, want Unknown from the dead terminal and not Paused from the declaration", got)
	}

	// Once: inside the escalation window every later pass absorbs it.
	for i := 0; i < 2; i++ {
		clock.Advance(15 * time.Second)
		r := l.pass(t)
		noWait(r, "after the surface")
		if got := verdictsOf(r, task, supervisor.KindTerminalGone); len(got) != 1 ||
			got[0] != (supervisor.Verdict{Level: supervisor.Absorb, Reason: supervisor.ReasonSurfacedThisWindow}) {
			t.Fatalf("pass %d after the surface = %+v, want absorbed for the window", i, got)
		}
	}
	if rows := wakesOfKind(t, l.records(t), task, supervisor.KindTerminalGone); len(rows) != 1 || !rows[0].Queued() {
		t.Fatalf("the terminal-gone wakes = %+v, want one queued row", rows)
	}
	if rows := wakesOfKind(t, l.records(t), task, supervisor.KindDeclaredWait); len(rows) != 0 {
		t.Fatalf("the dead worker has declared-wait wakes %+v, want none", rows)
	}
}

// PRD section 5's "a declared wait is not a stall" for a worker whose
// terminal is live, the owner's decision on #303 that section 5 outranks
// section 6's live process signal. The worker declared an external wait and
// its screen does not change. The task resolves Paused from its log with
// the terminal still read Live, and on no pass, inside the escalation
// window or past it, does it get terminal unchanged. Its declared-wait
// detection absorbs on the open recheck window and surfaces once when that
// window elapses. The control is the same worker with no declaration and
// the same unchanged live screen in the same loop: it resolves Running
// from the process tier and surfaces terminal unchanged once the
// escalation window elapses, so the declaration is what the difference
// rests on. Then the dead-terminal exception the decision keeps: the
// declared worker's terminal dies, the task resolves Unknown from the dead
// terminal rather than Paused, and the pass surfaces terminal gone as dead
// with no declared-wait detection.
func TestALiveWorkersDeclaredWaitIsNotAStall(t *testing.T) {
	t.Parallel()
	h, records := loopHomeConfigured(t, declaredWaitConfig)
	declared := plantDeclaredWait(t, records, "livewaitaaaa", "pane-declared")
	control := plantRunning(t, records, "livenowaitaa", "pane-undeclared").ID
	terminals := newScriptedTerminals()
	terminals.live("pane-declared", "waiting on the upstream\n")
	terminals.live("pane-undeclared", "waiting on the upstream\n")
	clock := &fakeClock{now: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	l := openLoop(t, h, records, terminals, clock)
	defer func() { _ = l.service.Close() }()
	l.nextPass(t)

	if got := l.observedTask(t, declared); got.State != taskstate.Paused || got.Source != taskstate.SourceEvent || got.Basis.Process != taskstate.Live {
		t.Fatalf("the live declared task resolves %+v, want Paused from its log with the terminal live", got)
	}
	if got := l.observedTask(t, control); got.State != taskstate.Running || got.Source != taskstate.SourceProcess {
		t.Fatalf("the undeclared control resolves %+v, want Running from its live terminal", got)
	}

	noStall := func(r passReport, when string) {
		t.Helper()
		if err := r.Tasks[declared].Err; err != nil || !r.Tasks[declared].Watched {
			t.Fatalf("%s the pass over the declared task: watched %v, err %v", when, r.Tasks[declared].Watched, err)
		}
		if got := detectionsOf(r, declared, supervisor.KindTerminalUnchanged); len(got) != 0 {
			t.Fatalf("%s the live declared wait raised terminal unchanged %+v: a declared wait is not a stall", when, got)
		}
	}
	waitIs := func(r passReport, when string, want supervisor.Verdict) {
		t.Helper()
		if got := verdictsOf(r, declared, supervisor.KindDeclaredWait); len(got) != 1 || got[0] != want {
			t.Fatalf("%s the live declared wait's verdicts = %+v, want exactly %+v", when, got, want)
		}
	}
	windowOpen := supervisor.Verdict{Level: supervisor.Absorb, Reason: supervisor.ReasonWindowOpen}

	clock.Advance(10 * time.Second)
	inside := l.pass(t)
	noStall(inside, "inside the escalation window")
	waitIs(inside, "inside the escalation window", windowOpen)
	if got := verdictsOf(inside, control, supervisor.KindTerminalUnchanged); len(got) != 1 ||
		got[0] != (supervisor.Verdict{Level: supervisor.Absorb, Reason: supervisor.ReasonProgress}) {
		t.Fatalf("the control inside the window = %+v, want absorbed on progress", got)
	}

	clock.Advance(2 * time.Minute)
	past := l.pass(t)
	noStall(past, "past the escalation window")
	waitIs(past, "past the escalation window", windowOpen)
	if got := verdictsOf(past, control, supervisor.KindTerminalUnchanged); len(got) != 1 ||
		got[0] != (supervisor.Verdict{Level: supervisor.Surface, Reason: supervisor.ReasonWindowElapsed}) {
		t.Fatalf("the control past the escalation window = %+v, want terminal unchanged surfaced", got)
	}
	if got := detectionsOf(past, control, supervisor.KindDeclaredWait); len(got) != 0 {
		t.Fatalf("the undeclared control made a declared-wait detection %+v", got)
	}

	clock.Advance(8 * time.Minute)
	recheck := l.pass(t)
	noStall(recheck, "past the recheck window")
	waitIs(recheck, "past the recheck window", supervisor.Verdict{Level: supervisor.Surface, Reason: supervisor.ReasonWindowElapsed})
	if rows := wakesOfKind(t, l.records(t), declared, supervisor.KindDeclaredWait); len(rows) != 1 {
		t.Fatalf("the live declared task's declared-wait wakes = %+v, want the recheck's one", rows)
	}
	if rows := wakesOfKind(t, l.records(t), declared, supervisor.KindTerminalUnchanged); len(rows) != 0 {
		t.Fatalf("the live declared task has terminal-unchanged wakes %+v, want none", rows)
	}

	// The dead-terminal control: the declaration never outranks a dead one.
	terminals.dead("pane-declared")
	clock.Advance(10 * time.Second)
	died := l.pass(t)
	if got := detectionsOf(died, declared, supervisor.KindDeclaredWait); len(got) != 0 {
		t.Fatalf("the dead terminal was read as the declared wait: %+v", got)
	}
	gone := detectionsOf(died, declared, supervisor.KindTerminalGone)
	if len(gone) != 1 || !gone[0].Verdict.Surfaces() || gone[0].Disposition != store.WakeAppended ||
		!strings.HasPrefix(gone[0].Marker, "terminal-gone:dead:pane-declared:") {
		t.Fatalf("the dead terminal under the declared wait = %+v, want terminal gone surfaced as dead", gone)
	}
	if got := l.observedTask(t, declared); got.State != taskstate.Unknown || got.Source != taskstate.SourceProcess {
		t.Fatalf("the declared task with its terminal dead resolves %+v, want Unknown from the dead terminal", got)
	}
}

// PRD principle P9's "absence of evidence surfaces; it never absorbs", for
// a worker that declared a wait and whose terminal then cannot be observed.
// The task resolves Paused, because the process tier is skipped on an
// Unobserved probe and the log answers. The loop surfaces terminal gone
// with the case unobservable, once per escalation window, rather than
// absorbing the silence on the declared wait's recheck window. The
// declaration is the worker's word that it is waiting; an Unobserved probe
// says nothing about whether that worker is still there, so absorbing on it
// would hide a worker lost behind an unheld reference or an unreadable
// terminal for a whole recheck window. Surfacing it is not reading the wait
// as a crash: the case is unobservable and not dead, and PRD section 11's
// ambiguous liveness row leaves such a task alone, which reconcile.go's
// table keeps (no relaunch, no cleanup). This is the guard against moving
// the declared-wait reading ahead of the gone cases, which an unvalidated
// change on wip/overnight-gaps proposed.
func TestAnUnobservedTerminalUnderADeclaredWaitSurfacesAsUnobservable(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P9)
	h, records := loopHomeConfigured(t, declaredWaitConfig)
	// The double holds no handle for this reference, so its probe answers
	// Unobserved with the unheld reason, as a provider does for a survivor
	// of another service's life.
	task := plantDeclaredWait(t, records, "unseenwaitaa", "pane-unheld")
	terminals := newScriptedTerminals()
	clock := &fakeClock{now: time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)}
	l := openLoop(t, h, records, terminals, clock)
	defer func() { _ = l.service.Close() }()
	first := l.nextPass(t)

	got, err := l.service.tasks().Task(t.Context(), task)
	if err != nil || got.State != taskstate.Paused || got.Basis.Process != taskstate.Unobserved {
		t.Fatalf("the task resolves %+v (%v), want Paused from its log with the terminal unobserved", got, err)
	}
	gone := detectionsOf(first, task, supervisor.KindTerminalGone)
	if len(gone) != 1 || !gone[0].Verdict.Surfaces() || gone[0].Disposition != store.WakeAppended ||
		!strings.HasPrefix(gone[0].Marker, "terminal-gone:unobservable:pane-unheld:") {
		t.Fatalf("the unobserved terminal under a declared wait = %+v, want terminal gone surfaced as unobservable", gone)
	}
	if got := detectionsOf(first, task, supervisor.KindDeclaredWait); len(got) != 0 {
		t.Fatalf("the unobserved terminal was absorbed on the declared wait: %+v", got)
	}
	// Once per window: the next pass inside it absorbs, and none reads it
	// as a wait.
	clock.Advance(30 * time.Second)
	next := l.pass(t)
	if got := verdictsOf(next, task, supervisor.KindTerminalGone); len(got) != 1 ||
		got[0] != (supervisor.Verdict{Level: supervisor.Absorb, Reason: supervisor.ReasonSurfacedThisWindow}) {
		t.Fatalf("the pass inside the window = %+v, want absorbed for the window", got)
	}
	if got := detectionsOf(next, task, supervisor.KindDeclaredWait); len(got) != 0 {
		t.Fatalf("a later pass read the unobserved terminal as the declared wait: %+v", got)
	}
	if rows := wakesOfKind(t, l.records(t), task, supervisor.KindTerminalGone); len(rows) != 1 {
		t.Fatalf("the terminal-gone wakes = %+v, want one", rows)
	}
}
