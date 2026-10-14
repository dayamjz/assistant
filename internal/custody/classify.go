package custody

import (
	"context"
	"errors"

	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Facts is what a classification is decided from: the newest run of the
// branch, where that run's own record says its work stood, its isolated
// copy as it stands, and the working copy's branch. The caller reads each
// from its owner - the run's row, its last checkpoint, the copy, the working
// copy - and states nothing it did not read; this package decides from them
// and reads nothing those owners hold.
type Facts struct {
	// Run is the newest run of the branch, absent when the branch has none.
	Run *store.Run
	// Position is where the run's last checkpoint says its work stood.
	Position Position
	// Copy is the run's isolated copy as it stands now.
	Copy Copy
	// Branch is the branch the working copy has checked out, empty on a
	// detached head.
	Branch string
	// Head is the commit that branch names.
	Head string
	// Target is the branch as a reference of the working copy, the target
	// every decision about moving it is made on.
	Target safety.Target
}

// Position is what a run's last checkpoint records about its work.
type Position struct {
	// Recorded reports that a checkpoint was read and named a head. A run
	// with no checkpoint, or one naming none, has no recorded position.
	Recorded bool
	// Head is pipeline.KeyHead: the commit the run's work stood at.
	Head string
	// Pushed is pipeline.KeyPushed: the commit the run's push stage
	// forwarded, empty when it forwarded none.
	Pushed string
}

// Copy is a run's isolated copy as read now.
type Copy struct {
	// Standing reports that the copy is still there.
	Standing bool
	// Path is where it is, for a refusal's sentence.
	Path string
	// Head is the commit its HEAD stands at.
	Head string
	// Dirty and Untracked report uncommitted changes and untracked files.
	Dirty, Untracked bool
}

// Classification is a branch's class and the commits it was decided on, which
// are what its steps act on. Classify is the one producer in this build.
//
// Its fields are exported because the structured answer carries them, so a
// value can also be built by hand, and nothing here can tell one from the
// other. That is the residual gap on Apply's anchor: Submitted is the run's
// observation only when it came out of Classify, which read it off the run's
// row, and a caller that builds a Classification from a tip it just read has
// the anchor P6 names as protecting nothing, on the terms internal/safety's
// doc states for a restored observation. The one caller of Apply,
// internal/service, passes Classify's result straight through.
type Classification struct {
	// Class is the row.
	Class Class `json:"class"`
	// Run is the run the branch was classified against.
	Run string `json:"run"`
	// Branch is the branch classified.
	Branch string `json:"branch"`
	// Head is the commit the branch named when it was read.
	Head string `json:"head"`
	// Submitted is the commit the run was submitted, which is the run's
	// observation of the branch and the anchor every move is made on.
	Submitted string `json:"submitted"`
	// Work is the commit the pipeline holds for the branch: what its push
	// stage forwarded, or where its work stood. It is empty for
	// ClassPipelineHolds, which is decided before the work is read.
	Work string `json:"work,omitempty"`
	// Pushed reports that Work is what the run pushed.
	Pushed bool `json:"pushed,omitempty"`
	// Detail says why the branch is in this class, in a sentence.
	Detail string `json:"detail"`
}

// Pipeline is what Classify asks of the repository the run's work is held
// in - the gate repository - which holds the submitted commit and every
// commit a run made. It is read and nothing in it is moved: the proof's
// merge writes the merged tree's objects into its object store, which no
// reference names, and that is all.
type Pipeline interface {
	safety.Merger
	// IsAncestor reports whether ancestor is reachable from descendant.
	IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error)
}

// Branch is what Classify asks of the person's working copy. Every method
// is a read.
type Branch interface {
	// ResolveCommit resolves a revision, and an error matching
	// vcs.ErrRefNotFound when it names nothing here.
	ResolveCommit(ctx context.Context, rev string) (string, error)
	// IsAncestor reports whether ancestor is reachable from descendant.
	IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error)
	// IsShallow reports whether the history is cut short.
	IsShallow(ctx context.Context) (bool, error)
}

// Classify decides a branch's class from f, asking pipeline about the run's
// commits and branch about the working copy's, and moves nothing.
//
// The order is the decision, and every step that needs a fact refuses when
// it cannot read one rather than falling through to a later row:
//
//  1. No run, or a detached working copy, is a refusal: there is nothing to
//     classify against, or no branch to classify.
//  2. A run whose status is not finished is ClassPipelineHolds, and nothing
//     past the status is read. Which statuses are finished is
//     internal/runs' table; a status this build does not define is
//     ReasonUnrecorded here, never a finished run, because reading an
//     unknown word as "over" would open the moves to it.
//  3. A finished run with no recorded position is ReasonUnrecorded. Its
//     isolated copy, still standing, says where its work is now: a copy
//     holding uncommitted changes or untracked files is ReasonCopyUnclean,
//     and a copy at another commit than the checkpoint names is newer work
//     than the checkpoint, which is what the work is taken to be.
//  4. Work that pushed something and then moved past it is ClassRecoverable
//     on the newer work. Otherwise the work is what was pushed, or where
//     the work stood when nothing was pushed.
//  5. Work at the submitted commit is ClassYoursAlready: the run's own
//     record shows the work never moved.
//  6. Work the branch already contains is ClassYoursAlready.
//  7. Work that was not pushed is ClassRecoverable.
//  8. Pushed work over a branch that moved since the run observed it is
//     ClassDiverged: no observation anchors a move of it.
//  9. Pushed work containing the submitted commit is ClassBehind.
//  10. Otherwise the proof decides ClassDivergedEquivalent or ClassDiverged,
//     and a proof it cannot decide is a refusal that escalates.
//
// Whether the branch contains the work is asked of the working copy, which
// may not hold the work at all: a commit the working copy cannot resolve is
// taken as one its branch does not contain, and a working copy whose history
// is cut short is refused first, because there that inference does not hold.
func Classify(ctx context.Context, f Facts, pipeline Pipeline, branch Branch) (Classification, error) {
	refuse := func(reason Reason, detail string, cause error) (Classification, error) {
		return Classification{}, &Refusal{Reason: reason, Branch: f.Branch, Detail: detail, Cause: cause}
	}
	if f.Branch == "" || f.Head == "" {
		return refuse(ReasonDetached, "the working copy is on no branch, so there is no branch to reconcile", nil)
	}
	if f.Run == nil {
		return refuse(ReasonNoRun, "no run of "+f.Branch+" is recorded, so the pipeline holds nothing for it", nil)
	}
	c := Classification{Run: f.Run.ID, Branch: f.Branch, Head: f.Head, Submitted: f.Run.SubmittedHead}
	if !f.Run.Status.Recognized() {
		return refuse(ReasonUnrecorded, "run "+f.Run.ID+" has the status "+string(f.Run.Status)+
			", which this build does not define, so whether it is over is not known", nil)
	}
	if !runs.Finished(f.Run.Status) {
		c.Class = ClassPipelineHolds
		c.Detail = "run " + f.Run.ID + " of " + f.Branch + " is " + string(f.Run.Status) + " and has not finished"
		return c, nil
	}
	if !f.Position.Recorded || f.Position.Head == "" {
		return refuse(ReasonUnrecorded, "run "+f.Run.ID+" left no checkpoint naming where its work stood, so "+
			"whether it moved the work cannot be established", nil)
	}
	work := f.Position.Head
	if f.Copy.Standing {
		if f.Copy.Dirty || f.Copy.Untracked {
			return refuse(ReasonCopyUnclean, "run "+f.Run.ID+"'s copy at "+f.Copy.Path+" holds uncommitted changes or "+
				"untracked files, which no commit carries back", nil)
		}
		if f.Copy.Head != "" {
			work = f.Copy.Head
		}
	}
	pushed := f.Position.Pushed
	if pushed != "" && work != pushed {
		c.Class, c.Work = ClassRecoverable, work
		c.Detail = "run " + f.Run.ID + " moved the work past what it pushed, to " + work
		return checkContained(ctx, c, branch)
	}
	c.Work, c.Pushed = work, pushed != ""
	if work == f.Run.SubmittedHead {
		c.Class = ClassYoursAlready
		c.Detail = "run " + f.Run.ID + "'s own record shows its work never moved off " + work + ", the commit it was submitted"
		return c, nil
	}
	if !c.Pushed {
		c.Class = ClassRecoverable
		c.Detail = "run " + f.Run.ID + " moved the work from " + f.Run.SubmittedHead + " to " + work + " and pushed none of it"
		return checkContained(ctx, c, branch)
	}
	contained, err := contains(ctx, branch, work, f.Head)
	if err != nil {
		return refuse(reasonOf(err), "whether "+f.Branch+" already contains "+work+" could not be established", err)
	}
	if contained {
		return yoursAlready(c), nil
	}
	if f.Head != f.Run.SubmittedHead {
		c.Class = ClassDiverged
		c.Detail = f.Branch + " stands at " + f.Head + ", not at " + f.Run.SubmittedHead + " where run " + f.Run.ID +
			" observed it, so no observation anchors moving it to " + work
		return c, nil
	}
	behind, err := pipeline.IsAncestor(ctx, f.Run.SubmittedHead, work)
	if err != nil {
		return refuse(ReasonUnverifiable, "whether "+work+" contains "+f.Run.SubmittedHead+" could not be established", err)
	}
	if behind {
		c.Class = ClassBehind
		c.Detail = work + ", which run " + f.Run.ID + " pushed, contains " + f.Branch + "'s " + f.Head
		return c, nil
	}
	proof, err := safety.NewAdoption(pipeline).Prove(ctx, f.Target, f.Head, work)
	if err != nil {
		return refuse(ReasonUnverifiable, "whether "+f.Head+" and "+work+" are equivalent could not be decided", err)
	}
	if proof.Equivalent() {
		c.Class = ClassDivergedEquivalent
		c.Detail = "integrating " + f.Head + " into " + work + " over " + proof.Base() + " reproduces " + work + "'s own tree"
		return c, nil
	}
	c.Class = ClassDiverged
	c.Detail = "integrating " + f.Head + " into " + work + " over " + proof.Base() + " does not reproduce " + work + "'s own tree"
	if proof.Conflict() {
		c.Detail = f.Head + " and " + work + " conflict when merged over " + proof.Base()
	}
	return c, nil
}

// checkContained turns a recoverable classification into yours already when
// the branch already contains the work, so nothing is recovered that is
// already there.
func checkContained(ctx context.Context, c Classification, branch Branch) (Classification, error) {
	contained, err := contains(ctx, branch, c.Work, c.Head)
	if err != nil {
		return Classification{}, &Refusal{Reason: reasonOf(err), Branch: c.Branch,
			Detail: "whether " + c.Branch + " already contains " + c.Work + " could not be established", Cause: err}
	}
	if contained {
		return yoursAlready(c), nil
	}
	return c, nil
}

// yoursAlready is the class for a branch that already contains the work.
func yoursAlready(c Classification) Classification {
	c.Class = ClassYoursAlready
	c.Detail = c.Branch + " already contains " + c.Work + ", the work run " + c.Run + " holds for it"
	return c
}

// errShallow is the cause of a ReasonShallow refusal.
var errShallow = errors.New("custody: the working copy's history is cut short")

// contains reports whether head contains work in the working copy, reading a
// commit the working copy cannot resolve as one head does not contain once
// the history is known not to be cut short.
func contains(ctx context.Context, branch Branch, work, head string) (bool, error) {
	shallow, err := branch.IsShallow(ctx)
	if err != nil {
		return false, err
	}
	if shallow {
		return false, errShallow
	}
	if _, err := branch.ResolveCommit(ctx, work); errors.Is(err, vcs.ErrRefNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return branch.IsAncestor(ctx, work, head)
}

// reasonOf is the reason for a failed containment read.
func reasonOf(err error) Reason {
	if errors.Is(err, errShallow) {
		return ReasonShallow
	}
	return ReasonUnverifiable
}
