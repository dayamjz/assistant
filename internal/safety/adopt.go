package safety

import (
	"context"
	"errors"
	"strings"

	"github.com/dayamjz/assistant/internal/vcs"
)

// Adopting a diverged head: PRD section 11's "diverged but equivalent" row,
// which is the one local move this package allows that is not a
// fast-forward.
//
// Section 11 states the proof it requires: an executable three-way merge
// whose result matches the preserved tree, anchored on the merge base, and
// patch identity is not sufficient. Prove is that proof and nothing looser.
// It names the one merge base of the branch's commit and the proposed
// commit, refusing when there is none or more than one, merges the branch's
// commit into the proposed one over that base, and calls the two equivalent
// only when the merged tree is the proposed commit's own tree: integrating
// what the branch holds into what would replace it changes nothing, so
// adopting the replacement drops no content the branch had.
//
// What that buys over patch identity is the part section 11 names. An edit
// textually like one of the branch's, made at another place in a file, is a
// different change, and the merge applies the branch's edit at its own place
// and produces a tree that is not the proposed one; that is "not
// equivalent", never "already there". An edit that differs only in
// whitespace from the branch's meets it on the same lines, and the merge
// conflicts, which is "not equivalent" as well.
//
// Anything the proof cannot decide is a refusal rather than an answer
// either way: a commit that does not resolve here, two histories with no
// common ancestor, two with more than one best common ancestor, and a git
// that could not run the merge. A refusal is ReasonUnverifiable or
// ReasonUnrelatedHistories, on Decide's terms.
//
// The residual gaps are stated rather than implied away. The proof is about
// content: two histories whose trees satisfy it may still differ in commit
// messages, authorship and how the work was split into commits, and adopting
// one replaces the other's history; section 11 has the replaced head
// anchored under a recovery reference first, and that write is the caller's,
// because nothing here writes. And the merge is git's: a file whose merge
// git resolves by a configured driver is merged the way that driver says.

// ReasonNotEquivalent is the branch's commit and the proposed commit are not
// equivalent by the three-way merge section 11 requires: integrating the
// branch's commit into the proposed one would change it, or the two conflict.
// The refusal names the commits the branch holds that the proposed commit
// does not contain, which is what adopting it would drop.
const ReasonNotEquivalent Reason = "not-equivalent"

// Merger is the mechanism the proof runs on: Git, and the three questions a
// three-way merge over a named base asks. *vcs.Repository satisfies it, and a
// compile-time assertion in this package's tests fails the build when it
// stops doing so.
//
// It is an interface of its own rather than three more methods on Git
// because Decide and Incorporation never ask them, and the fake those two are
// tested against would otherwise have to state a merge's result outright,
// which is a shape no real merge produces on demand. The proof is tested over
// real repositories instead.
type Merger interface {
	Git
	// MergeBases returns every best common ancestor of two commits, and an
	// error matching vcs.ErrNoMergeBase when they share none.
	MergeBases(ctx context.Context, a, b string) ([]string, error)
	// MergeTreeAt returns the tree a three-way merge of ours and theirs over
	// base produces, and an error matching vcs.ErrMergeConflict when the
	// merge does not resolve cleanly.
	MergeTreeAt(ctx context.Context, base, ours, theirs string) (string, error)
	// TreeOf returns the tree a commit records.
	TreeOf(ctx context.Context, rev string) (string, error)
}

// Equivalence is what Prove established about two commits: the base the
// merge was anchored on, the tree the merge produced, the tree it was
// compared against, and the verdict. Its fields are unexported and only
// Prove produces one, so a verdict cannot be assembled by a caller.
type Equivalence struct {
	base, current, proposed string
	merged, preserved       string
	conflict, equivalent    bool
	proved                  bool
}

// Equivalent reports whether the merge reproduced the proposed tree. The zero
// Equivalence reports false.
func (e Equivalence) Equivalent() bool { return e.proved && e.equivalent }

// Conflict reports that the merge did not resolve cleanly, which is a reason
// the two are not equivalent rather than a failure to decide.
func (e Equivalence) Conflict() bool { return e.conflict }

// Base is the merge base the merge was anchored on.
func (e Equivalence) Base() string { return e.base }

// Current is the branch's commit, the side whose content the proof asks
// about.
func (e Equivalence) Current() string { return e.current }

// Proposed is the commit whose tree is preserved: the one that would
// replace Current.
func (e Equivalence) Proposed() string { return e.proposed }

// Merged is the tree the merge produced, empty on a conflict.
func (e Equivalence) Merged() string { return e.merged }

// Preserved is the proposed commit's own tree, which Merged is compared
// against.
func (e Equivalence) Preserved() string { return e.preserved }

// Adoption answers the two questions of section 11's adoption: whether two
// commits are equivalent, and whether a branch may be moved to the proposed
// one on an anchor. It reads through a Merger and moves nothing.
type Adoption struct {
	git   Merger
	guard *Guard
}

// NewAdoption returns an Adoption over m. It panics when m is nil, on New's
// terms.
func NewAdoption(m Merger) *Adoption {
	if m == nil {
		panic("safety: NewAdoption requires a Merger implementation")
	}
	return &Adoption{git: m, guard: New(m)}
}

// Prove runs section 11's proof over current, the branch's commit, and
// proposed, the commit that would replace it, on this file's opening
// comment's terms. target is the branch the answer is about, and is what a
// refusal names. A verdict either way is an Equivalence and a nil error; a
// proof that could not be decided is a *Refusal and no verdict.
func (a *Adoption) Prove(ctx context.Context, target Target, current, proposed string) (Equivalence, error) {
	refuse := func(reason Reason, detail string, cause error) (Equivalence, error) {
		return Equivalence{}, &Refusal{Reason: reason, Target: target, Detail: detail, Cause: cause}
	}
	cur, err := a.git.ResolveCommit(ctx, current)
	if err != nil {
		return refuse(ReasonUnverifiable, resolveCommitDetail("the branch's commit "+current, "here", err,
			"so its content cannot be compared"), err)
	}
	prop, err := a.git.ResolveCommit(ctx, proposed)
	if err != nil {
		return refuse(ReasonUnverifiable, resolveCommitDetail("the proposed commit "+proposed, "here", err,
			"so its content cannot be compared"), err)
	}
	bases, err := a.git.MergeBases(ctx, cur, prop)
	switch {
	case errors.Is(err, vcs.ErrNoMergeBase):
		return refuse(ReasonUnrelatedHistories, cur+" and "+prop+" share no common ancestor, so there is no base to anchor the merge on", err)
	case err != nil:
		return refuse(ReasonUnverifiable, "the merge base of "+cur+" and "+prop+" could not be determined", err)
	case len(bases) != 1:
		return refuse(ReasonUnverifiable, cur+" and "+prop+" have more than one best common ancestor ("+
			strings.Join(bases, ", ")+"), so no one base anchors the merge and the proof escalates", nil)
	}
	preserved, err := a.git.TreeOf(ctx, prop)
	if err != nil {
		return refuse(ReasonUnverifiable, "the tree of "+prop+" could not be read", err)
	}
	e := Equivalence{base: bases[0], current: cur, proposed: prop, preserved: preserved, proved: true}
	merged, err := a.git.MergeTreeAt(ctx, bases[0], prop, cur)
	switch {
	case errors.Is(err, vcs.ErrMergeConflict):
		e.conflict = true
		return e, nil
	case err != nil:
		return refuse(ReasonUnverifiable, "the merge of "+cur+" into "+prop+" over "+bases[0]+" could not be run", err)
	}
	e.merged = merged
	e.equivalent = merged == preserved
	return e, nil
}

// Decide answers whether u may move a person's branch from the anchor to the
// proposed commit when the proposed commit does not contain it: Guard.Decide
// first, then the custody half DecideFastForward also applies, then the
// proof over the anchor's commit and the proposed commit. A refusal from
// Decide is returned as Decide made it; a branch that was absent is
// ReasonNotFastForward, since there is nothing to adopt over; a proof that
// finds the two not equivalent is ReasonNotEquivalent naming what the move
// would drop.
//
// A proposal that is a fast-forward passes the proof trivially and is
// allowed the same way, so a caller is never told to take the weaker path
// for want of the stronger one.
//
// The anchor rule is Decide's and the same trap applies: the caller anchors
// on a commit its run observed, never on the tip read a moment before. The
// proof is taken against that anchor's commit, which Decide has just
// confirmed the branch still names.
func (a *Adoption) Decide(ctx context.Context, u Update, c Checkout) (Decision, error) {
	decision, err := a.guard.Decide(ctx, u)
	if err != nil {
		return Decision{}, err
	}
	if decision.Kind() == KindCreate {
		return Decision{}, &Refusal{
			Reason: ReasonNotFastForward, Target: u.Target, Anchor: u.Anchor, Observed: u.Anchor.State(),
			Detail: "the branch was absent when it was observed, so there is no head to adopt over",
		}
	}
	if err := refuseCheckout(u, c, "adopting the proposed commit"); err != nil {
		return Decision{}, err
	}
	proof, err := a.Prove(ctx, u.Target, u.Anchor.State().Commit, decision.Proposed())
	if err != nil {
		var refusal *Refusal
		if errors.As(err, &refusal) {
			refusal.Anchor, refusal.Observed = u.Anchor, u.Anchor.State()
		}
		return Decision{}, err
	}
	if !proof.Equivalent() {
		detail := "integrating " + proof.Current() + " into " + proof.Proposed() + " over " + proof.Base() +
			" produces " + proof.Merged() + ", not " + proof.Proposed() + "'s own tree " + proof.Preserved()
		if proof.Conflict() {
			detail = proof.Current() + " and " + proof.Proposed() + " conflict when merged over " + proof.Base()
		}
		return Decision{}, &Refusal{
			Reason: ReasonNotEquivalent, Target: u.Target, Anchor: u.Anchor, Observed: u.Anchor.State(),
			Discarded: decision.Rewritten(), Detail: detail,
		}
	}
	return decision, nil
}
