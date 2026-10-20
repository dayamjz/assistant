package safety_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// TestDecideRefusesToDropACommitTheTargetHeldThatTheRunNeverHad is issue
// #396 at the decision: the run observed the branch standing at a commit
// somebody else pushed, x, and its proposed commit does not contain x. The
// target has not moved, so the anchor describes it and a lease on it would
// hold; that is P6's named trap, and the basis rule is what refuses.
//
// Both shapes the issue names are here: the run's commit diverged from x
// (both sit on c1), and the run's commit standing behind x, an ancestor of
// it, so the update would move the branch backwards. In both the refusal is
// would-discard, names exactly x, and carries as its anchor the commit the
// run observed, which is x itself: the assertion is on the anchor value and
// not only on the outcome, because an anchor read a moment before the push
// would name the same commit here and the outcome alone cannot tell them
// apart.
//
// The positive control is the same history with x in the run's basis, which
// is a branch the run was given and rewrote: the same decision is an anchored
// force dropping x. A rule that refused every anchored force, or that never
// read the basis, fails there.
func TestDecideRefusesToDropACommitTheTargetHeldThatTheRunNeverHad(t *testing.T) {
	principles.Cite(t, principles.P6)
	t.Parallel()
	for _, tc := range []struct {
		name     string
		parents  map[string][]string
		proposed string
	}{
		{"diverged", map[string][]string{"c1": nil, "x": {"c1"}, "m": {"c1"}}, "m"},
		{"behind", map[string][]string{"c1": nil, "m": {"c1"}, "x": {"m"}}, "m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			git := &fakeGit{parents: tc.parents, advertised: map[string][][]advert{remote: {{branch(ref, "x")}}}}
			guard := safety.New(git)
			obs := observe(t, guard)

			refusal := refusalFor(t, guard, safety.Update{
				Target: target, Proposed: tc.proposed, Anchor: obs, Basis: []string{tc.proposed},
			})
			if refusal.Reason != safety.ReasonWouldDiscard {
				t.Fatalf("Reason = %s, want %s: %v", refusal.Reason, safety.ReasonWouldDiscard, refusal)
			}
			if !slices.Equal(refusal.Discarded, []string{"x"}) {
				t.Fatalf("Discarded = %v, want exactly [x], the commit the run never had", refusal.Discarded)
			}
			if got := refusal.Anchor.State(); got != (safety.RemoteState{Exists: true, Commit: "x"}) {
				t.Fatalf("the refusal's anchor is %v, want the observation at x", got)
			}
			if got := refusal.Observed; got != (safety.RemoteState{Exists: true, Commit: "x"}) {
				t.Fatalf("the refusal's fresh read is %v, want x: the target did not move", got)
			}
			for _, want := range []string{"holds commits that", "does not contain", "would discard x"} {
				if !strings.Contains(refusal.Error(), want) {
					t.Fatalf("the refusal does not say %q: %v", want, refusal)
				}
			}

			// The control: x is in what the run worked from.
			decision, err := guard.Decide(context.Background(), safety.Update{
				Target: target, Proposed: tc.proposed, Anchor: obs, Basis: []string{tc.proposed, "x"},
			})
			if err != nil {
				t.Fatalf("Decide over a basis holding x: %v", err)
			}
			if decision.Kind() != safety.KindAnchoredForce || !slices.Equal(decision.Rewritten(), []string{"x"}) {
				t.Fatalf("the control is %v dropping %v, want an anchored force dropping x",
					decision.Kind(), decision.Rewritten())
			}
			if got := decision.Anchor().State().Commit; got != "x" {
				t.Fatalf("the control's lease is on %q, want the observed x", got)
			}
		})
	}
}

// TestDecideNamesOnlyTheCommitsNoBasisCommitContains is the case where the
// run rewrote history it was given and somebody else also pushed on top of it
// before the run observed the branch. The run was submitted s3, on s2, which
// the branch already held; a colleague pushed x3 on s2; the run rebased onto
// a newer base, b2, producing r. The update would drop s2 and x3. s2 is the
// run's own history being rewritten; x3 is not.
//
// The refusal names x3 and not s2, so a person at the hold is shown what
// would be lost rather than what the rebase replaced. The control without x3
// is the same branch standing at s2, which the run's basis contains, and is
// allowed as an anchored force dropping s2.
func TestDecideNamesOnlyTheCommitsNoBasisCommitContains(t *testing.T) {
	principles.Cite(t, principles.P6)
	t.Parallel()
	parents := map[string][]string{
		"c1": nil, "b2": {"c1"}, "s2": {"c1"}, "s3": {"s2"}, "x3": {"s2"}, "r": {"b2"},
	}
	for _, tc := range []struct {
		name    string
		tip     string
		refused []string
	}{
		{"somebody pushed on top of what the run was given", "x3", []string{"x3"}},
		{"control: the branch holds only what the run was given", "s2", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			git := &fakeGit{parents: parents, advertised: map[string][][]advert{remote: {{branch(ref, tc.tip)}}}}
			guard := safety.New(git)
			obs := observe(t, guard)
			u := safety.Update{Target: target, Proposed: "r", Anchor: obs, Basis: []string{"s3"}}
			if tc.refused != nil {
				refusal := refusalFor(t, guard, u)
				if refusal.Reason != safety.ReasonWouldDiscard || !slices.Equal(refusal.Discarded, tc.refused) {
					t.Fatalf("the refusal is %s discarding %v, want %s discarding %v",
						refusal.Reason, refusal.Discarded, safety.ReasonWouldDiscard, tc.refused)
				}
				return
			}
			decision, err := guard.Decide(context.Background(), u)
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if decision.Kind() != safety.KindAnchoredForce || !slices.Equal(decision.Rewritten(), []string{"s2"}) {
				t.Fatalf("the control is %v dropping %v, want an anchored force dropping s2",
					decision.Kind(), decision.Rewritten())
			}
		})
	}
}

// TestDecideFailsClosedOnABasisItCannotRead holds the basis rule to P6's
// fail-closed stance: an empty basis refuses every update that drops a commit,
// and a basis commit that does not resolve, or a comparison against it that
// cannot be answered, is unverifiable rather than a basis that contained
// nothing or everything. A fast-forward reads no basis at all, which is the
// control: the rule narrows the anchored force and nothing else.
func TestDecideFailsClosedOnABasisItCannotRead(t *testing.T) {
	t.Parallel()
	forced := map[string][]string{"c1": nil, "c2": {"c1"}, "r2": {"c1"}}

	t.Run("an empty basis", func(t *testing.T) {
		t.Parallel()
		git := &fakeGit{parents: forced, advertised: map[string][][]advert{remote: {{branch(ref, "c2")}}}}
		guard := safety.New(git)
		refusal := refusalFor(t, guard, safety.Update{Target: target, Proposed: "r2", Anchor: observe(t, guard)})
		if refusal.Reason != safety.ReasonWouldDiscard || !slices.Equal(refusal.Discarded, []string{"c2"}) {
			t.Fatalf("the refusal is %s discarding %v, want would-discard discarding c2", refusal.Reason, refusal.Discarded)
		}
	})
	t.Run("a basis commit that does not resolve", func(t *testing.T) {
		t.Parallel()
		git := &fakeGit{parents: forced, advertised: map[string][][]advert{remote: {{branch(ref, "c2")}}}}
		guard := safety.New(git)
		refusal := refusalFor(t, guard, safety.Update{
			Target: target, Proposed: "r2", Anchor: observe(t, guard), Basis: []string{"nowhere"},
		})
		if refusal.Reason != safety.ReasonUnverifiable || !errors.Is(refusal, vcs.ErrRefNotFound) {
			t.Fatalf("the refusal is %s (%v), want unverifiable wrapping ErrRefNotFound", refusal.Reason, refusal)
		}
		if len(refusal.Discarded) != 0 {
			t.Fatalf("an unverifiable refusal names %v as discarded, which reads as a comparison that was made",
				refusal.Discarded)
		}
	})
	t.Run("control: a fast-forward reads no basis", func(t *testing.T) {
		t.Parallel()
		git := &fakeGit{parents: linear("c1", "c2", "c3"), advertised: map[string][][]advert{remote: {{branch(ref, "c2")}}}}
		guard := safety.New(git)
		decision, err := guard.Decide(context.Background(), safety.Update{
			Target: target, Proposed: "c3", Anchor: observe(t, guard), Basis: []string{"nowhere"},
		})
		if err != nil || decision.Kind() != safety.KindFastForward {
			t.Fatalf("Decide = %v, %v, want a fast-forward", decision, err)
		}
	})
}
