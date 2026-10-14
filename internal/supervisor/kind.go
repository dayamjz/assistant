package supervisor

import (
	"errors"
	"fmt"
	"slices"

	"github.com/dayamjz/assistant/internal/taskstate"
)

// Kind is what a wake is, in PRD section 5's flowchart vocabulary. It is a
// closed set: a word outside Kinds is not a Kind this package classifies.
type Kind string

// The wake kinds, in the flowchart's order.
const (
	// KindCheckResult is an authenticated check result. It has no producer in
	// this build, and its row says so: the checks stage writes its result
	// into the run, and a run that needs a person reaches the supervisor as
	// KindHoldOpened. The kind is kept because the flowchart names it, the
	// way machine.OutcomePassed is kept with no producer, so the table and
	// the flowchart stay the same list.
	KindCheckResult Kind = "authenticated-check-result"
	// KindDecisionEvent is a task event whose kind, in taskstate's table,
	// opens a decision. Only Event builds one, because what a decision verb
	// is comes from taskstate.Lookup and never from a second list.
	KindDecisionEvent Kind = "event-with-decision-verb"
	// KindPlainEvent is a task event whose kind opens no decision. Only
	// Event builds one, on the same terms as KindDecisionEvent.
	KindPlainEvent Kind = "event-without-decision-verb"
	// KindTerminalUnchanged is a terminal observed successfully whose
	// normalized screen is the one observed before. It is the terminal-ish
	// wait PRD section 5 has surface once.
	KindTerminalUnchanged Kind = "terminal-unchanged"
	// KindDeclaredWait is a worker's own declared external wait: the task
	// resolves Paused, which PRD section 5 says is not a stall.
	KindDeclaredWait Kind = "declared-external-wait"
	// KindNoChange is a pass that observed every fact successfully and found
	// each identical to the one before. Nothing else may be classified as
	// it: a failed observation is KindTerminalGone.
	KindNoChange Kind = "no-change"
	// KindTerminalGone is a capture that failed, a probe that could not
	// observe the terminal, or a terminal that died. Which of the three is
	// the wake's marker, written by the loop; the verdict is the same for
	// all three, so this package does not read it. It is the one kind of
	// ClassResurface: a worker that crashed and stays silent surfaces again
	// once per escalation window, PRD section 5's "a terminal-ish wait
	// surfaces once" card.
	KindTerminalGone Kind = "terminal-gone"
	// KindHoldOpened is an open hold naming the task or one of its
	// attributed runs, including a run moved to held with no worker event.
	KindHoldOpened Kind = "hold-opened"
	// KindMessageUnacknowledged is a message the program wrote to a task's
	// worker that the worker has not acknowledged within the escalation
	// window, PRD section 6's "an unacknowledged message surfaces once". It
	// is always actionable: the window is the detector's, which makes the
	// detection only once the window has elapsed from the record's written
	// instant and only once per message, and nothing here resends it.
	KindMessageUnacknowledged Kind = "message-unacknowledged"
)

// Class is how the flowchart decides a kind.
type Class string

// The classes. They are the flowchart's four shapes, plus the one the
// "terminal-ish wait surfaces once" card adds for a crashed worker that
// stays silent, and nothing finer.
const (
	// ClassActionable always surfaces, whatever the evidence, the window or
	// the surfaced flag say.
	ClassActionable Class = "actionable"
	// ClassResurface surfaces whatever the evidence says, and is bounded to
	// once per window: a wake that has not surfaced surfaces, and one that
	// has absorbs while its window is open and surfaces again when it
	// elapses. The window is the escalation window measured from the last
	// surface, which the loop computes; see Classify.
	ClassResurface Class = "resurface"
	// ClassEvidence absorbs on positive evidence of progress and surfaces
	// without it. For KindTerminalUnchanged the absorb also ends at the
	// escalation window, and the surfaced flag absorbs one more case; see
	// Classify.
	ClassEvidence Class = "evidence"
	// ClassWait absorbs until its window elapses and reads no evidence.
	ClassWait Class = "wait"
	// ClassAbsorb always absorbs.
	ClassAbsorb Class = "absorb"
)

// Row is one wake kind's meaning.
type Row struct {
	// Kind is the wake kind.
	Kind Kind
	// Class is how the flowchart decides it.
	Class Class
	// Produced is whether anything in this build produces the kind. A kind
	// with no producer is kept so the table and the flowchart are one list,
	// and marked so a matrix row over it is not mistaken for a test of a
	// path something walks.
	Produced bool
}

// kinds is the one owner of what a wake kind means. The flowchart in PRD
// section 5 is what each row was written from, and the matrix test's oracle
// is written from the flowchart again, independently, so a row that drifts
// from the flowchart fails there rather than agreeing with itself.
var kinds = []Row{
	{Kind: KindCheckResult, Class: ClassActionable, Produced: false},
	{Kind: KindDecisionEvent, Class: ClassActionable, Produced: true},
	{Kind: KindPlainEvent, Class: ClassEvidence, Produced: true},
	{Kind: KindTerminalUnchanged, Class: ClassEvidence, Produced: true},
	{Kind: KindDeclaredWait, Class: ClassWait, Produced: true},
	{Kind: KindNoChange, Class: ClassAbsorb, Produced: true},
	{Kind: KindTerminalGone, Class: ClassResurface, Produced: true},
	{Kind: KindHoldOpened, Class: ClassActionable, Produced: true},
	{Kind: KindMessageUnacknowledged, Class: ClassActionable, Produced: true},
}

// Kinds returns every kind's row, in the flowchart's order. The result is a
// copy.
func Kinds() []Row { return slices.Clone(kinds) }

// Lookup returns the row for a kind, or false for a word this package defines
// no meaning for. That is a normal answer rather than an error.
func Lookup(kind Kind) (Row, bool) {
	for _, r := range kinds {
		if r.Kind == kind {
			return r, true
		}
	}
	return Row{}, false
}

// ErrKind reports a Kind this package has no row for, or one that Of may not
// build because the kind is derived rather than stated.
var ErrKind = errors.New("supervisor: not a wake kind this package builds")

// Wake is one wake as the classifier reads it. Its fields are unexported so
// the two event kinds can only be reached through Event, which derives them
// from taskstate's table; a caller cannot state that an event carries no
// decision verb.
type Wake struct {
	kind         Kind
	event        string
	unrecognized bool
}

// Of builds a wake of a kind that the loop observed directly. It refuses
// KindDecisionEvent and KindPlainEvent, which only Event derives, and any
// word outside Kinds, both with ErrKind.
func Of(kind Kind) (Wake, error) {
	if _, ok := Lookup(kind); !ok {
		return Wake{}, fmt.Errorf("%w: %q", ErrKind, kind)
	}
	if kind == KindDecisionEvent || kind == KindPlainEvent {
		return Wake{}, fmt.Errorf("%w: %q is derived from the event's kind by Event", ErrKind, kind)
	}
	return Wake{kind: kind}, nil
}

// Event builds the wake for a task event of the given kind. The kind is read
// through taskstate.Lookup: a row whose role is taskstate.RoleDecisionOpen is
// KindDecisionEvent, any other row is KindPlainEvent, and a kind taskstate
// has no row for is a KindPlainEvent marked unrecognized, which Classify
// surfaces with ReasonUnrecognized whatever else is true. It never refuses,
// because an unreadable verb is a classification and not a caller's error:
// P3's stance, carried here the way taskstate carries it.
//
// Only the open role is a decision verb. A kind that closes a decision is
// read as an event with no decision verb, on taskstate's own terms that a
// close is decision-only and claims no state; whether a close should surface
// on its own is not decided here.
func Event(eventKind string) Wake {
	row, ok := taskstate.Lookup(eventKind)
	switch {
	case !ok:
		return Wake{kind: KindPlainEvent, event: eventKind, unrecognized: true}
	case row.Role == taskstate.RoleDecisionOpen:
		return Wake{kind: KindDecisionEvent, event: eventKind}
	default:
		return Wake{kind: KindPlainEvent, event: eventKind}
	}
}

// Kind is the wake's kind. A Wake nobody built through Of or Event has the
// empty kind, which Classify surfaces as unrecognized.
func (w Wake) Kind() Kind { return w.kind }

// Event is the task event kind an event wake was built from, and empty for
// every other kind.
func (w Wake) Event() string { return w.event }

// Unrecognized reports whether the wake is an event whose kind taskstate has
// no row for.
func (w Wake) Unrecognized() bool { return w.unrecognized }
