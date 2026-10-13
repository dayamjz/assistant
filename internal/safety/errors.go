package safety

import (
	"errors"
	"strconv"
	"strings"
)

// Errors a caller is expected to handle. Test with errors.Is.
var (
	// ErrRefused matches every refusal this package produces. A refusal is a
	// typed result carrying what could not be verified or what would be lost;
	// use errors.As with **Refusal to read it.
	ErrRefused = errors.New("safety: update refused")
	// ErrAnchorNotObserved is returned when an update was submitted with an
	// anchor that is not an observation: the zero Observation, or one taken
	// against a different target. It is a programming error rather than a
	// refusal, because no state of the world produces it.
	ErrAnchorNotObserved = errors.New("safety: update anchor is not an observation of this target")
	// ErrInvalidTarget is returned when a target names no remote, or names a
	// reference that is not a full refs/ name.
	ErrInvalidTarget = errors.New("safety: target is not a full reference on a named remote")
	// ErrInvalidUpdate is returned when an update proposes no commit.
	ErrInvalidUpdate = errors.New("safety: update does not propose a commit")
	// ErrInvalidObservationRecord is returned when a checkpointed record
	// describes a state no read could have produced: an absent target that
	// names a commit, or a present one that names none.
	ErrInvalidObservationRecord = errors.New("safety: observation record does not describe a state a read could produce")
)

// Reason says why an update was refused. Every reason is a fact that could not
// be established or a loss that would occur, never a preference.
type Reason string

const (
	// ReasonUnreadableRemote is the remote could not be read, so where the
	// target stands now is unknown.
	ReasonUnreadableRemote Reason = "unreadable-remote"
	// ReasonTargetMoved is the target no longer stands where the run observed
	// it, so the anchor the run holds is not the state being updated. Nothing
	// this update could lose was identified, and the update is still refused:
	// an anchor that does not describe the current target protects nothing.
	ReasonTargetMoved Reason = "target-moved"
	// ReasonWouldDiscard is the target holds commits the proposed commit does
	// not contain, so the update would drop them from the branch. The refusal
	// names them.
	ReasonWouldDiscard Reason = "would-discard"
	// ReasonUnrelatedHistories is the two commits compared share no ancestor,
	// so no statement about what one contains of the other is available.
	ReasonUnrelatedHistories Reason = "unrelated-histories"
	// ReasonUnverifiable is a fact the decision depends on could not be
	// determined. The update is refused rather than allowed on an assumption.
	ReasonUnverifiable Reason = "unverifiable"
)

// Refusal reports an update that may not proceed. It is the result a caller
// handles, not a warning execution continues past: there is no decision inside
// it and no anchor to push on.
type Refusal struct {
	// Reason is the category of refusal.
	Reason Reason
	// Target is the branch the refused update would have moved.
	Target Target
	// Anchor is the observation the update was submitted with. Its zero value
	// means the refusal happened before the anchor was read.
	Anchor Observation
	// Observed is what the fresh read found on the remote, when one succeeded
	// and reduced to one state for the target. Its zero value means the remote
	// was not read, or was read and did not advertise the target, or was read
	// and could not be reduced to one state for it. The last case is a target
	// advertised more than once, advertised with no object, or advertised as
	// an object that peels to another and so names no commit of its own. No
	// state is invented for any of them; Reason and Detail say which happened,
	// and Detail carries the object identifiers where there are any.
	Observed RemoteState
	// Discarded names the commits the fresh read's target holds that the
	// proposed commit does not contain. It is populated for ReasonWouldDiscard
	// and is empty for every other reason.
	//
	// It is not narrowed to commits the run never observed. A refusal means
	// the anchor no longer describes the target, so the run cannot claim to
	// have incorporated anything, and this names everything the update would
	// drop. Its order is vcs.CommitsNotIn's, most recent first; this package
	// does not reorder it. It is unbounded, so a caller rendering it uses
	// DiscardedText rather than joining it.
	Discarded []string
	// Detail states what specifically could not be established or what
	// changed, in a sentence a caller can report without adding to it.
	Detail string
	// Cause is the underlying failure, when the refusal came from one.
	Cause error
}

// MaxDiscardedNamed is how many of Refusal.Discarded DiscardedText names
// before it counts the rest, so a refusal over a long-diverged branch renders
// as bounded text.
const MaxDiscardedNamed = 20

// DiscardedText renders Refusal.Discarded as text a caller can report without
// adding to it: the first MaxDiscardedNamed identifiers in the field's order,
// joined by ", ", followed by ", and K more" when the field holds more. It is
// the empty string when nothing would be discarded, and, like Error, it must
// be called on a non-nil *Refusal. Error renders the list through it, so the
// two cannot disagree about what was named.
func (r *Refusal) DiscardedText() string {
	if len(r.Discarded) <= MaxDiscardedNamed {
		return strings.Join(r.Discarded, ", ")
	}
	return strings.Join(r.Discarded[:MaxDiscardedNamed], ", ") +
		", and " + strconv.Itoa(len(r.Discarded)-MaxDiscardedNamed) + " more"
}

// Error renders the refusal with its reason, its target, and its detail, and
// the commits it would discard as DiscardedText renders them.
func (r *Refusal) Error() string {
	var b strings.Builder
	b.WriteString("safety: refused to update ")
	b.WriteString(r.Target.Ref)
	b.WriteString(" on ")
	b.WriteString(r.Target.Remote)
	b.WriteString(" (")
	b.WriteString(string(r.Reason))
	b.WriteString("): ")
	b.WriteString(r.Detail)
	if len(r.Discarded) > 0 {
		b.WriteString("; would discard ")
		b.WriteString(r.DiscardedText())
	}
	return b.String()
}

// remedies is the one statement of what gets a caller out of a refusal, keyed
// by its reason. A reason with no row has no remedy stated here.
var remedies = map[Reason]string{
	ReasonWouldDiscard: "To keep the commits this would discard, incorporate them: observe the target " +
		"again, rebase the proposed commit onto the commit that observation found, and submit the " +
		"result anchored to that new observation. While the target still stands there, and with " +
		"its commit fetched into the local repository, the decision over it is a fast-forward.",
}

// Remedy states what gets a caller out of this refusal, as text it can report
// without adding to it, or the empty string when no remedy is stated for the
// reason. Only ReasonWouldDiscard has one. It depends on the reason alone, so
// two refusals for the same reason state the same remedy.
//
// The would-discard remedy is a fresh Guard.Observe and a rebase onto what it
// found, not a retry: the original anchor no longer describes the target, so
// Decide refuses it again however the proposed commit changes. What makes the
// second decision a fast-forward is that the proposed commit then contains
// the target's commit and the target has not moved since the observation;
// a target that moves again in between is refused again, as it should be. The
// fast-forward also rests on Decide's stated precondition that the local
// repository holds both commits, which the remedy text states as fetching the
// target's commit; a comparison Decide cannot answer is refused as
// unverifiable instead. Like Error, it must be called on a non-nil *Refusal.
func (r *Refusal) Remedy() string {
	return remedies[r.Reason]
}

// Unwrap makes every refusal match ErrRefused, so a caller that only needs to
// know the update did not proceed does not have to enumerate the reasons, and
// exposes the failure the refusal came from alongside it, so errors.Is against
// a mechanism error such as vcs.ErrNoMergeBase still matches.
func (r *Refusal) Unwrap() []error {
	if r.Cause == nil {
		return []error{ErrRefused}
	}
	return []error{ErrRefused, r.Cause}
}
