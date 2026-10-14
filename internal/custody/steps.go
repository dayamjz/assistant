package custody

import "slices"

// Namespace is where custody writes references in a person's working copy.
// Everything under it is this program's own: a recovery reference names work
// custody brought back or a head it replaced, and a fetched reference is the
// scratch name a commit is fetched under before a move and removed after it.
// Nothing custody writes is outside it except the one branch a move moves,
// and nothing under it is a branch, so no reference custody owns can be
// checked out, pushed by a person's ordinary push, or mistaken for theirs.
//
// Custody never deletes a recovery reference. Removing one is the person's
// decision, made with git themselves, once they have what it names.
const Namespace = "refs/assistant/custody/"

// RecoveredRef is the recovery reference a ClassRecoverable recovery writes:
// the work the run moved on, brought back into the working copy beside the
// branch.
func RecoveredRef(run string) string { return Namespace + run + "/recovered" }

// ReplacedRef is the recovery reference a ClassDivergedEquivalent adoption
// writes before it moves the branch: the head the branch stood at, which the
// adoption replaces.
func ReplacedRef(run string) string { return Namespace + run + "/replaced" }

// FetchedRef is the scratch reference a commit is fetched under from the
// gate before it is acted on, and removed once the action is done or
// refused.
func FetchedRef(run string) string { return Namespace + run + "/fetched" }

// StepKind is one kind of thing a synchronization does. The set is closed.
type StepKind string

const (
	// StepVerify reads the code host's branch, without fetching or writing
	// anything there, and requires it to name the commit the run pushed.
	StepVerify StepKind = "verify"
	// StepFetch fetches a commit from the gate repository into the working
	// copy under the fetched reference.
	StepFetch StepKind = "fetch"
	// StepRecover writes the recovery reference that brings a run's work
	// back, creating it and never replacing one.
	StepRecover StepKind = "recover"
	// StepKeep writes the recovery reference that keeps the head an
	// adoption replaces, before the move, on StepRecover's terms.
	StepKeep StepKind = "keep"
	// StepMove moves the branch from one commit to another as a
	// compare-and-swap on the first, with its working tree when it is
	// checked out.
	StepMove StepKind = "move"
	// StepRelease removes the fetched reference.
	StepRelease StepKind = "release"
)

// StepKinds returns every step kind, in the order a synchronization takes
// them. The result is a copy.
func StepKinds() []StepKind {
	return []StepKind{StepVerify, StepFetch, StepRecover, StepKeep, StepMove, StepRelease}
}

// Step is one thing a synchronization does, in exact terms: which reference,
// and from and to which commits. From is empty for a step that creates.
type Step struct {
	// Kind is what the step does.
	Kind StepKind `json:"kind"`
	// Ref is the reference it reads or writes, in full.
	Ref string `json:"ref"`
	// From is the commit the reference is required to name first, empty when
	// the step requires it absent or reads no commit of it.
	From string `json:"from,omitempty"`
	// To is the commit the reference names after the step, or for
	// StepVerify the commit it is required to name.
	To string `json:"to,omitempty"`
	// Where is the repository or remote the step reads from, for StepVerify
	// and StepFetch.
	Where string `json:"where,omitempty"`
}

// Steps is what synchronizing c does, in order, and nothing for a class that
// does nothing. gate is the gate repository the work is fetched from and
// upstream the code host's repository the pushed commit is verified on.
//
// It is the plan a person confirms, and Apply carries out exactly these
// steps in this order: Apply calls this for the same arguments rather than
// taking a list, so the plan printed and the steps taken cannot differ.
func Steps(c Classification, gate, upstream string) []Step {
	branch := "refs/heads/" + c.Branch
	fetch := []Step{
		{Kind: StepFetch, Ref: FetchedRef(c.Run), To: c.Work, Where: gate},
	}
	release := Step{Kind: StepRelease, Ref: FetchedRef(c.Run), From: c.Work}
	verify := Step{Kind: StepVerify, Ref: branch, To: c.Work, Where: upstream}
	switch c.Class {
	case ClassRecoverable:
		return slices.Concat(fetch, []Step{
			{Kind: StepRecover, Ref: RecoveredRef(c.Run), To: c.Work},
			release,
		})
	case ClassBehind:
		return slices.Concat([]Step{verify}, fetch, []Step{
			{Kind: StepMove, Ref: branch, From: c.Submitted, To: c.Work},
			release,
		})
	case ClassDivergedEquivalent:
		return slices.Concat([]Step{verify}, fetch, []Step{
			{Kind: StepKeep, Ref: ReplacedRef(c.Run), To: c.Submitted},
			{Kind: StepMove, Ref: branch, From: c.Submitted, To: c.Work},
			release,
		})
	}
	return nil
}
