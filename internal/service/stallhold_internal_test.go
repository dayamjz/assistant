package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// stallConfig is PRD section 13's stalled-worker row's setting: a short
// escalation_window, at its floor, which the injected clock passes in one
// step, and stall_inspection_count and stall_hold_count both one, so the
// second surface of an unchanged screen inspects and the third holds.
const stallConfig = `{"supervision_beacon_interval": "1s", "escalation_window": "1m",
	"recheck_window": "1m", "stall_inspection_count": 1, "stall_hold_count": 1}` + "\n"

// stallStart is the instant the injected clock starts at.
var stallStart = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

// stallSubject is a loop over one planted Running task whose scripted
// terminal is live on an unchanged screen.
type stallSubject struct {
	*loopSubject
	terminals *scriptedTerminals
	task      string
	ref       string
}

// openStall plants a Running task with a live, unchanged scripted terminal,
// runs plant for anything else the test needs in the records, and opens the
// loop under stallConfig and the injected clock, past its baseline pass.
func openStall(t *testing.T, id string, plant func(records *store.Store)) *stallSubject {
	t.Helper()
	h, records := loopHomeConfigured(t, stallConfig)
	ref := "pane-" + id
	task := plantRunning(t, records, id, ref).ID
	if plant != nil {
		plant(records)
	}
	terminals := newScriptedTerminals()
	terminals.live(ref, "thinking\n")
	l := openLoop(t, h, records, terminals, &fakeClock{now: stallStart})
	t.Cleanup(func() { _ = l.service.Close() })
	l.nextPass(t)
	return &stallSubject{loopSubject: l, terminals: terminals, task: task, ref: ref}
}

// window moves the clock one escalation window on and kicks one pass.
func (s *stallSubject) window(t *testing.T) passReport {
	t.Helper()
	s.clock.Advance(time.Minute)
	return s.pass(t)
}

// unchanged is the task's terminal-unchanged verdict in a pass, which a test
// of a stall expects exactly one of.
func (s *stallSubject) unchanged(t *testing.T, r passReport) supervisor.Verdict {
	t.Helper()
	got := detectionsOf(r, s.task, supervisor.KindTerminalUnchanged)
	if len(got) != 1 {
		t.Fatalf("the pass made %d terminal-unchanged detections for the task, want one: %+v", len(got), r.Tasks[s.task])
	}
	return got[0].Verdict
}

// stallHolds is every stall hold naming the task, open or resolved, as the
// record holds them.
func (s *stallSubject) stallHolds(t *testing.T) []store.Hold {
	t.Helper()
	snap, err := s.records(t).TaskSnapshot(t.Context(), s.task)
	if err != nil {
		t.Fatalf("reading task %s: %v", s.task, err)
	}
	var out []store.Hold
	for _, h := range snap.Holds {
		if _, ok := supervisor.ParseStallHold(h.Key); ok {
			out = append(out, h)
		}
	}
	return out
}

// toHold drives the stall to its hold: the first surface, the second, which
// inspects, and the third, which holds, asserting each level and that no
// hold exists before the third. It returns the hold.
func (s *stallSubject) toHold(t *testing.T) store.Hold {
	t.Helper()
	for i, want := range []supervisor.Level{supervisor.Surface, supervisor.SurfaceAndInspect} {
		if got := s.unchanged(t, s.window(t)); got.Level != want {
			t.Fatalf("surface %d = %+v, want %s", i+1, got, want)
		}
		if open := s.openStallHolds(t); len(open) != 0 {
			t.Fatalf("surface %d registered %+v, want no hold before the third", i+1, open)
		}
	}
	r := s.window(t)
	if got := s.unchanged(t, r); got.Level != supervisor.SurfaceInspectAndHold {
		t.Fatalf("the third surface = %+v, want the hold", got)
	}
	open := s.openStallHolds(t)
	if len(open) != 1 {
		t.Fatalf("the third surface left %d open stall holds, want exactly one: %+v", len(open), open)
	}
	opened := detectionsOf(r, s.task, supervisor.KindHoldOpened)
	if len(opened) != 1 || opened[0].Marker != supervisor.HoldOpenedMarker(open[0].Key) || opened[0].Disposition != store.WakeAppended {
		t.Fatalf("the hold's wake in the same pass = %+v, want one appended for %s", opened, open[0].Key)
	}
	return open[0]
}

// openStallHolds is the task's open stall holds.
func (s *stallSubject) openStallHolds(t *testing.T) []store.Hold {
	t.Helper()
	var out []store.Hold
	for _, h := range s.stallHolds(t) {
		if h.Open() {
			out = append(out, h)
		}
	}
	return out
}

// acknowledgeAll acknowledges every queued wake about the task, as a
// coordinator that read them does.
func acknowledgeAll(t *testing.T, records *store.Store, task string) {
	t.Helper()
	wakes, err := records.TaskWakes(t.Context(), task)
	if err != nil {
		t.Fatalf("listing the wakes of %s: %v", task, err)
	}
	var ids []int64
	for _, w := range wakes {
		if w.Queued() {
			ids = append(ids, w.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	if err := records.AcknowledgeWakes(t.Context(), ids, store.AcknowledgedByCoordinator()); err != nil {
		t.Fatalf("acknowledging the wakes of %s: %v", task, err)
	}
}

// capturesSince drains the double's capture signals and counts them per
// reference: what each capture call was of, and never a probe, which the
// double does not signal.
func capturesSince(terminals *scriptedTerminals) map[string]int {
	out := make(map[string]int)
	for {
		select {
		case ref := <-terminals.calls:
			out[ref]++
		default:
			return out
		}
	}
}

// PRD section 13's stalled-worker row, the loop's half through the
// Terminals seam: with stall_inspection_count and stall_hold_count both one,
// the first and second surfaces of an unchanged live screen register no
// hold, the second demanding an inspection, and the third registers exactly
// one hold naming the task, keyed by the attempt and that surface, with its
// hold-opened wake in the same pass. The task then resolves Held. With every
// wake acknowledged and no answer, the screen is still captured at every
// later pass, counted by the double's capture calls and never by probes,
// which every pass takes for every task; its unchanged screen goes on
// surfacing once per window; and no second hold is registered. The control
// shows the capture count can fail: a second task held on a stop's hold,
// with its wakes acknowledged, sits in the same loop over the same passes
// and is captured at none.
func TestAStalledWorkerHoldsOnTheThirdSurfaceAndStaysWatched(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	const otherRef = "pane-heldothertsk"
	var other string
	s := openStall(t, "stalledworkr", func(records *store.Store) {
		other = plantRunning(t, records, "heldothertsk", otherRef).ID
		if _, err := records.RegisterHold(t.Context(), store.Hold{
			Key: stoppedKey(other, 1), TaskID: store.Known(other), Subject: "whether to begin this work again",
		}); err != nil {
			t.Fatalf("holding the control task: %v", err)
		}
	})
	s.terminals.live(otherRef, "idle\n")
	hold := s.toHold(t)
	want := supervisor.StallHold{Task: s.task, Attempt: 1, Epoch: 1, Surface: 3}.Key()
	if hold.Key != want || hold.TaskID.Or("") != s.task || hold.RunID.IsKnown() {
		t.Fatalf("the hold = %+v, want %s naming the task and no run", hold, want)
	}
	// The line a person reads about the hold as registered, subject
	// included, carries none of the owners' words (P13).
	vocabtest.Control(t)
	vocabtest.Assert(t, machine.HoldPhrase(hold).String())
	if got := s.observedTask(t, s.task); got.State != taskstate.Held || got.Basis.Hold != hold.Key {
		t.Fatalf("the held task resolves %+v, want Held on %s", got, hold.Key)
	}

	acknowledgeAll(t, s.records(t), s.task)
	acknowledgeAll(t, s.records(t), other)
	capturesSince(s.terminals)
	for i := 0; i < 3; i++ {
		s.clock.Advance(20 * time.Second)
		r := s.pass(t)
		captured := capturesSince(s.terminals)
		if captured[s.ref] != 1 {
			t.Fatalf("pass %d captured the stall-held terminal %d times, want once", i, captured[s.ref])
		}
		if captured[otherRef] != 0 {
			t.Fatalf("pass %d captured the terminal of the task held on its stop, which nothing keeps watched", i)
		}
		if !r.Tasks[s.task].Watched || r.Tasks[other].Watched {
			t.Fatalf("pass %d watched the stall-held task %v and the stop-held one %v, want the first only",
				i, r.Tasks[s.task].Watched, r.Tasks[other].Watched)
		}
	}
	// A window since the last surface: the held screen surfaces again, still
	// as an inspection and never as a second hold.
	s.clock.Advance(time.Minute)
	if got := s.unchanged(t, s.pass(t)); got.Level != supervisor.SurfaceAndInspect {
		t.Fatalf("the held screen past a window = %+v, want it surfaced as an inspection", got)
	}
	if all := s.stallHolds(t); len(all) != 1 || !all[0].Open() {
		t.Fatalf("the stall holds after the held surfaces = %+v, want the one, open", all)
	}
	if obs := s.terminals.Probe(t.Context(), s.ref); obs.Liveness != taskstate.Live {
		t.Fatalf("the worker reads %s after the hold, want it live", obs.Liveness)
	}
}

// PRD section 6's "what a stall is", through the loop: the same stand-in
// raises no hold when its screen changes before the third surface, when it
// has declared a wait, when a run attributed to its branch is being advanced,
// and when its terminal reads Unobserved, over enough windows that the
// stalled worker above holds twice over. Each case reads what the loop
// surfaced, so it is the hold and not the surfacing that is absent.
func TestAStallRaisesNoHoldOutsideARunningLiveUnchangedWorker(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	t.Run("the screen changes before the third surface", func(t *testing.T) {
		t.Parallel()
		s := openStall(t, "changedscrnn", nil)
		s.window(t)
		s.window(t)
		s.terminals.live(s.ref, "thinking harder\n")
		if r := s.pass(t); len(detectionsOf(r, s.task, supervisor.KindTerminalUnchanged)) != 0 {
			t.Fatalf("the changed screen made a terminal-unchanged detection: %+v", r.Tasks[s.task])
		}
		for i, want := range []supervisor.Level{supervisor.Surface, supervisor.SurfaceAndInspect} {
			if got := s.unchanged(t, s.window(t)); got.Level != want {
				t.Fatalf("surface %d of the new count = %+v, want %s", i+1, got, want)
			}
		}
		if all := s.stallHolds(t); len(all) != 0 {
			t.Fatalf("a screen that changed before its third surface held: %+v", all)
		}
	})
	t.Run("a declared wait", func(t *testing.T) {
		t.Parallel()
		s := openStall(t, "declaredwait", func(records *store.Store) {})
		if _, err := s.records(t).AppendTaskEvent(t.Context(), s.task, "waiting-external", waitingDetail); err != nil {
			t.Fatalf("declaring the wait: %v", err)
		}
		surfaced := 0
		for i := 0; i < 6; i++ {
			r := s.window(t)
			for _, v := range verdictsOf(r, s.task, supervisor.KindDeclaredWait) {
				if v.Surfaces() {
					surfaced++
				}
			}
		}
		if surfaced == 0 {
			t.Fatal("the declared wait never surfaced, so the case says nothing about holding")
		}
		if all := s.stallHolds(t); len(all) != 0 {
			t.Fatalf("a declared wait held: %+v", all)
		}
	})
	t.Run("a run attributed to its branch is being advanced", func(t *testing.T) {
		t.Parallel()
		var run string
		s := openStall(t, "advancedrunn", func(records *store.Store) {
			run = plantAttributedRunningRun(t, records, "stalledadvanced")
		})
		if err := s.records(t).SetTaskRun(t.Context(), s.task, run); err != nil {
			t.Fatalf("attributing the run: %v", err)
		}
		if err := s.service.claim(run, func() {}, nil); err != nil {
			t.Fatalf("claiming the run's slot: %v", err)
		}
		if got := s.observedTask(t, s.task); got.State != taskstate.Validating {
			t.Fatalf("the task with its run advancing resolves %s, want Validating", got.State)
		}
		surfaced := 0
		for i := 0; i < 6; i++ {
			if s.unchanged(t, s.window(t)).Surfaces() {
				surfaced++
			}
		}
		if surfaced == 0 {
			t.Fatal("the unchanged screen never surfaced, so the case says nothing about holding")
		}
		if all := s.stallHolds(t); len(all) != 0 {
			t.Fatalf("a worker whose run is being advanced held: %+v", all)
		}
	})
	t.Run("its terminal reads Unobserved", func(t *testing.T) {
		t.Parallel()
		s := openStall(t, "unobservedtk", nil)
		s.terminals.mu.Lock()
		delete(s.terminals.liveness, s.ref)
		s.terminals.mu.Unlock()
		gone := 0
		for i := 0; i < 6; i++ {
			gone += len(detectionsOf(s.window(t), s.task, supervisor.KindTerminalGone))
		}
		if gone == 0 {
			t.Fatal("the unobserved terminal never surfaced as gone, so the case says nothing about holding")
		}
		if all := s.stallHolds(t); len(all) != 0 {
			t.Fatalf("an unobserved terminal held: %+v", all)
		}
	})
}

// plantAttributedRunningRun records a repository and a running run on it,
// for a task to be attributed, and returns the run.
func plantAttributedRunningRun(t *testing.T, records *store.Store, id string) string {
	t.Helper()
	ctx := t.Context()
	if _, err := records.UpsertRepository(ctx, store.Repository{
		ID: "subject", WorkingPath: "/checkouts/one", UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	build, _ := store.CurrentBuild()
	run, err := records.CreateRun(ctx, store.Run{
		ID: id, RepositoryID: "subject", Branch: "work", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", Build: build, ConfigDigest: "cfg",
	})
	if err != nil {
		t.Fatalf("creating the run: %v", err)
	}
	if _, err := records.TransitionRun(ctx, run.ID, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("moving the run to running: %v", err)
	}
	return run.ID
}

// PRD section 6's "a stall that ends by itself closes the hold", through the
// loop with the hold open: the worker read dead surfaces terminal gone and
// the program closes the hold, naming itself and what it read, with no wake
// of its own; the screen read changed closes it the same way, surfaces the
// closure once and restarts the count, so the next hold comes only on the
// third surface of the new count, under a new key; and the terminal read
// Unobserved leaves it open, which is the control that a pass over a held
// stall closes nothing by itself.
func TestAnOpenStallHoldClosesWhenTheStallEndsByItself(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	t.Run("the worker reads dead", func(t *testing.T) {
		t.Parallel()
		s := openStall(t, "deadstalledw", nil)
		hold := s.toHold(t)
		s.terminals.dead(s.ref)
		r := s.pass(t)
		if gone := detectionsOf(r, s.task, supervisor.KindTerminalGone); len(gone) != 1 || !gone[0].Verdict.Surfaces() {
			t.Fatalf("the dead worker = %+v, want terminal gone surfaced", gone)
		}
		if closed := detectionsOf(r, s.task, supervisor.KindStallClosed); len(closed) != 0 {
			t.Fatalf("a closure on the worker read dead surfaced a wake of its own: %+v", closed)
		}
		assertClosedByProgram(t, s, hold.Key, supervisor.StallClosedWorkerDead, s.ref)
	})
	t.Run("the screen changes", func(t *testing.T) {
		t.Parallel()
		s := openStall(t, "movedonworkr", nil)
		hold := s.toHold(t)
		s.terminals.live(s.ref, "moving again\n")
		r := s.pass(t)
		closed := detectionsOf(r, s.task, supervisor.KindStallClosed)
		if len(closed) != 1 || closed[0].Marker != stallClosedMarker(hold.Key) || closed[0].Disposition != store.WakeAppended {
			t.Fatalf("the closure's wake = %+v, want one appended for %s", closed, hold.Key)
		}
		assertClosedByProgram(t, s, hold.Key, supervisor.StallClosedScreenChanged, "screen changed")
		if m, _, err := s.records(t).WakeMarkers(t.Context(), s.task); err != nil || m.StallRepetitions != 0 {
			t.Fatalf("the markers after the change = %+v (%v), want the count restarted", m, err)
		}
		if again := detectionsOf(s.pass(t), s.task, supervisor.KindStallClosed); len(again) != 0 {
			t.Fatalf("the closure surfaced again: %+v", again)
		}
		if rows := wakesOfKind(t, s.records(t), s.task, supervisor.KindStallClosed); len(rows) != 1 {
			t.Fatalf("the closure's wake rows = %+v, want one", rows)
		}
		next := s.toHold(t)
		if next.Key == hold.Key {
			t.Fatalf("the stall after the change held on the closed hold's key %s", hold.Key)
		}
	})
	t.Run("the terminal reads Unobserved", func(t *testing.T) {
		t.Parallel()
		s := openStall(t, "unobservedhd", nil)
		hold := s.toHold(t)
		s.terminals.mu.Lock()
		delete(s.terminals.liveness, s.ref)
		s.terminals.mu.Unlock()
		for i := 0; i < 3; i++ {
			s.window(t)
		}
		if got, err := s.records(t).Hold(t.Context(), hold.Key); err != nil || !got.Open() {
			t.Fatalf("an Unobserved terminal closed the stall hold: %+v (%v)", got, err)
		}
	})
}

// assertClosedByProgram reads the hold and holds it to a closure the program
// recorded: the word as the resolution, the program as the resolver, and a
// note carrying what was read.
func assertClosedByProgram(t *testing.T, s *stallSubject, key, word, read string) {
	t.Helper()
	got, err := s.records(t).Hold(t.Context(), key)
	if err != nil {
		t.Fatalf("reading the hold: %v", err)
	}
	by, known := got.ResolvedBy.Get()
	if got.Open() || got.Resolution.Or("") != word || !known || by != store.ResolvedByProgram() ||
		!strings.Contains(got.ResolutionNote.Or(""), read) {
		t.Fatalf("the hold = %+v, want closed %q by the program with a note naming %q", got, word, read)
	}
	if open := s.openStallHolds(t); len(open) != 0 {
		t.Fatalf("stall holds still open after the closure: %+v", open)
	}
}

// answerStallHold answers the task's one open hold as the coordinator would
// over the socket.
func answerStallHold(t *testing.T, s *Service, task, answer, note string) (machine.TaskAnswered, error) {
	t.Helper()
	return s.respondTask(t.Context(), ipc.Request{}, machine.TaskRespondRequest{Task: task, Answer: answer, Note: note})
}

// PRD section 6's approve: the worker keeps running, the answer is recorded
// on the hold, and the count of the screen starts again from zero, so the
// next surface is a plain one, the one after it inspects, and the next hold
// comes only on the third surface of the new count, under a new key. The
// control for the restart is that third surface: without it every surface
// past the approve would hold at once.
func TestApprovingAStallHoldRestartsTheCount(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	s := openStall(t, "approvedstal", nil)
	hold := s.toHold(t)
	answered, err := answerStallHold(t, s.service, s.task, machine.AnswerApprove, "it is compiling")
	if err != nil {
		t.Fatalf("approve = %v", err)
	}
	if answered.Effect != machine.AnswerRecorded || answered.Hold.Key != hold.Key || answered.Hold.Resolution.Or("") != machine.AnswerApprove {
		t.Fatalf("the approve's answer = %+v, want the hold recorded approve", answered)
	}
	if got := s.observedTask(t, s.task); got.State != taskstate.Running {
		t.Fatalf("the approved task resolves %s, want Running again", got.State)
	}
	for i, want := range []supervisor.Level{supervisor.Surface, supervisor.SurfaceAndInspect} {
		if got := s.unchanged(t, s.window(t)); got.Level != want {
			t.Fatalf("surface %d after the approve = %+v, want %s", i+1, got, want)
		}
	}
	if open := s.openStallHolds(t); len(open) != 0 {
		t.Fatalf("a hold came before the third surface of the new count: %+v", open)
	}
	if got := s.unchanged(t, s.window(t)); got.Level != supervisor.SurfaceInspectAndHold {
		t.Fatalf("the third surface after the approve = %+v, want the hold", got)
	}
	open := s.openStallHolds(t)
	if len(open) != 1 || open[0].Key == hold.Key {
		t.Fatalf("the holds after the new count = %+v, want one new hold beside %s", open, hold.Key)
	}
}

// PRD section 6's answer set is closed: skip, an answer another hold offers,
// and any other word are refused as invalid with the hold left open and
// nothing recorded. The control is approve, which is taken.
func TestAStallHoldRefusesAnyAnswerButTheThree(t *testing.T) {
	t.Parallel()
	s := openStall(t, "refusedanswr", nil)
	hold := s.toHold(t)
	for _, word := range []string{"skip", "land", "keep", "Approve", "retry-now"} {
		_, err := answerStallHold(t, s.service, s.task, word, "")
		if !errors.Is(err, ipc.ErrInvalidRequest) {
			t.Errorf("the answer %q = %v, want refused as invalid", word, err)
		}
		if got, rerr := s.records(t).Hold(t.Context(), hold.Key); rerr != nil || !got.Open() {
			t.Fatalf("the answer %q closed the hold: %+v (%v)", word, got, rerr)
		}
	}
	if _, err := answerStallHold(t, s.service, s.task, machine.AnswerApprove, ""); err != nil {
		t.Fatalf("the control approve = %v", err)
	}
}

// PRD principle P6 at a stall's stop and retry: a worker the probe does not
// read dead after it was asked to end is a typed refusal, the stall hold
// stays open, no stop's hold is registered, and nothing is relaunched, for
// either answer. The ending is the identity path for a worker this service
// holds no session for, as task.stop's own unproven case is. The positive
// control is the same stop once the probe reads dead, which records the
// answer on the stall hold and registers the stop's hold, open.
func TestAStallStopTheProbeDoesNotProveIsRefused(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	h, records := loopHomeConfigured(t, stallConfig)
	ref := session.Reference{Home: session.HomeMarker(h), Task: "unprovenstal", PID: 1 << 22, Start: 7}.String()
	task := plantRunning(t, records, "unprovenstal", ref).ID
	terminals := newScriptedTerminals()
	terminals.live(ref, "thinking\n")
	l := openLoop(t, h, records, terminals, &fakeClock{now: stallStart})
	t.Cleanup(func() { _ = l.service.Close() })
	l.nextPass(t)
	s := &stallSubject{loopSubject: l, terminals: terminals, task: task, ref: ref}
	hold := s.toHold(t)

	for _, word := range []string{machine.AnswerStop, machine.AnswerRetry} {
		_, err := answerStallHold(t, l.service, task, word, "")
		var unverified *stopUnverified
		if !errors.As(err, &unverified) || !errors.Is(err, ipc.ErrRefused) {
			t.Fatalf("%s with the worker still live = %v, want a refused *stopUnverified", word, err)
		}
		if got, rerr := l.records(t).Hold(t.Context(), hold.Key); rerr != nil || !got.Open() {
			t.Fatalf("an unproven %s closed the stall hold: %+v (%v)", word, got, rerr)
		}
		if _, rerr := l.records(t).Hold(t.Context(), stoppedKey(task, 1)); !errors.Is(rerr, store.ErrNotFound) {
			t.Fatalf("an unproven %s registered a stop's hold (%v)", word, rerr)
		}
		if attempts, rerr := l.records(t).TaskAttempts(t.Context(), task); rerr != nil || len(attempts) != 1 {
			t.Fatalf("an unproven %s left attempts %+v (%v), want the one", word, attempts, rerr)
		}
	}

	terminals.dead(ref)
	answered, err := answerStallHold(t, l.service, task, machine.AnswerStop, "")
	if err != nil {
		t.Fatalf("the control stop = %v", err)
	}
	if answered.Effect != machine.AnswerWorkerStopped || answered.Hold.Resolution.Or("") != machine.AnswerStop ||
		answered.Stopped == nil || answered.Stopped.Key != stoppedKey(task, 1) || !answered.Stopped.Open() {
		t.Fatalf("the control stop's answer = %+v, want the stall hold answered stop and the stop's hold open", answered)
	}
	if got, err := l.records(t).Hold(t.Context(), stoppedKey(task, 1)); err != nil || !got.Open() {
		t.Fatalf("the stop's hold = %+v (%v), want it open", got, err)
	}
}

// The closure and an answer are each one write to the hold, serialized by
// the task's slot: a pass that reads the screen changed while an answer
// holds the slot closes nothing that pass, and the pass after it, whose
// capture matches the markers that pass advanced, still reads the stall
// ended, by the change epoch the hold was registered at, and closes it then,
// surfacing the closure once. The control is the same pass with the slot
// free, which TestAnOpenStallHoldClosesWhenTheStallEndsByItself makes.
func TestAStallClosureDeferredForTheSlotLandsAtTheNextPass(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	s := openStall(t, "deferredclos", nil)
	hold := s.toHold(t)
	release, err := s.service.claimSteer(s.task)
	if err != nil {
		t.Fatalf("taking the task's slot: %v", err)
	}
	s.terminals.live(s.ref, "moving again\n")
	if closed := detectionsOf(s.pass(t), s.task, supervisor.KindStallClosed); len(closed) != 0 {
		t.Fatalf("a pass without the slot surfaced a closure: %+v", closed)
	}
	if got, err := s.records(t).Hold(t.Context(), hold.Key); err != nil || !got.Open() {
		t.Fatalf("a pass without the slot closed the hold: %+v (%v)", got, err)
	}
	release()
	closed := detectionsOf(s.pass(t), s.task, supervisor.KindStallClosed)
	if len(closed) != 1 || closed[0].Disposition != store.WakeAppended {
		t.Fatalf("the pass after the slot was released surfaced %+v, want the closure once", closed)
	}
	assertClosedByProgram(t, s, hold.Key, supervisor.StallClosedScreenChanged, "screen changed")
}

// PRD section 6: every end section 8 lists still applies while the hold is
// open, a stop among them, and any probe the program takes that reads the
// worker dead closes the hold. A task.stop with the stall hold open ends the
// worker, and its own probe reading dead closes the stall hold as the
// program, naming the stop, before any pass reads it, while the stop
// registers its own hold as it always does.
func TestATaskStopWithAStallHoldOpenClosesItOnItsDeadReading(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	h, records := loopHomeConfigured(t, stallConfig)
	ref := session.Reference{Home: session.HomeMarker(h), Task: "stopwhilestl", PID: 1 << 22, Start: 7}.String()
	task := plantRunning(t, records, "stopwhilestl", ref).ID
	terminals := newScriptedTerminals()
	terminals.live(ref, "thinking\n")
	l := openLoop(t, h, records, terminals, &fakeClock{now: stallStart})
	t.Cleanup(func() { _ = l.service.Close() })
	l.nextPass(t)
	s := &stallSubject{loopSubject: l, terminals: terminals, task: task, ref: ref}
	hold := s.toHold(t)

	terminals.dead(ref)
	stopped, err := l.service.stopTask(t.Context(), ipc.Request{}, machine.TaskRequest{Task: task})
	if err != nil || stopped.Hold.Key != stoppedKey(task, 1) || !stopped.Hold.Open() {
		t.Fatalf("task.stop = %+v (%v), want the stop's hold for attempt 1", stopped, err)
	}
	assertClosedByProgram(t, s, hold.Key, supervisor.StallClosedWorkerDead, "a stop's probe")
}

// PRD section 6: an answer and the program's closure are each one write to
// the hold and the first to land stands, so a later answer is refused, and
// a stop or retry ends nothing it may not record. An approve that arrives
// while a stop or retry is ending the worker, after that answer read the
// hold open, is refused, and the hold reads the answer that ended the worker.
// The window is held open by the seam between the stop's proven end and its
// write, which is where an approve that skipped the task's slot committed and
// left the hold reading approve over a worker the stop had ended.
func TestAnApproveDuringAStallStopIsRefused(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	for _, word := range []string{machine.AnswerStop, machine.AnswerRetry} {
		t.Run(word, func(t *testing.T) {
			t.Parallel()
			h, records := loopHomeConfigured(t, stallConfig)
			task := "racestall" + map[string]string{machine.AnswerStop: "stp", machine.AnswerRetry: "rty"}[word]
			ref := session.Reference{Home: session.HomeMarker(h), Task: task, PID: 1 << 22, Start: 7}.String()
			plantRunning(t, records, task, ref)
			terminals := newScriptedTerminals()
			terminals.live(ref, "thinking\n")
			l := openLoop(t, h, records, terminals, &fakeClock{now: stallStart})
			t.Cleanup(func() { _ = l.service.Close() })
			l.nextPass(t)
			s := &stallSubject{loopSubject: l, terminals: terminals, task: task, ref: ref}
			hold := s.toHold(t)
			terminals.dead(ref)

			var approveErr error
			approved := false
			l.service.afterStallStopEnded = func() {
				approved = true
				_, approveErr = l.service.respondTask(t.Context(), ipc.Request{},
					machine.TaskRespondRequest{Task: task, Hold: hold.Key, Answer: machine.AnswerApprove})
			}
			answered, err := answerStallHold(t, l.service, task, word, "")
			if !approved {
				t.Fatalf("the %s never reached the window between its end and its write (%v)", word, err)
			}
			if !errors.Is(approveErr, ipc.ErrRefused) {
				t.Fatalf("an approve while the %s ended the worker = %v, want refused", word, approveErr)
			}
			if err != nil {
				t.Fatalf("the %s = %v", word, err)
			}
			got, rerr := l.records(t).Hold(t.Context(), hold.Key)
			if rerr != nil || got.Resolution.Or("") != word || answered.Hold.Resolution.Or("") != word {
				t.Fatalf("the stall hold reads %+v (%v) and the answer %+v, want both %q", got, rerr, answered.Hold, word)
			}
		})
	}
}

// The guard behind the slot: a stop or retry that ended the worker and then
// finds the stall hold already written is refused, the end standing on the
// stop's own hold, and a retry relaunches nothing, unless what landed first
// is the program's closure on the death that stop made, PRD section 6's one
// exception, which reports the end it made. The first write is put into the
// window straight through the store, as a writer that skipped the slot
// would, since no writer in this service can reach it.
func TestAStallStopFindingTheHoldWrittenAfterItsEndKeepsOnlySection6sException(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	cases := []struct {
		name, word, first, task string
		by                      store.Resolver
		refused                 bool
	}{
		{"an approve before a stop's write", machine.AnswerStop, machine.AnswerApprove, "lateapprstop", store.ResolvedByMachineInterface(), true},
		{"an approve before a retry's write", machine.AnswerRetry, machine.AnswerApprove, "lateapprrtry", store.ResolvedByMachineInterface(), true},
		{"the program's closure on the death", machine.AnswerStop, supervisor.StallClosedWorkerDead, "latedeadstop", store.ResolvedByProgram(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, records := loopHomeConfigured(t, stallConfig)
			ref := session.Reference{Home: session.HomeMarker(h), Task: c.task, PID: 1 << 22, Start: 7}.String()
			plantRunning(t, records, c.task, ref)
			terminals := newScriptedTerminals()
			terminals.live(ref, "thinking\n")
			l := openLoop(t, h, records, terminals, &fakeClock{now: stallStart})
			t.Cleanup(func() { _ = l.service.Close() })
			l.nextPass(t)
			s := &stallSubject{loopSubject: l, terminals: terminals, task: c.task, ref: ref}
			hold := s.toHold(t)
			terminals.dead(ref)
			l.service.afterStallStopEnded = func() {
				if _, err := l.records(t).ResolveHoldNoted(t.Context(), hold.Key, c.first, "", c.by); err != nil {
					t.Errorf("writing the hold first: %v", err)
				}
			}
			answered, err := answerStallHold(t, l.service, c.task, c.word, "")
			if c.refused {
				if !errors.Is(err, ipc.ErrRefused) || !errors.Is(err, store.ErrHoldResolved) {
					t.Fatalf("the %s after the hold was answered %q = %+v, %v, want refused", c.word, c.first, answered, err)
				}
			} else if err != nil || answered.Effect != machine.AnswerWorkerStopped || answered.Hold.Resolution.Or("") != c.first {
				t.Fatalf("the %s after the program's closure = %+v, %v, want the end reported over the closure", c.word, answered, err)
			}
			if got, rerr := l.records(t).Hold(t.Context(), stoppedKey(c.task, 1)); rerr != nil || !got.Open() {
				t.Fatalf("the stop's hold = %+v (%v), want it open: the end stands on it", got, rerr)
			}
			if attempts, rerr := l.records(t).TaskAttempts(t.Context(), c.task); rerr != nil || len(attempts) != 1 {
				t.Fatalf("attempts = %+v (%v), want the one: nothing relaunched", attempts, rerr)
			}
		})
	}
}

// The surface that reaches the count is never recorded without its hold,
// whichever write of the pass records it: every surfaced wake of a pass is
// written with the same marker advance, so the first of them to commit
// commits the count. A pass that holds and also surfaces a decision the
// worker raised writes that wake first; the incarnation is replaced right
// after it commits, which refuses every later write of the pass as stale,
// the way a watchdog replacing a wedged loop does. The count the first write
// committed is past the threshold, and the hold must stand beside it.
func TestAHoldingPassRegistersItsHoldWithItsFirstWrite(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	h, records := loopHomeConfigured(t, stallConfig)
	ref := "pane-firstwrite"
	task := plantRunning(t, records, "firstwritetk", ref).ID
	terminals := newScriptedTerminals()
	terminals.live(ref, "thinking\n")
	var l *loopSubject
	replace := false
	l = openLoopHooked(t, h, records, terminals, &fakeClock{now: stallStart}, nil, func(hooks *supervisionHooks) {
		hooks.afterWakeAppend = func(kind supervisor.Kind) {
			if replace && kind == supervisor.KindDecisionEvent {
				replace = false
				if _, err := l.service.store.BeginIncarnation(t.Context()); err != nil {
					t.Errorf("replacing the incarnation: %v", err)
				}
			}
		}
	})
	t.Cleanup(func() { _ = l.service.Close() })
	l.nextPass(t)
	s := &stallSubject{loopSubject: l, terminals: terminals, task: task, ref: ref}
	s.window(t)
	s.window(t)
	if _, err := l.records(t).AppendTaskEvent(t.Context(), task, "decision-open", "which parser to keep"); err != nil {
		t.Fatalf("raising the decision: %v", err)
	}
	if got := s.observedTask(t, task); got.State != taskstate.Running {
		t.Fatalf("with the decision raised the task resolves %s, want Running from its live terminal", got.State)
	}
	replace = true
	s.clock.Advance(time.Minute)
	reply := make(chan passReport, 1)
	l.kick <- reply
	r := <-reply
	if !errors.Is(r.Tasks[task].Err, store.ErrStaleIncarnation) {
		t.Fatalf("the holding pass's writes after the replacement = %v, want refused as stale", r.Tasks[task].Err)
	}
	m, _, err := l.records(t).WakeMarkers(t.Context(), task)
	if err != nil || m.StallRepetitions != 3 {
		t.Fatalf("the markers = %+v (%v), want the third surface committed by the first write", m, err)
	}
	if open := s.openStallHolds(t); len(open) != 1 {
		t.Fatalf("the count is at the third surface with open stall holds %+v, want its hold beside it", open)
	}
}

// PRD section 6: "a relaunched worker's new terminal starts a count of its
// own". Attempt 1 reaches its hold, the hold is answered, and attempt 2
// begins with a session of its own that shows the very screen attempt 1
// showed, which is what two runs of one agent at the same point look like.
// The new session's first surface is a plain one and the hold comes only on
// its third, under attempt 2's key, rather than at once on the count
// attempt 1 left in the markers.
func TestARelaunchedWorkerStartsAStallCountOfItsOwn(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	s := openStall(t, "relaunchedct", nil)
	hold := s.toHold(t)
	if _, err := s.records(t).ResolveHoldNoted(t.Context(), hold.Key, machine.AnswerRetry, "", store.ResolvedByMachineInterface()); err != nil {
		t.Fatalf("answering the hold: %v", err)
	}
	if _, err := s.records(t).BeginTaskAttempt(t.Context(), s.task, 1); err != nil {
		t.Fatalf("beginning attempt 2: %v", err)
	}
	next := "pane-relaunchedct-2"
	if err := s.records(t).SetAttemptSession(t.Context(), s.task, 2, next, store.SessionHost{}); err != nil {
		t.Fatalf("recording attempt 2's session: %v", err)
	}
	s.terminals.dead(s.ref)
	s.terminals.live(next, "thinking\n")
	s.ref = next
	if got := s.observedTask(t, s.task); got.State != taskstate.Running {
		t.Fatalf("the relaunched task resolves %s, want Running from its new terminal", got.State)
	}
	if got := detectionsOf(s.pass(t), s.task, supervisor.KindTerminalUnchanged); len(got) != 0 {
		t.Fatalf("the new session's first reading made a terminal-unchanged detection %+v, want it read as a new screen", got)
	}
	for i, want := range []supervisor.Level{supervisor.Surface, supervisor.SurfaceAndInspect} {
		if got := s.unchanged(t, s.window(t)); got.Level != want {
			t.Fatalf("surface %d of attempt 2 = %+v, want %s", i+1, got, want)
		}
		if open := s.openStallHolds(t); len(open) != 0 {
			t.Fatalf("surface %d of attempt 2 held at once: %+v", i+1, open)
		}
	}
	if got := s.unchanged(t, s.window(t)); got.Level != supervisor.SurfaceInspectAndHold {
		t.Fatalf("the third surface of attempt 2 = %+v, want the hold", got)
	}
	open := s.openStallHolds(t)
	if sh, ok := supervisor.ParseStallHold(open[0].Key); len(open) != 1 || !ok || sh.Attempt != 2 {
		t.Fatalf("attempt 2's holds = %+v, want one keyed by attempt 2", open)
	}
}
