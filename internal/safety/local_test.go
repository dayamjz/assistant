package safety_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
)

// fastForwardRefusal runs a local fast-forward expected to be refused and
// returns the refusal, asserting that no decision comes back with it.
func fastForwardRefusal(t *testing.T, g *safety.Guard, u safety.Update, c safety.Checkout) *safety.Refusal {
	t.Helper()
	decision, err := g.DecideFastForward(context.Background(), u, c)
	if decision.Allowed() {
		t.Fatalf("DecideFastForward allowed %v, want a refusal", decision)
	}
	var refusal *safety.Refusal
	if !errors.Is(err, safety.ErrRefused) || !errors.As(err, &refusal) {
		t.Fatalf("DecideFastForward error = %v, want a *Refusal", err)
	}
	return refusal
}

// A local landing is a fast-forward from the tip the program observed and
// nothing else: the positive control is allowed and keeps its anchor; a
// proposal that would drop the branch's commits, a branch that was absent,
// and a branch that moved since the observation are each refused, the last
// by Decide's own rule; and a clean fast-forward is still refused where
// custody would block it, checked out elsewhere or checked out here over a
// dirty or untracked tree, while a clean checkout here is allowed.
func TestALocalLandingIsOnlyAFastForwardCustodyAllows(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	git := &fakeGit{
		parents: map[string][]string{"c1": nil, "c2": {"c1"}, "c3": {"c2"}, "x3": {"c1"}},
		// The first round is the observation, every later one a decision's
		// fresh read.
		advertised: map[string][][]advert{remote: {{branch(ref, "c2")}}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	ctx := context.Background()

	decision, err := guard.DecideFastForward(ctx, safety.Update{Target: target, Proposed: "c3", Anchor: obs}, safety.Checkout{})
	if err != nil || !decision.Allowed() || decision.Kind() != safety.KindFastForward || decision.Anchor().State().Commit != "c2" {
		t.Fatalf("a fast-forward from the observed tip = %v, %v", decision, err)
	}
	if decision, err := guard.DecideFastForward(ctx, safety.Update{Target: target, Proposed: "c3", Anchor: obs},
		safety.Checkout{Here: true}); err != nil || !decision.Allowed() {
		t.Fatalf("a fast-forward of a clean checked-out branch = %v, %v", decision, err)
	}

	r := fastForwardRefusal(t, guard, safety.Update{Target: target, Proposed: "x3", Anchor: obs}, safety.Checkout{})
	if r.Reason != safety.ReasonNotFastForward || !slices.Equal(r.Discarded, []string{"c2"}) {
		t.Fatalf("a proposal dropping c2 was refused as %s naming %v", r.Reason, r.Discarded)
	}
	for name, c := range map[string]safety.Checkout{
		"checked out elsewhere":   {Elsewhere: []string{"/elsewhere"}},
		"checked out dirty":       {Here: true, Dirty: true},
		"checked out, untracked":  {Here: true, Untracked: true},
		"elsewhere and here, too": {Here: true, Elsewhere: []string{"/elsewhere"}},
	} {
		if r := fastForwardRefusal(t, guard, safety.Update{Target: target, Proposed: "c3", Anchor: obs}, c); r.Reason != safety.ReasonCheckedOut {
			t.Fatalf("%s was refused as %s, want %s", name, r.Reason, safety.ReasonCheckedOut)
		}
	}
	// Dirty and untracked describe the working copy the update is made in,
	// and say nothing when the branch is not checked out there.
	if _, err := guard.DecideFastForward(ctx, safety.Update{Target: target, Proposed: "c3", Anchor: obs},
		safety.Checkout{Dirty: true, Untracked: true}); err != nil {
		t.Fatalf("a dirty tree on another branch refused the fast-forward: %v", err)
	}

	absent := &fakeGit{parents: linear("c1"), advertised: map[string][][]advert{remote: {nil}}}
	ag := safety.New(absent)
	if r := fastForwardRefusal(t, ag, safety.Update{Target: target, Proposed: "c1", Anchor: observe(t, ag)}, safety.Checkout{}); r.Reason != safety.ReasonNotFastForward {
		t.Fatalf("creating the branch was refused as %s", r.Reason)
	}

	moved := &fakeGit{parents: linear("c1", "c2", "c3"), advertised: map[string][][]advert{remote: {{branch(ref, "c1")}, {branch(ref, "c2")}}}}
	mg := safety.New(moved)
	if r := fastForwardRefusal(t, mg, safety.Update{Target: target, Proposed: "c3", Anchor: observe(t, mg)}, safety.Checkout{}); r.Reason != safety.ReasonTargetMoved {
		t.Fatalf("a branch that moved since the observation was refused as %s, want %s", r.Reason, safety.ReasonTargetMoved)
	}
}
