package custody

import (
	"errors"
	"slices"
)

// Class is one row of PRD section 11's classification: what the pipeline
// holds for a person's branch, relative to the branch, and so what may be
// done about it. The set is closed; Classes lists it.
type Class string

// The six classes, in section 11's order.
const (
	// ClassPipelineHolds is a run of the branch that has not finished. The
	// next action is to wait for it, or answer it where it waits on a
	// person; nothing here changes the branch meanwhile.
	ClassPipelineHolds Class = "pipeline-holds"
	// ClassRecoverable is a finished run that moved the branch's work on and
	// did not push what it moved it to, or moved it past what it pushed.
	// Recovery brings that work back into the working copy under a recovery
	// reference and leaves the branch where it is.
	ClassRecoverable Class = "recoverable"
	// ClassYoursAlready is a finished run whose recorded position shows the
	// work never moved off the commit the run was submitted, or whose work
	// the branch already contains. Nothing is done.
	ClassYoursAlready Class = "yours-already"
	// ClassBehind is a branch still at the commit the run was submitted,
	// which the commit the run pushed contains. The branch is fast-forwarded
	// to it after the code host is read again and still names it.
	ClassBehind Class = "behind"
	// ClassDivergedEquivalent is a branch still at the commit the run was
	// submitted, which the pushed commit does not contain, and the two are
	// equivalent by internal/safety's proof. The branch's head is kept under
	// a recovery reference, and then the branch is moved to the pushed
	// commit.
	ClassDivergedEquivalent Class = "diverged-equivalent"
	// ClassDiverged is every divergence the proof does not clear: the
	// contents differ, or the branch moved since the run observed it, so no
	// observation anchors a move. Nothing changes and the classification
	// says why.
	ClassDiverged Class = "diverged"
)

// classes is the closed set, in the order Classes reports.
var classes = []Class{
	ClassPipelineHolds, ClassRecoverable, ClassYoursAlready, ClassBehind, ClassDivergedEquivalent, ClassDiverged,
}

// Classes returns the closed set in section 11's order. The result is a copy.
func Classes() []Class { return slices.Clone(classes) }

// Moves reports whether synchronizing a branch of this class moves the
// branch: true for ClassBehind and ClassDivergedEquivalent and for nothing
// else. Recovery writes a reference beside the branch and never moves it.
func (c Class) Moves() bool { return c == ClassBehind || c == ClassDivergedEquivalent }

// Acts reports whether synchronizing a branch of this class does anything at
// all: Moves, and ClassRecoverable's recovery reference.
func (c Class) Acts() bool { return c.Moves() || c == ClassRecoverable }

// Reason says why custody refused, either to classify a branch or to carry
// out what its classification allows. Every reason is a fact that could not
// be established or a move that was refused, never a preference.
type Reason string

const (
	// ReasonNoRun is no run of this branch is recorded, so the pipeline
	// holds nothing to reconcile it with.
	ReasonNoRun Reason = "no-run"
	// ReasonDetached is the working copy is on no branch.
	ReasonDetached Reason = "detached"
	// ReasonUnrecorded is the run's own record does not say where its work
	// stood: it left no checkpoint, the checkpoint names no head, or the
	// run's status is one this build does not define. Yours already needs
	// that positively, and so does every other row of a finished run.
	ReasonUnrecorded Reason = "unrecorded"
	// ReasonCopyUnclean is the run's isolated copy holds uncommitted changes
	// or untracked files, which no commit carries and so nothing here can
	// bring back or rule out.
	ReasonCopyUnclean Reason = "copy-unclean"
	// ReasonShallow is the working copy's history is cut short, so a commit
	// it cannot resolve may still be one its branch contains.
	ReasonShallow Reason = "shallow"
	// ReasonUnverifiable is a fact the classification or the move depends on
	// could not be read. It is refused rather than assumed.
	ReasonUnverifiable Reason = "unverifiable"
	// ReasonNotCurrent is the code host, read again before a move, does not
	// name the commit the run pushed for this branch, so the commit the move
	// would adopt is not freshly verified.
	ReasonNotCurrent Reason = "pushed-not-current"
	// ReasonRecoveryTaken is a recovery reference this would write already
	// exists and names another commit. Nothing replaces it.
	ReasonRecoveryTaken Reason = "recovery-taken"
	// ReasonRefused is internal/safety or the mechanism refused the move:
	// the branch moved since it was observed, its checkout would be left
	// not matching it, or the proof did not hold. The cause carries which.
	ReasonRefused Reason = "refused"
	// ReasonNotSynchronizable is the classification allows no action, so
	// carrying it out was refused rather than reported as done.
	ReasonNotSynchronizable Reason = "not-synchronizable"
)

// reasons is the closed set of refusal reasons, in the order Reasons
// reports.
var reasons = []Reason{
	ReasonNoRun, ReasonDetached, ReasonUnrecorded, ReasonCopyUnclean, ReasonShallow, ReasonUnverifiable,
	ReasonNotCurrent, ReasonRecoveryTaken, ReasonRefused, ReasonNotSynchronizable,
}

// Reasons returns every refusal reason. The result is a copy.
func Reasons() []Reason { return slices.Clone(reasons) }

// ErrRefused matches every refusal this package produces.
var ErrRefused = errors.New("custody: refused")

// Refusal is custody declining to classify a branch or to carry out a
// classification, as a typed result: the reason, the branch, a sentence a
// caller can report without adding to it, and the failure it came from.
// Nothing was written when a classification is refused; what an apply had
// written before it was refused is in its Result.
type Refusal struct {
	// Reason is the category.
	Reason Reason
	// Branch is the branch the refusal is about, empty when none was read.
	Branch string
	// Detail is what could not be established or what was refused.
	Detail string
	// Cause is the underlying failure, a *safety.Refusal among them.
	Cause error
}

// Error renders the refusal with its reason and detail.
func (r *Refusal) Error() string {
	text := "custody: refused (" + string(r.Reason) + ")"
	if r.Branch != "" {
		text += " for " + r.Branch
	}
	text += ": " + r.Detail
	if r.Cause != nil {
		text += ": " + r.Cause.Error()
	}
	return text
}

// Unwrap makes every refusal match ErrRefused and its cause alike, so
// errors.As reaches a *safety.Refusal carried as the cause.
func (r *Refusal) Unwrap() []error {
	if r.Cause == nil {
		return []error{ErrRefused}
	}
	return []error{ErrRefused, r.Cause}
}
