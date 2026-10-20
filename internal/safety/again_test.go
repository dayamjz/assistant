package safety_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
)

// A second update of a branch the run already pushed is anchored only to an
// observation of the branch standing where the run's own push left it. The
// remote is read through the fake's wire lines, so the observation is one
// Observe produces from what the remote advertised rather than one the test
// stated.
//
// The run pushed c2 on c1, and rebased its change to r2 on a base that moved.
// With the branch still at c2 the observation is accepted, and the decision
// over it is an anchored force dropping c2 and nothing else, which is the
// run's own commit. With somebody else's x3 pushed on top since, the same
// rule refuses: an anchored force over that observation would drop x3 as
// though the run had seen it before its work, and an absent branch is refused
// on the same terms.
func TestASecondUpdateIsAnchoredOnlyWhereTheRunsOwnUpdateLeftTheBranch(t *testing.T) {
	principles.Cite(t, principles.P6)
	t.Parallel()
	parents := map[string][]string{
		"c1": nil,
		"c2": {"c1"},
		"x3": {"c2"},
		"b2": {"c1"},
		"r2": {"b2"},
	}
	for _, tc := range []struct {
		name    string
		advert  []advert
		refused bool
	}{
		{"control: the branch stands where the run pushed it", []advert{branch(ref, "c2")}, false},
		{"somebody pushed on top of the run's commit", []advert{branch(ref, "x3")}, true},
		{"the branch was deleted", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			git := &fakeGit{parents: parents, advertised: map[string][][]advert{remote: {tc.advert}}}
			guard := safety.New(git)
			obs := observe(t, guard)

			err := safety.RequireObservedAt(obs, "c2")
			if !tc.refused {
				if err != nil {
					t.Fatalf("RequireObservedAt refused the branch standing at the run's own commit: %v", err)
				}
				// The basis is the run's first push, c2, which is what an
				// anchored force over this observation may drop.
				decision, err := guard.Decide(context.Background(),
					safety.Update{Target: target, Proposed: "r2", Anchor: obs, Basis: []string{"c2"}})
				if err != nil {
					t.Fatalf("Decide: %v", err)
				}
				if decision.Kind() != safety.KindAnchoredForce || !slices.Equal(decision.Rewritten(), []string{"c2"}) {
					t.Fatalf("the second update is %v dropping %v, want an anchored force dropping only c2",
						decision.Kind(), decision.Rewritten())
				}
				return
			}
			var refusal *safety.Refusal
			if !errors.As(err, &refusal) || !errors.Is(err, safety.ErrRefused) {
				t.Fatalf("RequireObservedAt = %v, want a *Refusal matching ErrRefused", err)
			}
			if refusal.Reason != safety.ReasonTargetMoved || refusal.Target != target {
				t.Fatalf("the refusal is %s on %s, want %s on %s",
					refusal.Reason, refusal.Target, safety.ReasonTargetMoved, target)
			}
		})
	}
}

// The zero Observation and an empty commit are a caller's mistakes, refused
// with the sentinels Decide uses for the same two mistakes rather than with a
// refusal a person would be asked about.
func TestRequireObservedAtRefusesACallersMistakeAsOne(t *testing.T) {
	t.Parallel()
	if err := safety.RequireObservedAt(safety.Observation{}, "c2"); !errors.Is(err, safety.ErrAnchorNotObserved) {
		t.Fatalf("the zero Observation: %v, want ErrAnchorNotObserved", err)
	}
	git := &fakeGit{parents: linear("c1", "c2"), advertised: map[string][][]advert{remote: {{branch(ref, "c2")}}}}
	if err := safety.RequireObservedAt(observe(t, safety.New(git)), ""); !errors.Is(err, safety.ErrInvalidUpdate) {
		t.Fatalf("an empty commit: %v, want ErrInvalidUpdate", err)
	}
}
