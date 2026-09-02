package safety_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

const (
	remote = "gate"
	ref    = "refs/heads/feature"
)

var target = safety.Target{Remote: remote, Ref: ref}

// observe takes the run's observation and fails the test when it could not be
// taken, because a test about a decision has nothing to say without one.
func observe(t *testing.T, g *safety.Guard) safety.Observation {
	t.Helper()
	obs, err := g.Observe(context.Background(), target)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	return obs
}

// refusalFor runs a decision that is expected to be refused and returns the
// refusal. It also asserts the thing every refusal has to be true of: no
// decision comes back with it.
func refusalFor(t *testing.T, g *safety.Guard, u safety.Update) *safety.Refusal {
	t.Helper()
	decision, err := g.Decide(context.Background(), u)
	if decision.Allowed() {
		t.Fatalf("Decide allowed %v, want a refusal", decision)
	}
	if !errors.Is(err, safety.ErrRefused) {
		t.Fatalf("Decide error = %v, want it to match ErrRefused", err)
	}
	var refusal *safety.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("Decide error = %v, want a *Refusal", err)
	}
	return refusal
}

func TestObserveReportsWhereTheTargetStands(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1", "c2"),
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c2")}}},
	}
	obs := observe(t, safety.New(git))
	if !obs.Observed() {
		t.Fatal("Observed() = false, want true for an observation Observe produced")
	}
	if got, want := obs.State(), (safety.RemoteState{Exists: true, Commit: "c2"}); got != want {
		t.Fatalf("State() = %v, want %v", got, want)
	}
	if obs.Target() != target {
		t.Fatalf("Target() = %v, want %v", obs.Target(), target)
	}
}

func TestObserveOnAnAbsentTargetIsAnObservation(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1"),
		advertised: map[string][][]vcs.Ref{remote: {{branch("refs/heads/other", "c1")}}},
	}
	obs := observe(t, safety.New(git))
	if !obs.Observed() {
		t.Fatal("Observed() = false, want true: an absent target is a state, not a failure")
	}
	if obs.State().Exists {
		t.Fatalf("State() = %v, want absent", obs.State())
	}
}

func TestObserveRefusesWhenTheRemoteCannotBeRead(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:   linear("c1"),
		remoteErr: map[string]error{remote: errBroken},
	}
	obs, err := safety.New(git).Observe(context.Background(), target)
	if obs.Observed() {
		t.Fatalf("Observe returned an observation %v, want none: an unreadable remote is not an absent branch", obs)
	}
	var refusal *safety.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != safety.ReasonUnreadableRemote {
		t.Fatalf("Observe error = %v, want a *Refusal with %s", err, safety.ReasonUnreadableRemote)
	}
	if !errors.Is(err, errBroken) {
		t.Fatalf("Observe error = %v, want it to carry the underlying failure", err)
	}
}

func TestDecideAllowsCreatingAnAbsentTarget(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1", "c2"),
		advertised: map[string][][]vcs.Ref{remote: {nil}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	decision, err := guard.Decide(context.Background(), safety.Update{Target: target, Proposed: "c2", Anchor: obs})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !decision.Allowed() || decision.Kind() != safety.KindCreate {
		t.Fatalf("Decide = %v, want an allowed %s", decision, safety.KindCreate)
	}
	if decision.Anchor().State().Exists {
		t.Fatalf("Anchor() = %v, want the lease to be that the target is still absent", decision.Anchor())
	}
	if len(decision.Rewritten()) != 0 {
		t.Fatalf("Rewritten() = %v, want none for a creation", decision.Rewritten())
	}
}

func TestDecideAllowsAFastForwardAndAnchorsOnTheObservedCommit(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1", "c2", "c3"),
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c2")}}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	decision, err := guard.Decide(context.Background(), safety.Update{Target: target, Proposed: "c3", Anchor: obs})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Kind() != safety.KindFastForward {
		t.Fatalf("Kind() = %v, want %v", decision.Kind(), safety.KindFastForward)
	}
	if got := decision.Anchor().State().Commit; got != "c2" {
		t.Fatalf("Anchor commit = %q, want the observed commit c2", got)
	}
	if got := decision.Proposed(); got != "c3" {
		t.Fatalf("Proposed() = %q, want c3", got)
	}
	if len(decision.Rewritten()) != 0 {
		t.Fatalf("Rewritten() = %v, want none for a fast-forward", decision.Rewritten())
	}
	// Two reads: the run's observation, and the fresh read the decision was
	// taken against. A decision that trusted the anchor instead of reading
	// would have made one.
	if git.reads != 2 {
		t.Fatalf("remote reads = %d, want 2: Decide must take its own fresh read", git.reads)
	}
}

func TestDecideAllowsAnAnchoredForceAndNamesWhatItRewrites(t *testing.T) {
	t.Parallel()
	// c2 and c3 are the run's own submitted commits; r3 is the rebased
	// replacement. The target still stands where the run observed it.
	git := &fakeGit{
		parents: map[string][]string{
			"c1": nil,
			"c2": {"c1"},
			"c3": {"c2"},
			"r3": {"c1"},
		},
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c3")}}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	decision, err := guard.Decide(context.Background(), safety.Update{Target: target, Proposed: "r3", Anchor: obs})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decision.Kind() != safety.KindAnchoredForce {
		t.Fatalf("Kind() = %v, want %v", decision.Kind(), safety.KindAnchoredForce)
	}
	if got := decision.Anchor().State().Commit; got != "c3" {
		t.Fatalf("Anchor commit = %q, want the observed commit c3", got)
	}
	if want := []string{"c3", "c2"}; !slices.Equal(decision.Rewritten(), want) {
		t.Fatalf("Rewritten() = %v, want %v, most recent first", decision.Rewritten(), want)
	}
}

func TestDecideRefusesAMovedTargetAndNamesEverythingItWouldDrop(t *testing.T) {
	t.Parallel()
	// The run observes c2 and rebases onto r3. Between the observation and
	// the decision, someone pushes c3 and c4 onto the target. The refusal
	// names c2 as well as c3 and c4: the anchor no longer describes the
	// target, so the run cannot claim r3 incorporated the commit it observed.
	git := &fakeGit{
		parents: map[string][]string{
			"c1": nil,
			"c2": {"c1"},
			"c3": {"c2"},
			"c4": {"c3"},
			"r3": {"c1"},
		},
		advertised: map[string][][]vcs.Ref{remote: {
			{branch(ref, "c2")},
			{branch(ref, "c4")},
		}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	refusal := refusalFor(t, guard, safety.Update{Target: target, Proposed: "r3", Anchor: obs})
	if refusal.Reason != safety.ReasonWouldDiscard {
		t.Fatalf("Reason = %v, want %v", refusal.Reason, safety.ReasonWouldDiscard)
	}
	if want := []string{"c4", "c3", "c2"}; !slices.Equal(refusal.Discarded, want) {
		t.Fatalf("Discarded = %v, want %v, most recent first", refusal.Discarded, want)
	}
	if got := refusal.Observed; got != (safety.RemoteState{Exists: true, Commit: "c4"}) {
		t.Fatalf("Observed = %v, want the tip the fresh read found", got)
	}
	if got := refusal.Anchor.State().Commit; got != "c2" {
		t.Fatalf("Anchor commit = %q, want the observed commit c2", got)
	}
}

func TestDecideRefusesAMovedTargetEvenWhenNothingWouldBeLost(t *testing.T) {
	t.Parallel()
	// The target advanced to c3, and the proposed commit c4 contains c3, so
	// no commit would be discarded. The anchor still does not describe the
	// target, so proceeding would be a blind force wearing a lease.
	git := &fakeGit{
		parents: linear("c1", "c2", "c3", "c4"),
		advertised: map[string][][]vcs.Ref{remote: {
			{branch(ref, "c2")},
			{branch(ref, "c3")},
		}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	refusal := refusalFor(t, guard, safety.Update{Target: target, Proposed: "c4", Anchor: obs})
	if refusal.Reason != safety.ReasonTargetMoved {
		t.Fatalf("Reason = %v, want %v", refusal.Reason, safety.ReasonTargetMoved)
	}
	if len(refusal.Discarded) != 0 {
		t.Fatalf("Discarded = %v, want none: nothing on the target is missing from c4", refusal.Discarded)
	}
}

func TestDecideRefusesATargetThatVanished(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents: linear("c1", "c2", "c3"),
		advertised: map[string][][]vcs.Ref{remote: {
			{branch(ref, "c2")},
			nil,
		}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	refusal := refusalFor(t, guard, safety.Update{Target: target, Proposed: "c3", Anchor: obs})
	if refusal.Reason != safety.ReasonTargetMoved {
		t.Fatalf("Reason = %v, want %v", refusal.Reason, safety.ReasonTargetMoved)
	}
	if refusal.Observed.Exists {
		t.Fatalf("Observed = %v, want absent", refusal.Observed)
	}
}

func TestDecideRefusesATargetThatAppearedAfterTheObservation(t *testing.T) {
	t.Parallel()
	// The run observed no branch and prepared to create one. Someone else
	// created it first, with a commit the run never saw.
	git := &fakeGit{
		parents: map[string][]string{"c1": nil, "theirs": {"c1"}, "ours": {"c1"}},
		advertised: map[string][][]vcs.Ref{remote: {
			nil,
			{branch(ref, "theirs")},
		}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	refusal := refusalFor(t, guard, safety.Update{Target: target, Proposed: "ours", Anchor: obs})
	if refusal.Reason != safety.ReasonWouldDiscard {
		t.Fatalf("Reason = %v, want %v", refusal.Reason, safety.ReasonWouldDiscard)
	}
	if want := []string{"theirs"}; !slices.Equal(refusal.Discarded, want) {
		t.Fatalf("Discarded = %v, want %v", refusal.Discarded, want)
	}
}

func TestDecideRefusesWhenTheRemoteCannotBeRead(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1", "c2"),
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c1")}}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	git.remoteErr = map[string]error{remote: errBroken}
	refusal := refusalFor(t, guard, safety.Update{Target: target, Proposed: "c2", Anchor: obs})
	if refusal.Reason != safety.ReasonUnreadableRemote {
		t.Fatalf("Reason = %v, want %v", refusal.Reason, safety.ReasonUnreadableRemote)
	}
	if got := refusal.Anchor.State().Commit; got != "c1" {
		t.Fatalf("Anchor commit = %q, want the observed commit c1 to be reported", got)
	}
}

func TestDecideRefusesUnrelatedHistories(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    map[string][]string{"c1": nil, "c2": {"c1"}, "x1": nil},
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c2")}}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	refusal := refusalFor(t, guard, safety.Update{Target: target, Proposed: "x1", Anchor: obs})
	if refusal.Reason != safety.ReasonUnrelatedHistories {
		t.Fatalf("Reason = %v, want %v", refusal.Reason, safety.ReasonUnrelatedHistories)
	}
	if !errors.Is(refusal, vcs.ErrNoMergeBase) {
		t.Fatalf("refusal = %v, want it to carry vcs.ErrNoMergeBase", refusal)
	}
	if got, want := refusal.Observed, (safety.RemoteState{Exists: true, Commit: "c2"}); got != want {
		t.Fatalf("Observed = %v, want %v: the fresh read succeeded, so the refusal must report what it found", got, want)
	}
}

func TestDecideRefusesWhenAComparisonCannotBeAnswered(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		sabotage func(*fakeGit)
	}{
		{"merge base fails", func(g *fakeGit) { g.mergeBaseErr = errBroken }},
		{"reachability fails", func(g *fakeGit) { g.commitsErr = errBroken }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			git := &fakeGit{
				parents:    linear("c1", "c2", "c3"),
				advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c2")}}},
			}
			guard := safety.New(git)
			obs := observe(t, guard)
			tc.sabotage(git)
			refusal := refusalFor(t, guard, safety.Update{Target: target, Proposed: "c3", Anchor: obs})
			if refusal.Reason != safety.ReasonUnverifiable {
				t.Fatalf("Reason = %v, want %v", refusal.Reason, safety.ReasonUnverifiable)
			}
			if len(refusal.Discarded) != 0 {
				t.Fatalf("Discarded = %v, want none: an unanswered comparison must not report that nothing would be lost", refusal.Discarded)
			}
			if got, want := refusal.Observed, (safety.RemoteState{Exists: true, Commit: "c2"}); got != want {
				t.Fatalf("Observed = %v, want %v: the fresh read succeeded, so the refusal must report what it found", got, want)
			}
		})
	}
}

func TestDecideRefusesAProposedCommitThatDoesNotResolve(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1", "c2"),
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c2")}}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	refusal := refusalFor(t, guard, safety.Update{Target: target, Proposed: "nowhere", Anchor: obs})
	if refusal.Reason != safety.ReasonUnverifiable {
		t.Fatalf("Reason = %v, want %v", refusal.Reason, safety.ReasonUnverifiable)
	}
	if !errors.Is(refusal, vcs.ErrRefNotFound) {
		t.Fatalf("refusal = %v, want it to carry vcs.ErrRefNotFound", refusal)
	}
}

func TestObserveRefusesATargetThatPeelsToAnotherObject(t *testing.T) {
	t.Parallel()
	// An annotated tag names a tag object that peels to a commit. The lease
	// this package hands back compares against the object the reference
	// names, while the reachability comparisons read the commit it peels to,
	// so a reference whose two differ is refused rather than decided about.
	git := &fakeGit{
		parents:    linear("c1"),
		advertised: map[string][][]vcs.Ref{remote: {{{Name: ref, Object: "tagobj", Commit: "c1"}}}},
	}
	_, err := safety.New(git).Observe(context.Background(), target)
	var refusal *safety.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != safety.ReasonUnverifiable {
		t.Fatalf("Observe error = %v, want a *Refusal with %s", err, safety.ReasonUnverifiable)
	}
	if refusal.Observed != (safety.RemoteState{}) {
		t.Fatalf("Observed = %v, want the zero state: the reference names a tag object, not a commit", refusal.Observed)
	}
}

func TestDecideDecidesAboutAReferenceThatNamesItsCommitDirectly(t *testing.T) {
	t.Parallel()
	// The gap Target names, pinned so it is a stated behavior rather than an
	// unexamined one. A lightweight tag is advertised as a name and the
	// commit it names, exactly as a branch is, so this package cannot tell
	// the two apart and decides about whichever reference the caller chose.
	tag := safety.Target{Remote: remote, Ref: "refs/tags/v1"}
	git := &fakeGit{
		parents:    linear("c1", "c2"),
		advertised: map[string][][]vcs.Ref{remote: {{branch(tag.Ref, "c1")}}},
	}
	guard := safety.New(git)
	obs, err := guard.Observe(context.Background(), tag)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	decision, err := guard.Decide(context.Background(), safety.Update{Target: tag, Proposed: "c2", Anchor: obs})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !decision.Allowed() || decision.Kind() != safety.KindFastForward {
		t.Fatalf("Decide = %v, want an allowed %s: the reference names its commit directly", decision, safety.KindFastForward)
	}
	if got, want := decision.Anchor().State(), (safety.RemoteState{Exists: true, Commit: "c1"}); got != want {
		t.Fatalf("Anchor().State() = %v, want %v: the lease reads the commit the reference names", got, want)
	}
}

func TestObserveRefusesARemoteAdvertisingTheTargetTwice(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1", "c2"),
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c1"), branch(ref, "c2")}}},
	}
	_, err := safety.New(git).Observe(context.Background(), target)
	var refusal *safety.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != safety.ReasonUnverifiable {
		t.Fatalf("Observe error = %v, want a *Refusal with %s", err, safety.ReasonUnverifiable)
	}
	if refusal.Observed != (safety.RemoteState{}) {
		t.Fatalf("Observed = %v, want the zero state: two advertisements do not reduce to one, and none is invented", refusal.Observed)
	}
}

func TestObserveRefusesATargetAdvertisedWithNoObject(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1"),
		advertised: map[string][][]vcs.Ref{remote: {{{Name: ref}}}},
	}
	_, err := safety.New(git).Observe(context.Background(), target)
	var refusal *safety.Refusal
	if !errors.As(err, &refusal) || refusal.Reason != safety.ReasonUnverifiable {
		t.Fatalf("Observe error = %v, want a *Refusal with %s", err, safety.ReasonUnverifiable)
	}
	if refusal.Observed != (safety.RemoteState{}) {
		t.Fatalf("Observed = %v, want the zero state: an advertisement with no object names no commit", refusal.Observed)
	}
}

func TestObserveReadsOnlyTheExactlyNamedReference(t *testing.T) {
	t.Parallel()
	// ls-remote matches a pattern against the tail of a reference name, so a
	// read for refs/heads/feature also carries back refs/tags/refs/heads/
	// feature. Only the exact name is the branch being decided about.
	decoy := "refs/tags/" + ref
	t.Run("the exact name wins", func(t *testing.T) {
		t.Parallel()
		git := &fakeGit{
			parents:    linear("c1", "c2"),
			advertised: map[string][][]vcs.Ref{remote: {{branch(decoy, "c2"), branch(ref, "c1")}}},
		}
		obs, err := safety.New(git).Observe(context.Background(), target)
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if got, want := obs.State(), (safety.RemoteState{Exists: true, Commit: "c1"}); got != want {
			t.Fatalf("State() = %v, want %v: only %s is the target", got, want, ref)
		}
	})
	t.Run("a tail match alone is still absent", func(t *testing.T) {
		t.Parallel()
		git := &fakeGit{
			parents:    linear("c1", "c2"),
			advertised: map[string][][]vcs.Ref{remote: {{branch(decoy, "c2")}}},
		}
		obs, err := safety.New(git).Observe(context.Background(), target)
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if obs.State().Exists {
			t.Fatalf("State() = %v, want absent: the remote advertises %s and not %s", obs.State(), decoy, ref)
		}
	})
}

func TestRewrittenCannotBeEditedThroughTheDecision(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents: map[string][]string{
			"c1": nil,
			"c2": {"c1"},
			"r2": {"c1"},
		},
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c2")}}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	decision, err := guard.Decide(context.Background(), safety.Update{Target: target, Proposed: "r2", Anchor: obs})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	rewritten := decision.Rewritten()
	if want := []string{"c2"}; !slices.Equal(rewritten, want) {
		t.Fatalf("Rewritten() = %v, want %v", rewritten, want)
	}
	rewritten[0] = "something else"
	if want := []string{"c2"}; !slices.Equal(decision.Rewritten(), want) {
		t.Fatalf("Rewritten() = %v after a caller edited an earlier result, want %v", decision.Rewritten(), want)
	}
}

func TestDecideDecidesOnTheResolvedCommitNotTheSubmittedRevision(t *testing.T) {
	t.Parallel()
	// "head" is a revision, not a commit identifier. What a caller pushes is
	// Decision.Proposed(), so it has to be the commit the revision named when
	// the decision was taken, and the reachability comparison has to have run
	// on that same commit.
	for _, tc := range []struct {
		name          string
		parents       map[string][]string
		wantKind      safety.Kind
		wantRewritten []string
	}{
		{"fast-forward", linear("c1", "c2", "c3"), safety.KindFastForward, nil},
		{"anchored force", map[string][]string{"c1": nil, "c2": {"c1"}, "c3": {"c1"}}, safety.KindAnchoredForce, []string{"c2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			git := &fakeGit{
				parents:    tc.parents,
				revs:       map[string]string{"head": "c3"},
				advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c2")}}},
			}
			guard := safety.New(git)
			obs := observe(t, guard)
			decision, err := guard.Decide(context.Background(), safety.Update{Target: target, Proposed: "head", Anchor: obs})
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if got := decision.Proposed(); got != "c3" {
				t.Fatalf("Proposed() = %q, want the resolved commit c3: a caller pushing the submitted revision would move the target to whatever that revision names later", got)
			}
			if decision.Kind() != tc.wantKind {
				t.Fatalf("Kind() = %v, want %v: the comparison has to run on the resolved commit", decision.Kind(), tc.wantKind)
			}
			if !slices.Equal(decision.Rewritten(), tc.wantRewritten) {
				t.Fatalf("Rewritten() = %v, want %v", decision.Rewritten(), tc.wantRewritten)
			}
		})
	}
}

func TestDecisionStringRendersWhatItsContractStates(t *testing.T) {
	t.Parallel()
	git := &fakeGit{
		parents:    linear("c1", "c2", "c3"),
		advertised: map[string][][]vcs.Ref{remote: {{branch(ref, "c2")}}},
	}
	guard := safety.New(git)
	obs := observe(t, guard)
	decision, err := guard.Decide(context.Background(), safety.Update{Target: target, Proposed: "c3", Anchor: obs})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if want := "fast-forward refs/heads/feature@gate to c3 anchored on c2"; decision.String() != want {
		t.Fatalf("String() = %q, want %q", decision.String(), want)
	}
	if want := "no decision"; (safety.Decision{}).String() != want {
		t.Fatalf("the zero Decision renders as %q, want %q", (safety.Decision{}).String(), want)
	}
}

func TestFakeMergeBaseAnswersWithTheNearestCommonAncestor(t *testing.T) {
	t.Parallel()
	// The fake stands in for Git.MergeBase, which promises the best common
	// ancestor. c1 is a common ancestor of c3 and b3 and sorts first by name;
	// c2 is the one git would report.
	git := &fakeGit{parents: map[string][]string{
		"c1": nil,
		"c2": {"c1"},
		"c3": {"c2"},
		"b3": {"c2"},
	}}
	base, err := git.MergeBase(context.Background(), "c3", "b3")
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	if base != "c2" {
		t.Fatalf("MergeBase(c3, b3) = %q, want c2, the nearest common ancestor and not the first by name", base)
	}
}
