package custody

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Working is what custody asks of a person's working copy to carry out a
// classification. *vcs.Repository satisfies it, and this package's tests
// fail the build when it stops doing so.
//
// Its writes are exactly four, and they are the whole of what custody can do
// to a working copy: Fetch, which custody gives a refspec under Namespace
// alone; CreateRef, which never replaces; DeleteRef, which custody gives the
// fetched reference alone; and MoveBranch, the compare-and-swap a decision
// from internal/safety is carried out with. Nothing here stashes, merges,
// rebases, switches a branch, deletes a branch, or writes to a remote, and
// nothing in this interface could.
type Working interface {
	safety.Merger
	// IsAncestor reports whether ancestor is reachable from descendant.
	IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error)
	// IsShallow reports whether the history is cut short.
	IsShallow(ctx context.Context) (bool, error)
	// Path is the working copy's root, which is the remote a decision's
	// fresh read is taken against.
	Path() string
	// Inventory reads the working copy's references, HEAD and tree state.
	Inventory(ctx context.Context) (vcs.Inventory, error)
	// ListWorktrees lists every worktree of the repository.
	ListWorktrees(ctx context.Context) ([]vcs.WorktreeInfo, error)
	// Fetch fetches from a remote into references the refspecs name.
	Fetch(ctx context.Context, spec vcs.FetchSpec) error
	// CreateRef writes a reference that does not exist, and refuses with an
	// error matching vcs.ErrRefExists when it does.
	CreateRef(ctx context.Context, name, commit string) error
	// DeleteRef removes a reference.
	DeleteRef(ctx context.Context, name string) error
	// MoveBranch moves a branch from one commit to another as a
	// compare-and-swap on the first.
	MoveBranch(ctx context.Context, branch, from, to, reason string) error
}

// TargetOf is the target a decision about moving branch in the working copy
// at working is made on: the branch, read from the working copy itself.
func TargetOf(working, branch string) safety.Target {
	return safety.Target{Remote: working, Ref: "refs/heads/" + branch}
}

// ReadCheckout reads where branch is checked out, which is the custody half
// of every local move internal/safety decides: in w itself, with its tree's
// state, and in any other worktree of the same repository. It reads and
// writes nothing.
func ReadCheckout(ctx context.Context, w Working, branch string) (safety.Checkout, error) {
	inv, err := w.Inventory(ctx)
	if err != nil {
		return safety.Checkout{}, fmt.Errorf("custody: reading %s: %w", w.Path(), err)
	}
	c := safety.Checkout{Here: inv.Head.Branch == branch, Dirty: inv.Dirty, Untracked: inv.Untracked}
	worktrees, err := w.ListWorktrees(ctx)
	if err != nil {
		return safety.Checkout{}, fmt.Errorf("custody: listing the worktrees of %s: %w", w.Path(), err)
	}
	here := canonical(w.Path())
	for _, wt := range worktrees {
		if !wt.Bare && wt.Branch == branch && canonical(wt.Path) != here {
			c.Elsewhere = append(c.Elsewhere, wt.Path)
		}
	}
	return c, nil
}

// canonical is a path with its links resolved, or cleaned when it cannot be
// resolved, so the working copy is not counted as a worktree other than
// itself.
func canonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// FastForward moves branch in w to u.Proposed when internal/safety's
// DecideFastForward allows it on u's anchor and the checkout ReadCheckout
// reads, and records reason in the branch's reflog. It returns the decision
// it moved on.
//
// It is the one fast-forward of a person's branch in this build: section 11's
// behind row and section 6's approved local landing both move through it, so
// the custody rule DecideFastForward applies and the compare-and-swap
// MoveBranch makes are taken the same way for both. A refusal is returned as
// it came: a *safety.Refusal from the decision, or an error matching
// vcs.ErrBranchMoved or vcs.ErrTreeNotClean from the move.
func FastForward(ctx context.Context, w Working, u safety.Update, branch, reason string) (safety.Decision, error) {
	checkout, err := ReadCheckout(ctx, w, branch)
	if err != nil {
		return safety.Decision{}, err
	}
	decision, err := safety.New(w).DecideFastForward(ctx, u, checkout)
	if err != nil {
		return safety.Decision{}, err
	}
	if err := w.MoveBranch(ctx, branch, decision.Anchor().State().Commit, decision.Proposed(), reason); err != nil {
		return safety.Decision{}, err
	}
	return decision, nil
}

// Result is what Apply did: the steps it carried out, in order. On a refusal
// it is what was done before the refusal. The only step after a move is
// removing the fetched reference, so a Result holding a move beside a
// refusal is a branch that did move and a scratch reference that could not
// be removed.
type Result struct {
	// Done is every step carried out.
	Done []Step
}

// Apply carries out Steps(c, gate, upstream) in the working copy w, in order,
// and refuses a classification that allows no action. It derives the steps
// itself rather than taking a list, so what it carries out can only be the
// plan Steps prints for the same classification: no caller can hand it a
// fetch into a branch or a move the plan never named.
//
// Every move is anchored to c.Submitted, the commit the run was submitted,
// which is the run's own observation of the branch, and never to the tip
// read a moment before: the anchor is restored from the run's record through
// safety.RestoreObservedFromCheckpoint, and a branch that has moved off it
// since is refused by Decide's own rule. The decision is taken once, before
// the first step that writes something a move depends on - the recovery
// reference an adoption writes, or the move itself - so a refused decision
// leaves no recovery reference for a move that never happened.
//
// The fetched reference is removed whether the steps finish or are refused,
// and a failure to remove it after a refusal does not replace the refusal.
func Apply(ctx context.Context, w Working, c Classification, gate, upstream string) (Result, error) {
	steps := Steps(c, gate, upstream)
	var result Result
	refuse := func(reason Reason, detail string, cause error) (Result, error) {
		return result, &Refusal{Reason: reason, Branch: c.Branch, Detail: detail, Cause: cause}
	}
	if !c.Class.Acts() {
		return refuse(ReasonNotSynchronizable, "a branch that is "+string(c.Class)+" has nothing to synchronize", nil)
	}
	target := TargetOf(w.Path(), c.Branch)
	fetched := ""
	defer func() {
		if fetched != "" {
			_ = w.DeleteRef(context.WithoutCancel(ctx), fetched)
		}
	}()
	var decision *safety.Decision
	decide := func() error {
		if decision != nil {
			return nil
		}
		anchor, err := safety.RestoreObservedFromCheckpoint(safety.ObservationRecord{
			Remote: target.Remote, Ref: target.Ref, Exists: true, Commit: c.Submitted,
		})
		if err != nil {
			return &Refusal{Reason: ReasonUnverifiable, Branch: c.Branch, Detail: "the run's observation of the branch could not be restored", Cause: err}
		}
		u := safety.Update{Target: target, Proposed: c.Work, Anchor: anchor}
		checkout, err := ReadCheckout(ctx, w, c.Branch)
		if err != nil {
			return &Refusal{Reason: ReasonUnverifiable, Branch: c.Branch, Detail: "where the branch is checked out could not be read", Cause: err}
		}
		var d safety.Decision
		if c.Class == ClassBehind {
			d, err = safety.New(w).DecideFastForward(ctx, u, checkout)
		} else {
			d, err = safety.NewAdoption(w).Decide(ctx, u, checkout)
		}
		if err != nil {
			return &Refusal{Reason: ReasonRefused, Branch: c.Branch, Detail: "moving the branch was refused", Cause: err}
		}
		decision = &d
		return nil
	}
	for _, step := range steps {
		switch step.Kind {
		case StepVerify:
			if err := verify(ctx, w, c.Branch, step); err != nil {
				return result, err
			}
		case StepFetch:
			fetched = step.Ref
			if err := w.Fetch(ctx, vcs.FetchSpec{Remote: step.Where, Refspecs: []string{"+" + step.To + ":" + step.Ref}}); err != nil {
				return refuse(ReasonUnverifiable, "fetching "+step.To+" from the gate repository failed", err)
			}
			if got, err := w.ResolveCommit(ctx, step.Ref); err != nil || got != step.To {
				return refuse(ReasonUnverifiable, "the fetch of "+step.To+" did not land it under "+step.Ref, err)
			}
		case StepRecover, StepKeep:
			if step.Kind == StepKeep {
				if err := decide(); err != nil {
					return result, err
				}
				if decision.Anchor().State().Commit != step.To {
					return refuse(ReasonUnverifiable, "the head kept before the move is not the head the move is anchored on", nil)
				}
			}
			if err := w.CreateRef(ctx, step.Ref, step.To); err != nil {
				if !errors.Is(err, vcs.ErrRefExists) {
					return refuse(ReasonUnverifiable, "writing "+step.Ref+" failed", err)
				}
				if existing, readErr := w.ResolveCommit(ctx, step.Ref); readErr != nil || existing != step.To {
					return refuse(ReasonRecoveryTaken, step.Ref+" already exists and does not name "+step.To, err)
				}
			}
		case StepMove:
			if err := decide(); err != nil {
				return result, err
			}
			if decision.Anchor().State().Commit != step.From || decision.Proposed() != step.To {
				return refuse(ReasonUnverifiable, "the move decided is not the move planned", nil)
			}
			reason := "assistant: sync " + string(c.Class) + " to the work of run " + c.Run
			if err := w.MoveBranch(ctx, c.Branch, step.From, step.To, reason); err != nil {
				return refuse(ReasonRefused, "moving "+c.Branch+" from "+step.From+" to "+step.To+" was refused", err)
			}
		case StepRelease:
			if err := w.DeleteRef(ctx, step.Ref); err != nil {
				return refuse(ReasonUnverifiable, "removing "+step.Ref+" failed", err)
			}
			fetched = ""
		default:
			return refuse(ReasonUnverifiable, "the plan names a step of kind "+string(step.Kind)+", which this build does not carry out", nil)
		}
		result.Done = append(result.Done, step)
	}
	return result, nil
}

// verify reads the code host's branch and requires it to name the pushed
// commit and nothing else. It reads what the remote advertises and writes
// nothing, here or there.
func verify(ctx context.Context, w Working, branch string, step Step) error {
	refuse := func(reason Reason, detail string, cause error) error {
		return &Refusal{Reason: reason, Branch: branch, Detail: detail, Cause: cause}
	}
	if step.Where == "" {
		return refuse(ReasonUnverifiable, "the repository record names no upstream, so the pushed commit cannot be verified", nil)
	}
	refs, err := w.RemoteRefs(ctx, step.Where, step.Ref)
	if err != nil {
		return refuse(ReasonUnverifiable, "the code host's "+step.Ref+" could not be read", err)
	}
	var found []vcs.Ref
	for _, ref := range refs {
		if ref.Name == step.Ref {
			found = append(found, ref)
		}
	}
	if len(found) != 1 || found[0].Object != step.To || found[0].Commit != step.To {
		names := "nothing"
		if len(found) == 1 {
			names = found[0].Object
		} else if len(found) > 1 {
			names = "more than one object"
		}
		return refuse(ReasonNotCurrent, "the code host's "+step.Ref+" names "+names+", not "+step.To+", the commit the run pushed", nil)
	}
	return nil
}
