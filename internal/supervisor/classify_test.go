package supervisor_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// classifier is the shape of Classify, so the matrix can be run against a
// counterfeit as well as the real one.
type classifier func(supervisor.Wake, supervisor.Evidence, supervisor.WindowState, bool, supervisor.Stall) supervisor.Verdict

// cell is one coordinate of the matrix.
type cell struct {
	kind     supervisor.Kind
	evidence supervisor.Evidence
	window   supervisor.WindowState
	surfaced bool
}

func (c cell) String() string {
	return fmt.Sprintf("%s evidence=%s window=%s surfaced=%t", c.kind, c.evidence, c.window, c.surfaced)
}

// noStall is a repetition count below any threshold, so a cell that is not
// about repetition is not raised by one.
var noStall = supervisor.Stall{Repetitions: 0, InspectAt: 1}

const (
	absent  = supervisor.NoEvidence
	proof   = supervisor.Progress
	open    = supervisor.WindowOpen
	elapsed = supervisor.WindowElapsed
)

// oracle is PRD section 5's flowchart, written out one cell at a time from
// the document and not from the classifier's table: every edge of the
// flowchart, the "absence of evidence surfaces" rule beneath it, the "never
// read as no change" sentence for terminal gone and a run moved to held, the
// "terminal-ish wait surfaces once" rule with its sentence on a crashed
// worker that stays silent, and the issue's decision that the surfaced flag
// reaches no other kind. Each kind has every combination of
// evidence, window and surfaced, so a kind the table has that this list
// lacks a row for fails rather than passing on nothing.
var oracle = map[cell]supervisor.Level{
	// "authenticated check result" -> Surface: always actionable. The
	// evidence, the window and the flag change nothing.
	{supervisor.KindCheckResult, absent, open, false}:    supervisor.Surface,
	{supervisor.KindCheckResult, absent, open, true}:     supervisor.Surface,
	{supervisor.KindCheckResult, absent, elapsed, false}: supervisor.Surface,
	{supervisor.KindCheckResult, absent, elapsed, true}:  supervisor.Surface,
	{supervisor.KindCheckResult, proof, open, false}:     supervisor.Surface,
	{supervisor.KindCheckResult, proof, open, true}:      supervisor.Surface,
	{supervisor.KindCheckResult, proof, elapsed, false}:  supervisor.Surface,
	{supervisor.KindCheckResult, proof, elapsed, true}:   supervisor.Surface,

	// "event with a decision verb" -> Surface: always actionable.
	{supervisor.KindDecisionEvent, absent, open, false}:    supervisor.Surface,
	{supervisor.KindDecisionEvent, absent, open, true}:     supervisor.Surface,
	{supervisor.KindDecisionEvent, absent, elapsed, false}: supervisor.Surface,
	{supervisor.KindDecisionEvent, absent, elapsed, true}:  supervisor.Surface,
	{supervisor.KindDecisionEvent, proof, open, false}:     supervisor.Surface,
	{supervisor.KindDecisionEvent, proof, open, true}:      supervisor.Surface,
	{supervisor.KindDecisionEvent, proof, elapsed, false}:  supervisor.Surface,
	{supervisor.KindDecisionEvent, proof, elapsed, true}:   supervisor.Surface,

	// "event with no decision verb" -> positive evidence? yes: absorb, no:
	// surface. No window applies, and the flag absorbs nothing here.
	{supervisor.KindPlainEvent, absent, open, false}:    supervisor.Surface,
	{supervisor.KindPlainEvent, absent, open, true}:     supervisor.Surface,
	{supervisor.KindPlainEvent, absent, elapsed, false}: supervisor.Surface,
	{supervisor.KindPlainEvent, absent, elapsed, true}:  supervisor.Surface,
	{supervisor.KindPlainEvent, proof, open, false}:     supervisor.Absorb,
	{supervisor.KindPlainEvent, proof, open, true}:      supervisor.Absorb,
	{supervisor.KindPlainEvent, proof, elapsed, false}:  supervisor.Absorb,
	{supervisor.KindPlainEvent, proof, elapsed, true}:   supervisor.Absorb,

	// "terminal unchanged" -> positive evidence? no: surface; yes: idle past
	// the escalation window? no: absorb, yes: surface. Then "a terminal-ish
	// wait surfaces once, then is absorbed even as its idle screen redraws":
	// no evidence with the flag set absorbs while the window is open, and
	// surfaces once more when it elapses.
	{supervisor.KindTerminalUnchanged, absent, open, false}:    supervisor.Surface,
	{supervisor.KindTerminalUnchanged, absent, open, true}:     supervisor.Absorb,
	{supervisor.KindTerminalUnchanged, absent, elapsed, false}: supervisor.Surface,
	{supervisor.KindTerminalUnchanged, absent, elapsed, true}:  supervisor.Surface,
	{supervisor.KindTerminalUnchanged, proof, open, false}:     supervisor.Absorb,
	{supervisor.KindTerminalUnchanged, proof, open, true}:      supervisor.Absorb,
	{supervisor.KindTerminalUnchanged, proof, elapsed, false}:  supervisor.Surface,
	{supervisor.KindTerminalUnchanged, proof, elapsed, true}:   supervisor.Surface,

	// "declared external wait" -> idle past the recheck window? no: absorb,
	// yes: surface. The flowchart reads no evidence on this edge: "a
	// declared wait is not a stall".
	{supervisor.KindDeclaredWait, absent, open, false}:    supervisor.Absorb,
	{supervisor.KindDeclaredWait, absent, open, true}:     supervisor.Absorb,
	{supervisor.KindDeclaredWait, absent, elapsed, false}: supervisor.Surface,
	{supervisor.KindDeclaredWait, absent, elapsed, true}:  supervisor.Surface,
	{supervisor.KindDeclaredWait, proof, open, false}:     supervisor.Absorb,
	{supervisor.KindDeclaredWait, proof, open, true}:      supervisor.Absorb,
	{supervisor.KindDeclaredWait, proof, elapsed, false}:  supervisor.Surface,
	{supervisor.KindDeclaredWait, proof, elapsed, true}:   supervisor.Surface,

	// "no change" -> Absorb: advance markers, no model turn.
	{supervisor.KindNoChange, absent, open, false}:    supervisor.Absorb,
	{supervisor.KindNoChange, absent, open, true}:     supervisor.Absorb,
	{supervisor.KindNoChange, absent, elapsed, false}: supervisor.Absorb,
	{supervisor.KindNoChange, absent, elapsed, true}:  supervisor.Absorb,
	{supervisor.KindNoChange, proof, open, false}:     supervisor.Absorb,
	{supervisor.KindNoChange, proof, open, true}:      supervisor.Absorb,
	{supervisor.KindNoChange, proof, elapsed, false}:  supervisor.Absorb,
	{supervisor.KindNoChange, proof, elapsed, true}:   supervisor.Absorb,

	// "terminal gone" -> Surface, "never read as no change", and evidence
	// is not read. Then the "terminal-ish wait surfaces once" card: "a
	// worker that crashed and is still silent is not absorbed for good: it
	// surfaces again once per escalation_window". Surfaced, it absorbs
	// while the window since that surface is open and surfaces when it has
	// elapsed.
	{supervisor.KindTerminalGone, absent, open, false}:    supervisor.Surface,
	{supervisor.KindTerminalGone, absent, open, true}:     supervisor.Absorb,
	{supervisor.KindTerminalGone, absent, elapsed, false}: supervisor.Surface,
	{supervisor.KindTerminalGone, absent, elapsed, true}:  supervisor.Surface,
	{supervisor.KindTerminalGone, proof, open, false}:     supervisor.Surface,
	{supervisor.KindTerminalGone, proof, open, true}:      supervisor.Absorb,
	{supervisor.KindTerminalGone, proof, elapsed, false}:  supervisor.Surface,
	{supervisor.KindTerminalGone, proof, elapsed, true}:   supervisor.Surface,

	// "a run moved to held" -> Surface: always actionable, widened to any
	// hold naming the task.
	{supervisor.KindHoldOpened, absent, open, false}:    supervisor.Surface,
	{supervisor.KindHoldOpened, absent, open, true}:     supervisor.Surface,
	{supervisor.KindHoldOpened, absent, elapsed, false}: supervisor.Surface,
	{supervisor.KindHoldOpened, absent, elapsed, true}:  supervisor.Surface,
	{supervisor.KindHoldOpened, proof, open, false}:     supervisor.Surface,
	{supervisor.KindHoldOpened, proof, open, true}:      supervisor.Surface,
	{supervisor.KindHoldOpened, proof, elapsed, false}:  supervisor.Surface,
	{supervisor.KindHoldOpened, proof, elapsed, true}:   supervisor.Surface,
}

// eventKindWithRole picks a task event kind of the given role from
// taskstate's real table, so the event wakes here are built the way the loop
// builds them and never from a kind this test invented.
func eventKindWithRole(t *testing.T, role taskstate.Role) string {
	t.Helper()
	for _, row := range taskstate.Kinds() {
		if row.Role == role {
			return row.Kind
		}
	}
	t.Fatalf("taskstate's table has no kind with role %q", role)
	return ""
}

// wakeOf builds a wake of each kind by the route the package offers for it.
func wakeOf(t *testing.T, kind supervisor.Kind) supervisor.Wake {
	t.Helper()
	switch kind {
	case supervisor.KindDecisionEvent:
		return supervisor.Event(eventKindWithRole(t, taskstate.RoleDecisionOpen))
	case supervisor.KindPlainEvent:
		return supervisor.Event(eventKindWithRole(t, taskstate.RoleState))
	}
	w, err := supervisor.Of(kind)
	if err != nil {
		t.Fatalf("building a wake of kind %s: %v", kind, err)
	}
	return w
}

// runMatrix runs every cell of the oracle through c and reports each cell
// whose level disagrees, plus every kind in the real table that the oracle
// has no complete set of rows for. It reports rather than failing so the
// same walk can be run against a counterfeit that must fail.
func runMatrix(t *testing.T, c classifier) []string {
	t.Helper()
	var wrong []string
	for _, row := range supervisor.Kinds() {
		for _, evidence := range []supervisor.Evidence{absent, proof} {
			for _, window := range []supervisor.WindowState{open, elapsed} {
				for _, surfaced := range []bool{false, true} {
					at := cell{row.Kind, evidence, window, surfaced}
					want, ok := oracle[at]
					if !ok {
						wrong = append(wrong, fmt.Sprintf("%s: the table has this kind and the oracle has no row for this cell", at))
						continue
					}
					got := c(wakeOf(t, row.Kind), evidence, window, surfaced, noStall)
					if got.Level != want {
						wrong = append(wrong, fmt.Sprintf("%s: got %s (%s), the flowchart says %s", at, got.Level, got.Reason, want))
					}
				}
			}
		}
	}
	return wrong
}

// The P9 matrix. Every kind in the real table, crossed with evidence present
// and absent, the window open and elapsed, and the surfaced flag both ways,
// is held to an oracle written from PRD section 5's flowchart and not from
// the classifier's own table. A kind the table gains without an oracle row
// fails. What this establishes is the table and only the table: whether a
// held, stalled or finished run ever reaches here as evidence is
// internal/taskstate's derivation, and whether the window and the flag are
// computed so a window can elapse is the loop's, which cites P9 for its half
// in #100.
func TestTheClassifierMatchesTheFlowchartForEveryKindAndEvidence(t *testing.T) {
	principles.Cite(t, principles.P9)
	t.Parallel()
	if wrong := runMatrix(t, supervisor.Classify); len(wrong) > 0 {
		t.Fatalf("the classifier disagrees with the flowchart:\n%s", strings.Join(wrong, "\n"))
	}
	for at := range oracle {
		if _, ok := supervisor.Lookup(at.kind); !ok {
			t.Errorf("the oracle has a row for %s, which the table does not have", at.kind)
		}
	}
}

// Evidence absent surfaces for every kind whose absorb rests on evidence,
// which is the case PRD section 13's P9 row says to include. The two kinds
// the flowchart absorbs without reading evidence are named rather than
// skipped, so a kind added as evidence-gated is held to this without an
// edit here.
func TestAbsenceOfEvidenceSurfacesWhereverEvidenceCouldAbsorb(t *testing.T) {
	t.Parallel()
	for _, row := range supervisor.Kinds() {
		if row.Class != supervisor.ClassEvidence {
			continue
		}
		got := supervisor.Classify(wakeOf(t, row.Kind), absent, open, false, noStall)
		if !got.Surfaces() || got.Reason != supervisor.ReasonNoEvidence {
			t.Errorf("%s with no evidence: got %+v, want a surface for no evidence", row.Kind, got)
		}
	}
	for _, kind := range []supervisor.Kind{supervisor.KindDeclaredWait, supervisor.KindNoChange} {
		row, ok := supervisor.Lookup(kind)
		if !ok || row.Class == supervisor.ClassEvidence {
			t.Errorf("%s is expected to absorb without reading evidence, by the flowchart; its row says %+v", kind, row)
		}
	}
}

// Four classifiers, each the real one with one rule relaxed, fail the
// matrix at the cell the relaxation breaks, so what the matrix catches is
// named and not merely counted. A timer absorbs every evidence-gated kind
// while its window is open, which is time standing in for proof. A silencer
// absorbs a surfaced terminal gone whatever the window says, which would
// leave a dead worker quiet after its first surface: #95 line H. A storm
// ignores the surfaced flag for a terminal gone, which surfaces a crashed
// worker on every pass. And a non-escalator never surfaces an unchanged
// terminal with evidence once the escalation window has elapsed, which is
// the loop that never escalates in the issue's B1.
func TestTheMatrixRejectsATimerASilencedDeadTerminalAStormAndANonEscalator(t *testing.T) {
	t.Parallel()
	timer := func(w supervisor.Wake, e supervisor.Evidence, win supervisor.WindowState, s bool, st supervisor.Stall) supervisor.Verdict {
		if row, ok := supervisor.Lookup(w.Kind()); ok && row.Class == supervisor.ClassEvidence && win == open {
			return supervisor.Verdict{Level: supervisor.Absorb, Reason: supervisor.ReasonProgress}
		}
		return supervisor.Classify(w, e, win, s, st)
	}
	silenced := func(w supervisor.Wake, e supervisor.Evidence, win supervisor.WindowState, s bool, st supervisor.Stall) supervisor.Verdict {
		if w.Kind() == supervisor.KindTerminalGone && s {
			return supervisor.Verdict{Level: supervisor.Absorb, Reason: supervisor.ReasonSurfacedOnce}
		}
		return supervisor.Classify(w, e, win, s, st)
	}
	storm := func(w supervisor.Wake, e supervisor.Evidence, win supervisor.WindowState, s bool, st supervisor.Stall) supervisor.Verdict {
		if w.Kind() == supervisor.KindTerminalGone {
			return supervisor.Verdict{Level: supervisor.Surface, Reason: supervisor.ReasonActionable}
		}
		return supervisor.Classify(w, e, win, s, st)
	}
	nonEscalator := func(w supervisor.Wake, e supervisor.Evidence, win supervisor.WindowState, s bool, st supervisor.Stall) supervisor.Verdict {
		if w.Kind() == supervisor.KindTerminalUnchanged && e == proof {
			return supervisor.Verdict{Level: supervisor.Absorb, Reason: supervisor.ReasonProgress}
		}
		return supervisor.Classify(w, e, win, s, st)
	}
	cases := []struct {
		name   string
		c      classifier
		broken cell
	}{
		{"absorbs on an open window alone", timer, cell{supervisor.KindPlainEvent, absent, open, false}},
		{"silences a surfaced terminal gone", silenced, cell{supervisor.KindTerminalGone, absent, elapsed, true}},
		{"surfaces a silent crash on every pass", storm, cell{supervisor.KindTerminalGone, absent, open, true}},
		{"never escalates on an elapsed window with evidence", nonEscalator, cell{supervisor.KindTerminalUnchanged, proof, elapsed, false}},
	}
	for _, c := range cases {
		wrong := runMatrix(t, c.c)
		if len(wrong) == 0 {
			t.Errorf("a classifier that %s passed the matrix, so the matrix cannot fail on it", c.name)
			continue
		}
		if !slices.ContainsFunc(wrong, func(line string) bool { return strings.HasPrefix(line, c.broken.String()+":") }) {
			t.Errorf("a classifier that %s failed the matrix, but not at %s:\n%s", c.name, c.broken, strings.Join(wrong, "\n"))
		}
	}
}

// The terminal-ish wait: one surface, absorbed while the idle screen
// redraws, surfaced once more when the escalation window elapses, with the
// reason naming which. The same flag on every kind but the two it is
// defined for changes nothing, cell by cell against the flag off; the
// resurfacing class has its own test below.
func TestSurfacedOnceReachesOnlyTheUnchangedTerminalAndTheResurfacingKind(t *testing.T) {
	t.Parallel()
	unchanged := wakeOf(t, supervisor.KindTerminalUnchanged)
	if got := supervisor.Classify(unchanged, absent, open, true, noStall); got.Level != supervisor.Absorb || got.Reason != supervisor.ReasonSurfacedOnce {
		t.Errorf("surfaced, idle, window open: got %+v, want absorb as surfaced once", got)
	}
	if got := supervisor.Classify(unchanged, absent, elapsed, true, noStall); got.Level != supervisor.Surface || got.Reason != supervisor.ReasonWindowElapsed {
		t.Errorf("surfaced, idle, window elapsed: got %+v, want one more surface for the window", got)
	}
	for _, row := range supervisor.Kinds() {
		if row.Kind == supervisor.KindTerminalUnchanged || row.Class == supervisor.ClassResurface {
			continue
		}
		for _, evidence := range []supervisor.Evidence{absent, proof} {
			for _, window := range []supervisor.WindowState{open, elapsed} {
				off := supervisor.Classify(wakeOf(t, row.Kind), evidence, window, false, noStall)
				on := supervisor.Classify(wakeOf(t, row.Kind), evidence, window, true, noStall)
				if off != on {
					t.Errorf("%s evidence=%s window=%s: the flag changed the verdict from %+v to %+v", row.Kind, evidence, window, off, on)
				}
			}
		}
	}
}

// A crashed worker that stays silent, #95 line H: a terminal gone that has
// not surfaced surfaces as actionable whatever the window says; once it has
// surfaced it is absorbed while the window since that surface is open, and
// surfaces again for the window when it has elapsed, with evidence read in
// neither case and the repetition count raising nothing. Terminal gone is
// the one kind of the class, so a kind moved into it or out of it is a
// change this test sees.
func TestASilentCrashSurfacesOncePerWindow(t *testing.T) {
	t.Parallel()
	var resurfacing []supervisor.Kind
	for _, row := range supervisor.Kinds() {
		if row.Class == supervisor.ClassResurface {
			resurfacing = append(resurfacing, row.Kind)
		}
	}
	if !slices.Equal(resurfacing, []supervisor.Kind{supervisor.KindTerminalGone}) {
		t.Fatalf("the resurfacing kinds are %v, want exactly terminal gone", resurfacing)
	}
	gone := wakeOf(t, supervisor.KindTerminalGone)
	stalled := supervisor.Stall{Repetitions: 99, InspectAt: 1}
	for _, evidence := range []supervisor.Evidence{absent, proof} {
		for _, stall := range []supervisor.Stall{noStall, stalled} {
			for _, window := range []supervisor.WindowState{open, elapsed} {
				got := supervisor.Classify(gone, evidence, window, false, stall)
				if got != (supervisor.Verdict{Level: supervisor.Surface, Reason: supervisor.ReasonActionable}) {
					t.Errorf("not yet surfaced, evidence=%s window=%s stall=%+v: got %+v, want a surface as actionable", evidence, window, stall, got)
				}
			}
			got := supervisor.Classify(gone, evidence, open, true, stall)
			if got != (supervisor.Verdict{Level: supervisor.Absorb, Reason: supervisor.ReasonSurfacedThisWindow}) {
				t.Errorf("surfaced and within the window, evidence=%s stall=%+v: got %+v, want absorbed as surfaced this window", evidence, stall, got)
			}
			got = supervisor.Classify(gone, evidence, elapsed, true, stall)
			if got != (supervisor.Verdict{Level: supervisor.Surface, Reason: supervisor.ReasonWindowElapsed}) {
				t.Errorf("surfaced and past the window, evidence=%s stall=%+v: got %+v, want a surface for the window", evidence, stall, got)
			}
		}
	}
}

// Never more than once per window, and never silent for longer than one:
// passes every ten seconds over a crash that stays silent for five windows,
// with the caller the loop's shape, the window measured from the last
// surface and restarted at each one and nowhere else, surface at the first
// pass and then exactly at each window's end. The caller here is a model
// of the loop's, so this checks the table under that caller and nothing
// about the loop; internal/service's test of a dead terminal under an
// injected clock checks the loop through the real store.
func TestASilentCrashNeverSurfacesTwiceInOneWindow(t *testing.T) {
	t.Parallel()
	const (
		window = 60
		step   = 10
		span   = 5 * window
	)
	gone := wakeOf(t, supervisor.KindTerminalGone)
	var surfaces []int
	surfaced, last := false, 0
	for now := 0; now <= span; now += step {
		state := open
		if surfaced && now-last >= window {
			state = elapsed
		}
		if supervisor.Classify(gone, absent, state, surfaced, noStall).Surfaces() {
			surfaces = append(surfaces, now)
			surfaced, last = true, now
		}
	}
	want := []int{0, 60, 120, 180, 240, 300}
	if !slices.Equal(surfaces, want) {
		t.Fatalf("surfaces at %v seconds, want %v: once at the crash and once per window after", surfaces, want)
	}
	for i := 1; i < len(surfaces); i++ {
		if gap := surfaces[i] - surfaces[i-1]; gap < window {
			t.Errorf("surfaces at %d and %d are %d seconds apart, inside one %d-second window", surfaces[i-1], surfaces[i], gap, window)
		}
	}
}

// An unrecognized event kind surfaces as unrecognized, which is not the
// absence of evidence, and it surfaces even with evidence. A kind with the
// decision-open role surfaces whatever the evidence says, and the kind is
// read from taskstate's table rather than from a name: every row there with
// that role is a decision event and every other row is not.
func TestAnEventIsReadThroughTaskstateAndAnUnreadableVerbSurfaces(t *testing.T) {
	t.Parallel()
	typo := supervisor.Event("decision-opn")
	if typo.Kind() != supervisor.KindPlainEvent || !typo.Unrecognized() || typo.Event() != "decision-opn" {
		t.Fatalf("an unrecognized kind built %+v", typo)
	}
	for _, evidence := range []supervisor.Evidence{absent, proof} {
		got := supervisor.Classify(typo, evidence, open, true, noStall)
		if got.Level != supervisor.Surface || got.Reason != supervisor.ReasonUnrecognized {
			t.Errorf("unrecognized kind with evidence=%s: got %+v, want a surface as unrecognized", evidence, got)
		}
	}
	if got := supervisor.Classify(supervisor.Wake{}, proof, open, true, noStall); got.Reason != supervisor.ReasonUnrecognized {
		t.Errorf("a wake nothing built: got %+v, want a surface as unrecognized", got)
	}
	for _, row := range taskstate.Kinds() {
		w := supervisor.Event(row.Kind)
		if w.Unrecognized() {
			t.Errorf("%q is in taskstate's table and was built unrecognized", row.Kind)
		}
		want := supervisor.KindPlainEvent
		if row.Role == taskstate.RoleDecisionOpen {
			want = supervisor.KindDecisionEvent
		}
		if w.Kind() != want {
			t.Errorf("%q with role %s built %s, want %s", row.Kind, row.Role, w.Kind(), want)
		}
		if row.Role == taskstate.RoleDecisionOpen {
			for _, evidence := range []supervisor.Evidence{absent, proof} {
				if got := supervisor.Classify(w, evidence, open, true, noStall); !got.Surfaces() || got.Reason != supervisor.ReasonActionable {
					t.Errorf("%q with evidence=%s: got %+v, want a surface as actionable", row.Kind, evidence, got)
				}
			}
		}
	}
}

// The repetition row: at or above the threshold a surface of the unchanged
// terminal becomes surface-and-inspect, below it stays a surface, an absorb
// is never raised, and no other kind reads the count.
func TestRepeatedStallRaisesTheUnchangedTerminalToInspect(t *testing.T) {
	t.Parallel()
	unchanged := wakeOf(t, supervisor.KindTerminalUnchanged)
	at := supervisor.Stall{Repetitions: 3, InspectAt: 3}
	if got := supervisor.Classify(unchanged, absent, open, false, at); got.Level != supervisor.SurfaceAndInspect || got.Reason != supervisor.ReasonNoEvidence {
		t.Errorf("at the threshold: got %+v, want surface-and-inspect for no evidence", got)
	}
	if got := supervisor.Classify(unchanged, proof, elapsed, false, at); got.Level != supervisor.SurfaceAndInspect || got.Reason != supervisor.ReasonWindowElapsed {
		t.Errorf("at the threshold on an elapsed window: got %+v, want surface-and-inspect for the window", got)
	}
	below := supervisor.Stall{Repetitions: 2, InspectAt: 3}
	if got := supervisor.Classify(unchanged, absent, open, false, below); got.Level != supervisor.Surface {
		t.Errorf("below the threshold: got %+v, want a plain surface", got)
	}
	if got := supervisor.Classify(unchanged, proof, open, false, at); got.Level != supervisor.Absorb {
		t.Errorf("an absorb at the threshold: got %+v, want it left an absorb", got)
	}
	if got := supervisor.Classify(unchanged, absent, open, false, supervisor.Stall{Repetitions: 0, InspectAt: 0}); got.Level != supervisor.SurfaceAndInspect {
		t.Errorf("a threshold of zero: got %+v, want every surface to inspect", got)
	}
	for _, row := range supervisor.Kinds() {
		if row.Kind == supervisor.KindTerminalUnchanged {
			continue
		}
		for _, evidence := range []supervisor.Evidence{absent, proof} {
			for _, window := range []supervisor.WindowState{open, elapsed} {
				if got := supervisor.Classify(wakeOf(t, row.Kind), evidence, window, false, supervisor.Stall{Repetitions: 99, InspectAt: 1}); got.Level == supervisor.SurfaceAndInspect {
					t.Errorf("%s evidence=%s window=%s read the repetition count: %+v", row.Kind, evidence, window, got)
				}
			}
		}
	}
}

// Evidence is Progress and nothing else: a resolution whose terminal is live
// but whose Progress is false is no evidence, so nothing here reads Basis.
func TestEvidenceIsProgressAlone(t *testing.T) {
	t.Parallel()
	live := taskstate.Resolution{State: taskstate.Running, Source: taskstate.SourceProcess, Basis: taskstate.Basis{Process: taskstate.Live}}
	if got := supervisor.EvidenceOf(live); got != supervisor.NoEvidence {
		t.Errorf("a live terminal with Progress false read as %s", got)
	}
	live.Progress = true
	if got := supervisor.EvidenceOf(live); got != supervisor.Progress {
		t.Errorf("Progress true read as %s", got)
	}
	if got := supervisor.Classify(wakeOf(t, supervisor.KindPlainEvent), supervisor.Evidence("yes"), open, false, noStall); !got.Surfaces() {
		t.Errorf("a word outside the evidence set absorbed: %+v", got)
	}
	if got := supervisor.Classify(wakeOf(t, supervisor.KindDeclaredWait), absent, supervisor.WindowState("soon"), false, noStall); !got.Surfaces() {
		t.Errorf("a word outside the window set absorbed: %+v", got)
	}
}

// The two event kinds are derived and Of refuses to state them, as it
// refuses a word outside the table, so the table's kinds are the only ones
// a wake can carry. The one kind with no producer is the check result and
// only that one.
func TestTheTableIsClosedAndTheProducerlessKindIsNamed(t *testing.T) {
	t.Parallel()
	for _, kind := range []supervisor.Kind{supervisor.KindDecisionEvent, supervisor.KindPlainEvent, "terminal-vanished", ""} {
		if _, err := supervisor.Of(kind); !errors.Is(err, supervisor.ErrKind) {
			t.Errorf("Of(%q) = %v, want ErrKind", kind, err)
		}
	}
	var unproduced []supervisor.Kind
	for _, row := range supervisor.Kinds() {
		if !row.Produced {
			unproduced = append(unproduced, row.Kind)
		}
		if _, ok := supervisor.Lookup(row.Kind); !ok {
			t.Errorf("%s is in Kinds and Lookup does not find it", row.Kind)
		}
	}
	if len(unproduced) != 1 || unproduced[0] != supervisor.KindCheckResult {
		t.Errorf("the kinds marked as having no producer are %v, want exactly the check result", unproduced)
	}
}
