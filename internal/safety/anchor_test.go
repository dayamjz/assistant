package safety_test

import (
	"context"
	"errors"
	"slices"
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

func TestTheZeroObservationIsNotAnAnchor(t *testing.T) {
	t.Parallel()
	// An Observation's fields are unexported, so the only one a caller can
	// build by hand is the zero value, and that one is not an anchor. Producing
	// a usable anchor takes Observe or RestoreObservedFromCheckpoint. The
	// second of those makes the wrong anchor representable, which is why the
	// provenance guarantee is stated to rest on the checkpoint rather than on
	// this type; what stays true here is that nothing is an anchor by default.
	forged := safety.Observation{}
	if forged.Observed() {
		t.Fatal("the zero Observation reports Observed() = true, so an anchor could be assembled without a read")
	}
	if forged.State() != (safety.RemoteState{}) {
		t.Fatalf("the zero Observation carries state %v, want none", forged.State())
	}
	if _, err := safety.RestoreObservedFromCheckpoint(forged.Record()); err == nil {
		t.Fatal("restoring the zero Observation's record succeeded, want a refusal: it names no target")
	}
}

func TestRestoredAnchorCarriesTheRecordedObservation(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1", "c2"),
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c2")}}},
	}
	obs := observe(t, safety.New(git))
	restored, err := safety.RestoreObservedFromCheckpoint(obs.Record())
	if err != nil {
		t.Fatalf("RestoreObservedFromCheckpoint: %v", err)
	}
	if !restored.Observed() {
		t.Fatal("Observed() = false on a restored anchor, want true: a run has to carry its anchor across a restart")
	}
	if restored.Target() != obs.Target() || restored.State() != obs.State() {
		t.Fatalf("restored %v, want %v", restored, obs)
	}
}

func TestRestoredAnchorIsDecidedLikeAnObservedOne(t *testing.T) {
	t.Parallel()
	// The run observed c2 and rebased onto r3, then restarted and restored its
	// anchor from the checkpoint. Someone pushed c3 and c4 meanwhile. Restoring
	// must not buy the update anything an observed anchor would not have.
	git := &fakeGit{
		parents: map[string][]string{
			"c1": nil,
			"c2": {"c1"},
			"c3": {"c2"},
			"c4": {"c3"},
			"r3": {"c1"},
		},
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c4")}}},
	}
	restored, err := safety.RestoreObservedFromCheckpoint(safety.ObservationRecord{
		Remote: remote, Ref: ref, Exists: true, Commit: "c2",
	})
	if err != nil {
		t.Fatalf("RestoreObservedFromCheckpoint: %v", err)
	}
	guard := safety.New(git)
	decision, err := guard.Decide(context.Background(), safety.Update{Target: target, Proposed: "r3", Anchor: restored})
	if decision.Allowed() {
		t.Fatalf("Decide allowed %v on a restored anchor the target has moved off, want a refusal", decision)
	}
	var refusal *safety.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != safety.ReasonWouldDiscard {
		t.Fatalf("Decide error = %v, want a *Refusal with %s", err, safety.ReasonWouldDiscard)
	}
	if want := []string{"c4", "c3", "c2"}; !slices.Equal(refusal.Discarded, want) {
		t.Fatalf("Discarded = %v, want %v, the same list an observed anchor would have produced", refusal.Discarded, want)
	}
}

func TestDecideRefusesARestoredAnchorFromAnotherTarget(t *testing.T) {
	t.Parallel()
	// Both branches stand at c1, so only the target the record names is wrong.
	// The provenance check has to apply to a restored anchor exactly as it does
	// to an observed one.
	git := &fakeGit{
		parents: linear("c1", "c2"),
		advertised: map[string][][]vcs.Ref{remote: {
			{branch(ref, "c1"), branch("refs/heads/other", "c1")},
		}},
	}
	elsewhere, err := safety.RestoreObservedFromCheckpoint(safety.ObservationRecord{
		Remote: remote, Ref: "refs/heads/other", Exists: true, Commit: "c1",
	})
	if err != nil {
		t.Fatalf("RestoreObservedFromCheckpoint: %v", err)
	}
	decision, err := safety.New(git).Decide(context.Background(), safety.Update{Target: target, Proposed: "c2", Anchor: elsewhere})
	if decision.Allowed() {
		t.Fatalf("Decide allowed %v on an anchor restored for another target, want a refusal", decision)
	}
	if !errors.Is(err, safety.ErrAnchorNotObserved) {
		t.Fatalf("Decide error = %v, want ErrAnchorNotObserved", err)
	}
}

func TestRestoreRefusesARecordNoReadCouldHaveProduced(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		record safety.ObservationRecord
		want   error
	}{
		{"absent but naming a commit", safety.ObservationRecord{Remote: remote, Ref: ref, Commit: "c1"}, safety.ErrInvalidObservationRecord},
		{"present but naming none", safety.ObservationRecord{Remote: remote, Ref: ref, Exists: true}, safety.ErrInvalidObservationRecord},
		{"no remote", safety.ObservationRecord{Ref: ref, Exists: true, Commit: "c1"}, safety.ErrInvalidTarget},
		{"short reference", safety.ObservationRecord{Remote: remote, Ref: "feature", Exists: true, Commit: "c1"}, safety.ErrInvalidTarget},
		{"reference that is only a prefix", safety.ObservationRecord{Remote: remote, Ref: "refs/heads/", Exists: true, Commit: "c1"}, safety.ErrInvalidTarget},
		{"remote that reads as an option", safety.ObservationRecord{Remote: "--upload-pack=x", Ref: ref, Exists: true, Commit: "c1"}, safety.ErrInvalidTarget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			restored, err := safety.RestoreObservedFromCheckpoint(tc.record)
			if restored.Observed() {
				t.Fatalf("RestoreObservedFromCheckpoint returned the anchor %v, want none", restored)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("RestoreObservedFromCheckpoint error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRestoredAbsenceIsAnAnchorForACreation(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1"),
		advertised: map[string][][]vcs.Ref{remote: {nil}},
	}
	restored, err := safety.RestoreObservedFromCheckpoint(safety.ObservationRecord{Remote: remote, Ref: ref})
	if err != nil {
		t.Fatalf("RestoreObservedFromCheckpoint: %v", err)
	}
	decision, err := safety.New(git).Decide(context.Background(), safety.Update{Target: target, Proposed: "c1", Anchor: restored})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Kind() != safety.KindCreate {
		t.Fatalf("Kind() = %v, want %v: an absent target is a state a record has to be able to carry", decision.Kind(), safety.KindCreate)
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
		{"reference that is only a prefix", safety.Update{Target: safety.Target{Remote: remote, Ref: "refs/heads/"}, Proposed: "c1", Anchor: obs}, safety.ErrInvalidTarget},
		{"remote that reads as an option", safety.Update{Target: safety.Target{Remote: "--upload-pack=x", Ref: ref}, Proposed: "c1", Anchor: obs}, safety.ErrInvalidTarget},
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

func TestATargetNameThatAddressesNothingNeverBecomesACreation(t *testing.T) {
	t.Parallel()
	// refs/heads/ is a prefix, not a reference. Allowed to stand, a remote
	// that advertises nothing under it reads as an absent target, and the
	// creation of a reference no name can ever be would be permitted.
	prefix := safety.Target{Remote: remote, Ref: "refs/heads/"}
	git := &fakeGit{
		parents:    linear("c1"),
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c1")}}},
	}
	guard := safety.New(git)

	obs, err := guard.Observe(context.Background(), prefix)
	if obs.Observed() || !errors.Is(err, safety.ErrInvalidTarget) {
		t.Fatalf("Observe = (%v, %v), want no observation and ErrInvalidTarget", obs, err)
	}
	restored, err := safety.RestoreObservedFromCheckpoint(safety.ObservationRecord{Remote: remote, Ref: prefix.Ref})
	if restored.Observed() || !errors.Is(err, safety.ErrInvalidTarget) {
		t.Fatalf("RestoreObservedFromCheckpoint = (%v, %v), want no anchor and ErrInvalidTarget", restored, err)
	}
	decision, err := guard.Decide(context.Background(), safety.Update{Target: prefix, Proposed: "c1", Anchor: restored})
	if decision.Allowed() {
		t.Fatalf("Decide allowed %v, want a refusal: %s addresses no reference", decision, prefix.Ref)
	}
	if !errors.Is(err, safety.ErrInvalidTarget) {
		t.Fatalf("Decide error = %v, want ErrInvalidTarget", err)
	}
	if git.reads != 0 {
		t.Fatalf("remote reads = %d, want 0: a target that addresses nothing is refused before git is invoked", git.reads)
	}
}

func TestARemoteThatReadsAsAnOptionIsRefusedBeforeGit(t *testing.T) {
	t.Parallel()
	// A remote beginning with a dash is an option to the command the
	// mechanism builds, not a remote. internal/vcs refuses one too; this
	// package refuses it before the name reaches git at all.
	optionish := safety.Target{Remote: "--upload-pack=x", Ref: ref}
	git := &fakeGit{
		parents:    linear("c1"),
		advertised: map[string][][]vcs.Ref{optionish.Remote: {{branch(ref, "c1")}}},
	}
	obs, err := safety.New(git).Observe(context.Background(), optionish)
	if obs.Observed() || !errors.Is(err, safety.ErrInvalidTarget) {
		t.Fatalf("Observe = (%v, %v), want no observation and ErrInvalidTarget", obs, err)
	}
	if git.reads != 0 {
		t.Fatalf("remote reads = %d, want 0: an option is not a remote to read", git.reads)
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
