package safety_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// The trap PRD principle P6 names is an anchor that came from the tip read a
// moment before pushing. This package answers it by accepting only an
// Observation as an anchor, so the tests here are about what an Observation
// has to be rather than about what a commit identifier says.

func TestDecideRefusesAnAnchorThatIsNotAnObservation(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1", "c2"),
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c1")}}},
	}
	decision, err := safety.New(git).Decide(context.Background(), safety.Update{
		Target:   target,
		Proposed: "c2",
		// The zero Observation is what a caller ends up with when it never
		// looked at the target at all.
	})
	if decision.Allowed() {
		t.Fatalf("Decide allowed %v on the zero Observation, want a refusal", decision)
	}
	if !errors.Is(err, safety.ErrAnchorNotObserved) {
		t.Fatalf("Decide error = %v, want ErrAnchorNotObserved", err)
	}
}

func TestDecideRefusesAnAnchorObservedOnAnotherTarget(t *testing.T) {
	t.Parallel()
	// Both branches stand at c1, so the anchor's value is exactly right for
	// the target being updated and only its provenance is wrong. A check that
	// compared commits rather than observations would allow this.
	other := safety.Target{Remote: remote, Ref: "refs/heads/other"}
	git := &fakeGit{
		parents: linear("c1", "c2"),
		advertised: map[string][][]vcs.Ref{remote: {
			{branch(ref, "c1"), branch(other.Ref, "c1")},
		}},
	}
	guard := safety.New(git)
	elsewhere, err := guard.Observe(context.Background(), other)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if got := elsewhere.State().Commit; got != "c1" {
		t.Fatalf("observed %q on the other branch, want c1: the test needs both branches at the same commit", got)
	}
	decision, err := guard.Decide(context.Background(), safety.Update{Target: target, Proposed: "c2", Anchor: elsewhere})
	if decision.Allowed() {
		t.Fatalf("Decide allowed %v on an anchor observed elsewhere, want a refusal", decision)
	}
	if !errors.Is(err, safety.ErrAnchorNotObserved) {
		t.Fatalf("Decide error = %v, want ErrAnchorNotObserved", err)
	}
}

func TestAnchorCannotBeBuiltFromACommitIdentifier(t *testing.T) {
	t.Parallel()
	// A caller holding the live tip has no way to turn it into an anchor: the
	// fields of an Observation are unexported, so the only value it can build
	// is the zero one, which Decide refuses. This is the "unrepresentable"
	// half of the rule, and it is asserted here rather than left to a reading
	// of the source.
	forged := safety.Observation{}
	if forged.Observed() {
		t.Fatal("the zero Observation reports Observed() = true, so an anchor could be assembled without a read")
	}
	if forged.State() != (safety.RemoteState{}) {
		t.Fatalf("the zero Observation carries state %v, want none", forged.State())
	}
}

func TestDecideRefusesAnUnusableTargetOrUpdate(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1"),
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c1")}}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	for _, tc := range []struct {
		name   string
		update safety.Update
		want   error
	}{
		{"no remote", safety.Update{Target: safety.Target{Ref: ref}, Proposed: "c1", Anchor: obs}, safety.ErrInvalidTarget},
		{"short reference", safety.Update{Target: safety.Target{Remote: remote, Ref: "feature"}, Proposed: "c1", Anchor: obs}, safety.ErrInvalidTarget},
		{"glob in reference", safety.Update{Target: safety.Target{Remote: remote, Ref: "refs/heads/*"}, Proposed: "c1", Anchor: obs}, safety.ErrInvalidTarget},
		{"no proposed commit", safety.Update{Target: target, Anchor: obs}, safety.ErrInvalidUpdate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			decision, err := guard.Decide(context.Background(), tc.update)
			if decision.Allowed() {
				t.Fatalf("Decide allowed %v, want a refusal", decision)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Decide error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestObserveRefusesAnUnusableTarget(t *testing.T) {
	t.Parallel()
	git := &fakeGit{parents: linear("c1")}
	_, err := safety.New(git).Observe(context.Background(), safety.Target{Remote: remote, Ref: "feature"})
	if !errors.Is(err, safety.ErrInvalidTarget) {
		t.Fatalf("Observe error = %v, want ErrInvalidTarget", err)
	}
	if git.reads != 0 {
		t.Fatalf("remote reads = %d, want 0: an unusable target is refused before git is invoked", git.reads)
	}
}

func TestNewRequiresAMechanism(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("New(nil) returned, want a panic: a guard that cannot read the remote can only ever refuse")
		}
	}()
	safety.New(nil)
}
