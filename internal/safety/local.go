package safety

import (
	"context"
	"strings"
)

// The local fast-forward: PRD section 6's approval of a local task, which
// moves the person's own default branch to the commit the worker pushed.
//
// It is Decide narrowed twice. A fast-forward is the only shape allowed:
// section 6 refuses "anything that is not a fast-forward from" the tip the
// program read, so a creation and an anchored force, which Decide allows for
// a push, are refused here with ReasonNotFastForward. And section 11's
// custody classification blocks what it blocks: the branch is moved only
// when it is checked out nowhere, or checked out in the working copy the
// update is made in with a clean tree, which is the "Behind" row's "clean
// checked-out branch". A branch checked out in another worktree, or checked
// out here over uncommitted changes or untracked files, is ReasonCheckedOut,
// because moving it would leave a working tree that no longer matches its
// branch, which reads as changes nobody made.
//
// What the custody half reads is the caller's: Checkout is a statement of
// where the branch is checked out, which the caller reads through
// internal/vcs at decision time, and this package believes it. A checkout
// that changes between that read and the update is not covered here; the
// mechanism that moves the branch is what has to refuse a tree that changed
// under it, and internal/vcs' AdvanceBranch states what it re-checks.

// Two more refusal reasons, for the local fast-forward alone.
const (
	// ReasonNotFastForward is the update would create the branch or drop
	// commits it holds. Only a fast-forward from the observed tip delivers a
	// local task.
	ReasonNotFastForward Reason = "not-fast-forward"
	// ReasonCheckedOut is the branch is checked out where moving it would
	// leave a working tree that no longer matches it: in another worktree,
	// or in this one over uncommitted changes or untracked files.
	ReasonCheckedOut Reason = "checked-out"
)

// Checkout is where the branch a local fast-forward would move is checked
// out, as the caller read it immediately before deciding.
type Checkout struct {
	// Here reports that the branch is checked out in the working copy the
	// update is made in.
	Here bool
	// Dirty and Untracked report that working copy's tree has uncommitted
	// changes or untracked files. They are read only when Here is true.
	Dirty, Untracked bool
	// Elsewhere names every other worktree that has the branch checked out.
	Elsewhere []string
}

// DecideFastForward answers whether a local task's approved fast-forward may
// proceed, on the terms this file's opening comment states: Decide, then the
// two narrowings. Every refusal is a *Refusal matching ErrRefused, and a
// refusal Decide made is returned as Decide made it.
//
// The anchor rule is Decide's, and P6's trap applies to it unchanged: an
// anchor observed immediately before this call protects nothing. The caller
// observes the branch when it offers the change for approval and decides
// against that observation when the approval arrives.
func (g *Guard) DecideFastForward(ctx context.Context, u Update, c Checkout) (Decision, error) {
	decision, err := g.Decide(ctx, u)
	if err != nil {
		return Decision{}, err
	}
	if decision.Kind() != KindFastForward {
		detail := "the branch was absent when it was observed, and a local landing moves a branch that exists"
		if decision.Kind() == KindAnchoredForce {
			detail = "the proposed commit does not contain the branch's tip, so moving it would drop commits it holds"
		}
		return Decision{}, &Refusal{
			Reason: ReasonNotFastForward, Target: u.Target, Anchor: u.Anchor, Observed: u.Anchor.State(),
			Discarded: decision.Rewritten(), Detail: detail,
		}
	}
	if err := refuseCheckout(u, c, "a fast-forward"); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

// refuseCheckout is the custody half of a local move, shared by every
// decision that moves a person's branch: a branch checked out elsewhere, or
// checked out here over uncommitted changes or untracked files, is
// ReasonCheckedOut, and anything else is nil. move names the kind of move in
// the refusal's sentence. One function answers it so the rule cannot be
// relaxed for one kind of move and kept for another.
func refuseCheckout(u Update, c Checkout, move string) error {
	refuse := func(detail string) error {
		return &Refusal{
			Reason: ReasonCheckedOut, Target: u.Target, Anchor: u.Anchor, Observed: u.Anchor.State(), Detail: detail,
		}
	}
	switch {
	case len(c.Elsewhere) > 0:
		return refuse("the branch is checked out in " + strings.Join(c.Elsewhere, ", ") +
			", whose working tree would no longer match it")
	case c.Here && c.Dirty:
		return refuse("the branch is checked out with uncommitted changes, which " + move + " would leave over a different commit")
	case c.Here && c.Untracked:
		return refuse("the branch is checked out with untracked files, which the files " + move + " brings could collide with")
	}
	return nil
}
