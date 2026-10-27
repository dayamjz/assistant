package supervisor

import "github.com/dayamjz/assistant/internal/taskstate"

// Evidence is whether something positively proves the task's work is
// proceeding. It is a closed set of two, and EvidenceOf is its only
// derivation: P9's evidence is taskstate.Resolution.Progress and nothing
// else, so this package reads no Basis, no liveness and no run status of its
// own. A value outside the two is read as NoEvidence, which is the direction
// a wrong value must fail in.
type Evidence string

// The two evidence values.
const (
	// NoEvidence is the absence of proof, which P9 says surfaces. It is not
	// evidence that work stopped: it is nothing either way.
	NoEvidence Evidence = "absent"
	// Progress is positive proof, as taskstate derives it: a validation run
	// the service is advancing now, or a terminal whose agent process is
	// live.
	Progress Evidence = "progress"
)

// EvidenceOf is the one translation from a resolution to evidence. It reads
// Progress alone, which is what makes "the classifier re-derives nothing"
// a property of this function rather than a rule its callers follow.
func EvidenceOf(r taskstate.Resolution) Evidence {
	if r.Progress {
		return Progress
	}
	return NoEvidence
}

// WindowState is whether the window that applies to a kind has elapsed: the
// escalation window for KindTerminalUnchanged, the recheck window for
// KindDeclaredWait, and for KindTerminalGone the escalation window measured
// from that wake's last surface. The loop computes it from the markers and
// the configured length, both of which are #100's; this package reads no
// clock and no key. For every other kind it is not read. A value outside the
// two is read as WindowElapsed, which is the direction a wrong value must
// fail in.
type WindowState string

// The two window states.
const (
	// WindowOpen is a window that has not elapsed.
	WindowOpen WindowState = "open"
	// WindowElapsed is a window that has.
	WindowElapsed WindowState = "elapsed"
)

// Stall is the repeated-stall input PRD section 11 names: how many times the
// terminal's current unchanged screen has already surfaced, consecutively,
// and the count at which the next surface demands a deeper inspection; and
// the inputs PRD section 6's "a stalled worker holds for you" adds, the
// count past that at which the next surface also holds the task for a
// person, and whether the task is one a stall can hold at all.
//
// Every field is a value the loop computed. Repetitions is a marker written
// in the same transaction as the wake row (#99), counting surfaces and not
// passes: an absorbed pass on the same digest neither adds to it nor resets
// it, a surface on a different digest resets it, and so does an approve
// answered to a stall hold on the same screen, which the loop reads off the
// hold record. InspectAt is the stall_inspection_count key, read by #100;
// its floor and ceiling are that key's, and a value of zero or less here
// reads as zero, so every surface inspects. HoldAt is the stall_hold_count
// key, counted past InspectAt, and a value of zero or less here reads as
// zero, so the hold falls on the first inspecting surface: toward the
// person, which is the direction a wrong value must fail in.
//
// State and Captured are what PRD section 6 says a stall is: a task that
// resolves Running whose terminal was captured live this pass, so a declared
// wait (Paused), a gate run being advanced (Validating), a task already held
// (Held), and a terminal that read gone or Unobserved never hold, whatever
// the count says.
//
// The count raises a verdict's level and nothing else: it stops nothing and
// parks nothing, so it is not one of internal/graph's bounds, and the hold
// the top level asks for waits on a person and ends nothing.
type Stall struct {
	Repetitions int
	InspectAt   int
	HoldAt      int
	State       taskstate.State
	Captured    bool
}

// holds reports whether a surface at this count of a task in this state is
// the one PRD section 6 has hold the task: the task resolves Running, its
// terminal was captured live, and the screen has surfaced InspectAt plus
// HoldAt times already.
func (s Stall) holds() bool {
	return s.State == taskstate.Running && s.Captured &&
		s.Repetitions >= max(s.InspectAt, 0)+max(s.HoldAt, 0)
}

// Level is what the verdict asks of the caller.
type Level string

// The three levels.
const (
	// Absorb is advance the markers and wake nobody.
	Absorb Level = "absorb"
	// Surface is write the wake to the durable queue and wake the
	// coordinator.
	Surface Level = "surface"
	// SurfaceAndInspect is Surface, with the demand PRD section 11 adds after
	// enough repetitions: a deeper inspection rather than another surface
	// look. It is one level above Surface and stops nothing.
	SurfaceAndInspect Level = "surface-and-inspect"
	// SurfaceInspectAndHold is SurfaceAndInspect, with the hold PRD section
	// 6's "a stalled worker holds for you" adds once the inspection has been
	// asked for stall_hold_count times and the screen has still not changed:
	// the caller registers a hold naming the task. It is one level above
	// SurfaceAndInspect, and it ends nothing: the hold waits on a person.
	SurfaceInspectAndHold Level = "surface-inspect-and-hold"
)

// Reason is why a verdict is what it is. It is a closed set, so a queue row
// or a test can tell an unreadable verb from the absence of evidence.
type Reason string

// The reasons.
const (
	// ReasonActionable is a kind that always surfaces: an authenticated check
	// result, an event with a decision verb, a hold opened; and a terminal
	// gone on its first surface.
	ReasonActionable Reason = "actionable"
	// ReasonUnrecognized is an event whose kind taskstate has no row for, or
	// a Wake nothing built. It surfaces, and it is not ReasonNoEvidence: the
	// verb could not be read, which is a different fact from no proof of
	// progress.
	ReasonUnrecognized Reason = "unrecognized"
	// ReasonNoEvidence is nothing positively proving work is proceeding.
	ReasonNoEvidence Reason = "no-evidence"
	// ReasonProgress is positive evidence of progress.
	ReasonProgress Reason = "progress"
	// ReasonWindowElapsed is the kind's window having elapsed.
	ReasonWindowElapsed Reason = "window-elapsed"
	// ReasonWindowOpen is a declared wait whose recheck window has not
	// elapsed.
	ReasonWindowOpen Reason = "window-open"
	// ReasonSurfacedOnce is an unchanged terminal with no evidence that has
	// already surfaced and whose escalation window has not elapsed: the
	// terminal-ish wait PRD section 5 has surface once.
	ReasonSurfacedOnce Reason = "surfaced-once"
	// ReasonNoChange is a pass that observed everything and found nothing
	// changed.
	ReasonNoChange Reason = "no-change"
	// ReasonRoutine is an event whose kind taskstate marks Routine: the
	// program's record of an act the coordinator asked for or the program
	// made itself, which asks nobody anything.
	ReasonRoutine Reason = "routine"
	// ReasonSurfacedThisWindow is a ClassResurface wake that has already
	// surfaced and whose window, measured from that surface, has not
	// elapsed: the crashed worker PRD section 5 surfaces once per
	// escalation window, between two of its surfaces.
	ReasonSurfacedThisWindow Reason = "surfaced-this-window"
)

// Verdict is the classifier's answer.
type Verdict struct {
	Level  Level
	Reason Reason
}

// Surfaces reports whether the verdict asks for a wake to be written.
func (v Verdict) Surfaces() bool { return v.Level != Absorb }

// Classify decides one wake, PRD section 5's flowchart as a function. It is
// pure: it reads no clock, no key and no record, and the same inputs give
// the same verdict.
//
// The rules, in the order they are applied:
//
//   - A wake nothing built, or an event whose kind is unrecognized, surfaces
//     with ReasonUnrecognized before anything else is read.
//   - An event whose kind taskstate marks Routine absorbs with
//     ReasonRoutine, whatever evidence says. It is the third case this
//     package absorbs with no proof of progress, and it rests on the entry
//     itself rather than on time: the program wrote it about an act the
//     coordinator asked for or the program made, so it tells the
//     coordinator nothing. What the act leads to, a worker that never
//     starts or dies, is the terminal's reading and surfaces there.
//   - ClassActionable surfaces, whatever evidence, window and surfaced say.
//   - ClassResurface reads no evidence. Not yet surfaced, it surfaces as
//     actionable; surfaced, it absorbs while window is WindowOpen and
//     surfaces when it has elapsed. The caller measures window from the
//     wake's last surface and restarts it on every surface, so the wake
//     surfaces at most once per window and never stays silent for longer
//     than one: PRD section 5's crashed worker that is still silent.
//   - ClassAbsorb absorbs, whatever they say.
//   - ClassWait absorbs while window is WindowOpen and surfaces when it has
//     elapsed; evidence is not read, because a declared wait is the worker's
//     own statement and PRD section 5 says it is not a stall.
//   - ClassEvidence surfaces without Progress and absorbs with it, and for
//     KindTerminalUnchanged two more facts apply. An absorb on evidence ends
//     when the escalation window elapses, so a screen unchanged for that long
//     surfaces even with evidence. And without evidence, surfaced=true
//     absorbs while the window is open, which with ClassResurface's absorb
//     and the routine entry is one of the three cases this package absorbs
//     with no proof of progress: the terminal-ish wait that has surfaced once and whose idle
//     screen is redrawing. When the window elapses it surfaces once more,
//     and the caller restarts the window at that surface, so the
//     post-surface absorb lasts one window at a time and not for good.
//   - A surface of KindTerminalUnchanged is raised to SurfaceAndInspect when
//     stall.Repetitions is at or above stall.InspectAt, and on to
//     SurfaceInspectAndHold when it is at or above stall.InspectAt plus
//     stall.HoldAt and the task resolves Running with its terminal captured,
//     PRD section 6's stalled worker. No other kind reads the count: the
//     repetition PRD section 11 names is of a stall on the same unchanged
//     screen, and only this kind has one.
//
// surfaced is read for KindTerminalUnchanged and ClassResurface alone.
// Every other kind with surfaced=true decides exactly as with it false. For
// a terminal gone the flag silences only until the window elapses, so a
// caller passing it cannot leave a dead terminal quiet after its first
// surface; it can only space the surfaces one window apart. Never absorbing on elapsed time alone is the shape of the
// table rather than a check: no rule here absorbs because time has passed.
// Three rules read an open window to absorb, and none rests on the window
// alone. The declared wait rests on the worker's own declaration. The
// surfaced-once case and the resurface case absorb with no evidence, and
// they rest on the surface already made plus the window still being open:
// deliberate, bounded exceptions, and the escalation window is what ends
// each.
func Classify(wake Wake, evidence Evidence, window WindowState, surfaced bool, stall Stall) Verdict {
	row, ok := Lookup(wake.kind)
	if !ok || wake.unrecognized {
		return Verdict{Level: Surface, Reason: ReasonUnrecognized}
	}
	if wake.routine {
		return Verdict{Level: Absorb, Reason: ReasonRoutine}
	}
	switch row.Class {
	case ClassActionable:
		return Verdict{Level: Surface, Reason: ReasonActionable}
	case ClassAbsorb:
		return Verdict{Level: Absorb, Reason: ReasonNoChange}
	case ClassResurface:
		switch {
		case !surfaced:
			return Verdict{Level: Surface, Reason: ReasonActionable}
		case window == WindowOpen:
			return Verdict{Level: Absorb, Reason: ReasonSurfacedThisWindow}
		default:
			return Verdict{Level: Surface, Reason: ReasonWindowElapsed}
		}
	case ClassWait:
		if window == WindowOpen {
			return Verdict{Level: Absorb, Reason: ReasonWindowOpen}
		}
		return Verdict{Level: Surface, Reason: ReasonWindowElapsed}
	}
	if wake.kind != KindTerminalUnchanged {
		if evidence == Progress {
			return Verdict{Level: Absorb, Reason: ReasonProgress}
		}
		return Verdict{Level: Surface, Reason: ReasonNoEvidence}
	}
	var v Verdict
	switch {
	case evidence == Progress && window == WindowOpen:
		v = Verdict{Level: Absorb, Reason: ReasonProgress}
	case evidence == Progress:
		v = Verdict{Level: Surface, Reason: ReasonWindowElapsed}
	case surfaced && window == WindowOpen:
		v = Verdict{Level: Absorb, Reason: ReasonSurfacedOnce}
	case surfaced:
		v = Verdict{Level: Surface, Reason: ReasonWindowElapsed}
	default:
		v = Verdict{Level: Surface, Reason: ReasonNoEvidence}
	}
	if v.Level == Surface && stall.Repetitions >= max(stall.InspectAt, 0) {
		v.Level = SurfaceAndInspect
	}
	if v.Level == SurfaceAndInspect && stall.holds() {
		v.Level = SurfaceInspectAndHold
	}
	return v
}
