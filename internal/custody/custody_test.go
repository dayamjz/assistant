package custody_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/custody"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// A run that has not finished holds the branch: the class is decided from
// the status before anything else is read, and carrying it out is refused,
// leaving the working copy exactly as it was.
func TestARunNotFinishedHoldsTheBranch(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newScenario(t)
	for _, status := range []store.RunStatus{store.RunPending, store.RunRunning, store.RunHeld} {
		s.run.Status = status
		// No position is recorded, and none is needed: a run in flight is
		// waited for, whatever it has done so far.
		c := s.mustClassify(t, custody.Position{})
		if c.Class != custody.ClassPipelineHolds {
			t.Fatalf("a %s run classified %s", status, c.Class)
		}
		before := snapshot(t, s.working, false)
		_, err := s.apply(t, c)
		refusal(t, err, custody.ReasonNotSynchronizable)
		if after := snapshot(t, s.working, false); after != before {
			t.Fatalf("a refused sync changed the working copy:\n%s\n---\n%s", before, after)
		}
	}
	// A status this build does not define is not read as finished, which
	// would open the moves to it.
	s.run.Status = "abandoned"
	_, err := s.classify(t, custody.Position{Recorded: true, Head: s.submitted})
	refusal(t, err, custody.ReasonUnrecorded)
}

// A finished run whose own record shows the work never moved is yours
// already, and nothing is done; one whose record is missing is refused
// rather than read as unmoved, and so is one whose copy holds uncommitted
// work no commit carries.
func TestYoursAlreadyNeedsTheRunsOwnRecord(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newScenario(t)
	s.run.Status = store.RunTerminated
	c := s.mustClassify(t, custody.Position{Recorded: true, Head: s.submitted})
	if c.Class != custody.ClassYoursAlready || c.Work != s.submitted {
		t.Fatalf("an unmoved run classified %+v", c)
	}
	if steps := custody.Steps(c, s.gate, s.upstream); len(steps) != 0 {
		t.Fatalf("yours already plans %v", steps)
	}

	_, err := s.classify(t, custody.Position{})
	refusal(t, err, custody.ReasonUnrecorded)

	write(t, s.copy, "half-done.txt", "a fix nobody committed\n")
	_, err = s.classify(t, custody.Position{Recorded: true, Head: s.submitted})
	refusal(t, err, custody.ReasonCopyUnclean)
}

// Work a finished run moved on and never pushed is recoverable, and
// recovery brings it back into the working copy under the run's recovery
// reference without moving the branch. A copy standing ahead of the
// checkpoint holds newer work than the checkpoint names, and that is what is
// recovered. Recovering again is a no-op, and once the branch holds the work
// it is yours already.
func TestRecoveryBringsTheWorkBackBesideTheBranch(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newScenario(t)
	checkpointed := commit(t, s.copy, "a fix round", map[string]string{"fix.txt": "fixed\n"})
	beyond := commit(t, s.copy, "a fix the checkpoint never saw", map[string]string{"fix.txt": "fixed again\n"})
	c := s.mustClassify(t, custody.Position{Recorded: true, Head: checkpointed})
	if c.Class != custody.ClassRecoverable || c.Work != beyond || c.Pushed {
		t.Fatalf("unpushed work classified %+v, want recoverable on %s", c, beyond)
	}
	result, err := s.apply(t, c)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if got := git(t, s.working, "rev-parse", custody.RecoveredRef(runID)); got != beyond {
		t.Fatalf("the recovery reference names %s, want %s", got, beyond)
	}
	if got := git(t, s.working, "rev-parse", "refs/heads/feature"); got != s.submitted {
		t.Fatalf("recovery moved the branch to %s", got)
	}
	if refs := git(t, s.working, "for-each-ref", "--format=%(refname)", custody.FetchedRef(runID)); refs != "" {
		t.Fatalf("the scratch reference was left behind: %s", refs)
	}
	if kinds := kindsOf(result.Done); !slices.Equal(kinds, []custody.StepKind{custody.StepFetch, custody.StepRecover, custody.StepRelease}) {
		t.Fatalf("recovery did %v", kinds)
	}
	if _, err := s.apply(t, c); err != nil {
		t.Fatalf("recovering again: %v", err)
	}

	git(t, s.working, "merge", "--quiet", "--ff-only", custody.RecoveredRef(runID))
	if c := s.mustClassify(t, custody.Position{Recorded: true, Head: checkpointed}); c.Class != custody.ClassYoursAlready {
		t.Fatalf("a branch holding the recovered work classified %s", c.Class)
	}
}

// A recovery reference somebody else wrote is not replaced.
func TestRecoveryNeverReplacesAReference(t *testing.T) {
	s := newScenario(t)
	work := commit(t, s.copy, "a fix round", map[string]string{"fix.txt": "fixed\n"})
	git(t, s.working, "update-ref", custody.RecoveredRef(runID), s.base)
	c := s.mustClassify(t, custody.Position{Recorded: true, Head: work})
	_, err := s.apply(t, c)
	refusal(t, err, custody.ReasonRecoveryTaken)
	if got := git(t, s.working, "rev-parse", custody.RecoveredRef(runID)); got != s.base {
		t.Fatalf("the existing reference was replaced with %s", got)
	}
}

// A branch still at the submitted commit, which the pushed commit contains,
// is behind: the code host is read again, the pushed commit is fetched from
// the gate, and the checked-out branch fast-forwards to it with its files,
// anchored on the submitted commit. Nothing outside the branch and the
// scratch reference changes: no remote, no remote-tracking reference, no
// configuration. Synchronizing again finds nothing to do.
func TestBehindFastForwardsTheCheckedOutBranch(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newScenario(t)
	pushed := commit(t, s.copy, "a fix round", map[string]string{"fix.txt": "fixed\n"})
	s.push(t, pushed)
	c := s.mustClassify(t, custody.Position{Recorded: true, Head: pushed, Pushed: pushed})
	if c.Class != custody.ClassBehind || c.Work != pushed || c.Submitted != s.submitted {
		t.Fatalf("a pushed fast-forward classified %+v", c)
	}
	upstream, gate := snapshot(t, s.upstream, true), snapshot(t, s.gate, true)
	remotes := git(t, s.working, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes/")
	config := git(t, s.working, "config", "--local", "--list")

	result, err := s.apply(t, c)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := git(t, s.working, "rev-parse", "refs/heads/feature"); got != pushed {
		t.Fatalf("feature is at %s, want %s", got, pushed)
	}
	if status := git(t, s.working, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("the tree does not match the moved branch:\n%s", status)
	}
	move := result.Done[slices.IndexFunc(result.Done, func(st custody.Step) bool { return st.Kind == custody.StepMove })]
	if move.From != s.submitted {
		t.Fatalf("the move was anchored on %s, want the submitted commit %s", move.From, s.submitted)
	}
	if after := snapshot(t, s.upstream, true); after != upstream {
		t.Fatalf("the upstream changed:\n%s\n---\n%s", upstream, after)
	}
	if after := snapshot(t, s.gate, true); after != gate {
		t.Fatalf("the gate changed:\n%s\n---\n%s", gate, after)
	}
	if after := git(t, s.working, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes/"); after != remotes {
		t.Fatalf("a remote-tracking reference changed:\n%s\n---\n%s", remotes, after)
	}
	if after := git(t, s.working, "config", "--local", "--list"); after != config {
		t.Fatalf("the configuration changed:\n%s\n---\n%s", config, after)
	}
	if c := s.mustClassify(t, custody.Position{Recorded: true, Head: pushed, Pushed: pushed}); c.Class != custody.ClassYoursAlready {
		t.Fatalf("after the sync the branch classified %s", c.Class)
	}
}

// The commit the move adopts is freshly verified: a code host whose branch
// no longer names the pushed commit refuses the sync before anything is
// fetched or moved.
func TestAPushedCommitNoLongerOnTheCodeHostIsRefused(t *testing.T) {
	s := newScenario(t)
	pushed := commit(t, s.copy, "a fix round", map[string]string{"fix.txt": "fixed\n"})
	s.push(t, pushed)
	c := s.mustClassify(t, custody.Position{Recorded: true, Head: pushed, Pushed: pushed})
	later := commit(t, s.copy, "somebody else's commit", map[string]string{"other.txt": "other\n"})
	s.push(t, later)
	before := snapshot(t, s.working, false)
	_, err := s.apply(t, c)
	refusal(t, err, custody.ReasonNotCurrent)
	if after := snapshot(t, s.working, false); after != before {
		t.Fatalf("a refused sync changed the working copy:\n%s\n---\n%s", before, after)
	}
}

// P6's anchor is the run's observation, never the tip read a moment before:
// a branch the person moved on after the classification is refused as
// moved, not fast-forwarded from where it now stands, and stays where they
// put it.
func TestTheMoveIsAnchoredOnTheRunsObservation(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newScenario(t)
	pushed := commit(t, s.copy, "a fix round", map[string]string{"fix.txt": "fixed\n"})
	s.push(t, pushed)
	c := s.mustClassify(t, custody.Position{Recorded: true, Head: pushed, Pushed: pushed})
	mine := commit(t, s.working, "the person carries on", map[string]string{"mine.txt": "mine\n"})
	_, err := s.apply(t, c)
	refusal(t, err, custody.ReasonRefused)
	if got := safetyReason(err); got != safety.ReasonWouldDiscard && got != safety.ReasonTargetMoved {
		t.Fatalf("a branch moved since the observation was refused as %q (%v)", got, err)
	}
	if got := git(t, s.working, "rev-parse", "refs/heads/feature"); got != mine {
		t.Fatalf("the refused sync left feature at %s, want %s", got, mine)
	}
	// Classified afresh, a branch that moved off the observation is
	// diverged: nothing anchors moving it.
	if c := s.mustClassify(t, custody.Position{Recorded: true, Head: pushed, Pushed: pushed}); c.Class != custody.ClassDiverged {
		t.Fatalf("a branch moved off the observation classified %s", c.Class)
	}
}

// The never list, for a move section 11 allows: a dirty tree and a branch
// checked out in another worktree are each refused by the custody half of
// internal/safety's decision, nothing moves, and the fetched scratch
// reference is removed.
func TestAMoveIsRefusedOverADirtyTreeOrAnotherCheckout(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newScenario(t)
	pushed := commit(t, s.copy, "a fix round", map[string]string{"fix.txt": "fixed\n"})
	s.push(t, pushed)
	c := s.mustClassify(t, custody.Position{Recorded: true, Head: pushed, Pushed: pushed})

	write(t, s.working, "readme.txt", "edited and not committed\n")
	_, err := s.apply(t, c)
	refusal(t, err, custody.ReasonRefused)
	if got := safetyReason(err); got != safety.ReasonCheckedOut {
		t.Fatalf("a dirty tree was refused as %q", got)
	}
	git(t, s.working, "checkout", "--quiet", "--", "readme.txt")
	write(t, s.working, "stray.txt", "untracked\n")
	if _, err := s.apply(t, c); safetyReason(err) != safety.ReasonCheckedOut {
		t.Fatalf("an untracked file was refused as %v", err)
	}
	git(t, s.working, "clean", "--quiet", "-f")

	git(t, s.working, "switch", "--quiet", "main")
	elsewhere := t.TempDir() + "/elsewhere"
	git(t, s.working, "worktree", "add", "--quiet", elsewhere, "feature")
	if _, err := s.apply(t, c); safetyReason(err) != safety.ReasonCheckedOut {
		t.Fatalf("a branch checked out in another worktree was refused as %v", err)
	}
	if got := git(t, s.working, "rev-parse", "refs/heads/feature"); got != s.submitted {
		t.Fatalf("a refused sync moved feature to %s", got)
	}
	if refs := git(t, s.working, "for-each-ref", "--format=%(refname)", custody.Namespace); refs != "" {
		t.Fatalf("a refused sync left %s", refs)
	}
	// Checked out nowhere, the branch moves as a reference alone.
	git(t, s.working, "worktree", "remove", elsewhere)
	if _, err := s.apply(t, c); err != nil {
		t.Fatalf("a branch checked out nowhere: %v", err)
	}
	if got := git(t, s.working, "rev-parse", "refs/heads/feature"); got != pushed {
		t.Fatalf("feature is at %s, want %s", got, pushed)
	}
	if head := git(t, s.working, "branch", "--show-current"); head != "main" {
		t.Fatalf("the sync switched the working copy to %s", head)
	}
}

// recorder is the working copy with every write it is asked for recorded in
// order. It delegates each to the real repository, so what it records is
// what the mechanism was asked to do and the result is the mechanism's own.
type recorder struct {
	*vcs.Repository
	calls []string
}

func (r *recorder) CreateRef(ctx context.Context, name, commit string) error {
	r.calls = append(r.calls, "create "+name+" "+commit)
	return r.Repository.CreateRef(ctx, name, commit)
}

func (r *recorder) MoveBranch(ctx context.Context, branch, from, to, reason string) error {
	r.calls = append(r.calls, "move "+branch+" "+from+" "+to)
	return r.Repository.MoveBranch(ctx, branch, from, to, reason)
}

// A diverged branch whose contents match what was pushed is adopted, and the
// head it replaces is written under the run's recovery reference before the
// branch moves. A refused decision writes no recovery reference, because
// the reference is for a move that happens.
func TestAnEquivalentHeadIsKeptBeforeTheBranchMoves(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newScenario(t)
	onto := s.upstreamMoves(t)
	pushed := s.rebuild(t, onto, firstEdited)
	s.push(t, pushed)
	c := s.mustClassify(t, custody.Position{Recorded: true, Head: pushed, Pushed: pushed})
	if c.Class != custody.ClassDivergedEquivalent {
		t.Fatalf("a rebased copy of the person's change classified %s (%s)", c.Class, c.Detail)
	}

	write(t, s.working, "readme.txt", "edited and not committed\n")
	w, _ := s.open(t)
	rec := &recorder{Repository: w}
	if _, err := custody.Apply(context.Background(), rec, c, s.gate, s.upstream); safetyReason(err) != safety.ReasonCheckedOut {
		t.Fatalf("an adoption over a dirty tree = %v", err)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("a refused adoption wrote %v", rec.calls)
	}
	git(t, s.working, "checkout", "--quiet", "--", "readme.txt")

	rec = &recorder{Repository: w}
	if _, err := custody.Apply(context.Background(), rec, c, s.gate, s.upstream); err != nil {
		t.Fatalf("adoption: %v", err)
	}
	want := []string{
		"create " + custody.ReplacedRef(runID) + " " + s.submitted,
		"move feature " + s.submitted + " " + pushed,
	}
	if !slices.Equal(rec.calls, want) {
		t.Fatalf("the adoption wrote %v, want %v", rec.calls, want)
	}
	if got := git(t, s.working, "rev-parse", custody.ReplacedRef(runID)); got != s.submitted {
		t.Fatalf("the replaced head is kept as %s, want %s", got, s.submitted)
	}
	if got := git(t, s.working, "rev-parse", "HEAD"); got != pushed {
		t.Fatalf("HEAD is %s, want %s", got, pushed)
	}
}

// The proof is a three-way merge, not patch identity. The positive control
// is the person's edit replayed onto the moved upstream, which the merge
// reproduces. The lookalike is the same edit, context and all, made to the
// other of two identical blocks: patch identity cannot tell the two apart,
// which the test shows with git's own patch-id, and the merge applies the
// person's edit at its own place and produces a tree that is not the pushed
// one. The lookalike is genuinely diverged, nothing is synchronized, and
// internal/safety's adoption decision refuses it as not equivalent.
func TestTheProofIsAMergeAndNotPatchIdentity(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newScenario(t)
	onto := s.upstreamMoves(t)
	lookalike := s.rebuild(t, onto, secondEdited)
	s.push(t, lookalike)

	patchID := func(commit string) string {
		t.Helper()
		out := gitWithInput(t, s.copy, git(t, s.copy, "show", "--format=", commit)+"\n", "patch-id", "--stable")
		id, _, _ := strings.Cut(out, " ")
		return id
	}
	if mine, theirs := patchID(s.submitted), patchID(lookalike); mine == "" || mine != theirs {
		t.Fatalf("the lookalike's patch identity %q is not the person's %q, so it is not a lookalike", theirs, mine)
	}

	c := s.mustClassify(t, custody.Position{Recorded: true, Head: lookalike, Pushed: lookalike})
	if c.Class != custody.ClassDiverged {
		t.Fatalf("the lookalike classified %s (%s), want diverged", c.Class, c.Detail)
	}
	before := snapshot(t, s.working, false)
	_, err := s.apply(t, c)
	refusal(t, err, custody.ReasonNotSynchronizable)
	if after := snapshot(t, s.working, false); after != before {
		t.Fatalf("a diverged branch was changed:\n%s\n---\n%s", before, after)
	}

	// The decision itself, asked directly over the lookalike, refuses: a
	// classification is not what stands between the branch and the move.
	w, _ := s.open(t)
	git(t, s.working, "fetch", "--quiet", s.gate, lookalike+":refs/assistant/custody/test")
	anchor, err := safety.RestoreObservedFromCheckpoint(safety.ObservationRecord{
		Remote: s.working, Ref: "refs/heads/feature", Exists: true, Commit: s.submitted})
	if err != nil {
		t.Fatal(err)
	}
	_, err = safety.NewAdoption(w).Decide(context.Background(),
		safety.Update{Target: custody.TargetOf(s.working, "feature"), Proposed: lookalike, Anchor: anchor}, safety.Checkout{Here: true})
	var r *safety.Refusal
	if !errors.As(err, &r) || r.Reason != safety.ReasonNotEquivalent || !slices.Equal(r.Discarded, []string{s.submitted}) {
		t.Fatalf("adopting the lookalike = %v, want not equivalent naming %s", err, s.submitted)
	}

	// The positive control: the same edit at the same place is equivalent.
	control := s.rebuild(t, onto, firstEdited)
	proof, err := safety.NewAdoption(w).Prove(context.Background(), custody.TargetOf(s.working, "feature"), s.submitted, control)
	if err == nil {
		t.Fatalf("the control was proved in a working copy that does not hold it: %+v", proof)
	}
	_, gate := s.open(t)
	proof, err = safety.NewAdoption(gate).Prove(context.Background(), custody.TargetOf(s.working, "feature"), s.submitted, control)
	if err != nil || !proof.Equivalent() || proof.Base() != s.base {
		t.Fatalf("the control proved %+v (%v), want equivalent over %s", proof, err, s.base)
	}
}

// Two histories the proof cannot anchor on one base escalate rather than
// being decided either way.
func TestAnUndecidableProofEscalates(t *testing.T) {
	s := newScenario(t)
	w, _ := s.open(t)
	// Two histories that each merged the other's original tip have two best
	// common ancestors.
	a := commit(t, s.working, "a", map[string]string{"a.txt": "a\n"})
	git(t, s.working, "switch", "--quiet", "-c", "side", s.submitted)
	b := commit(t, s.working, "b", map[string]string{"b.txt": "b\n"})
	git(t, s.working, "merge", "--quiet", "--no-edit", a)
	x := git(t, s.working, "rev-parse", "HEAD")
	git(t, s.working, "switch", "--quiet", "-c", "crossed", a)
	git(t, s.working, "merge", "--quiet", "--no-edit", b)
	y := git(t, s.working, "rev-parse", "HEAD")
	_, err := safety.NewAdoption(w).Prove(context.Background(), custody.TargetOf(s.working, "feature"), x, y)
	var r *safety.Refusal
	if !errors.As(err, &r) || r.Reason != safety.ReasonUnverifiable || !strings.Contains(r.Detail, "more than one") {
		t.Fatalf("a criss-cross proof = %v, want an unverifiable refusal", err)
	}
}

// A branch whose contents differ from what was pushed is genuinely
// diverged: blocked, with nothing changed, and the reason stated.
func TestGenuinelyDivergedChangesNothing(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newScenario(t)
	onto := s.upstreamMoves(t)
	// The fix round rewrote the person's line, so integrating the person's
	// version would change the pushed one.
	pushed := s.rebuild(t, onto, strings.Replace(firstEdited, "k = 1", "k = 2", 1))
	s.push(t, pushed)
	c := s.mustClassify(t, custody.Position{Recorded: true, Head: pushed, Pushed: pushed})
	if c.Class != custody.ClassDiverged || c.Detail == "" {
		t.Fatalf("a rewritten change classified %+v", c)
	}
	if steps := custody.Steps(c, s.gate, s.upstream); len(steps) != 0 {
		t.Fatalf("a diverged branch plans %v", steps)
	}
	before := snapshot(t, s.working, false)
	_, err := s.apply(t, c)
	refusal(t, err, custody.ReasonNotSynchronizable)
	if after := snapshot(t, s.working, false); after != before {
		t.Fatalf("a diverged branch was changed")
	}
}

// Classifying reads and never writes: across every class, the working copy,
// the gate's references and the upstream are exactly as they were. This is
// what lets assistant status show the classification.
func TestClassifyingChangesNothing(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newScenario(t)
	onto := s.upstreamMoves(t)
	pushed := s.rebuild(t, onto, firstEdited)
	s.push(t, pushed)
	working, gate, upstream := snapshot(t, s.working, false), snapshot(t, s.gate, true), snapshot(t, s.upstream, true)
	positions := []custody.Position{
		{Recorded: true, Head: s.submitted},
		{Recorded: true, Head: pushed},
		{Recorded: true, Head: pushed, Pushed: pushed},
		{},
	}
	seen := map[custody.Class]bool{}
	for _, status := range []store.RunStatus{store.RunHeld, store.RunPassed} {
		s.run.Status = status
		for _, p := range positions {
			if c, err := s.classify(t, p); err == nil {
				seen[c.Class] = true
			}
		}
	}
	if len(seen) < 3 {
		t.Fatalf("the reads covered only %v", seen)
	}
	if after := snapshot(t, s.working, false); after != working {
		t.Fatalf("classifying changed the working copy:\n%s\n---\n%s", working, after)
	}
	if after := snapshot(t, s.gate, true); after != gate {
		t.Fatalf("classifying changed the gate's references")
	}
	if after := snapshot(t, s.upstream, true); after != upstream {
		t.Fatalf("classifying changed the upstream")
	}
}

// The closed sets and the plan agree: every class is listed once, only the
// classes that act plan steps, and every step a plan holds is a kind the
// set lists.
func TestThePlanIsTheClassesSteps(t *testing.T) {
	t.Parallel()
	listed := map[custody.StepKind]bool{}
	for _, k := range custody.StepKinds() {
		listed[k] = true
	}
	for _, class := range custody.Classes() {
		steps := custody.Steps(custody.Classification{Class: class, Run: runID, Branch: "feature", Submitted: "s", Work: "w"}, "gate", "up")
		if (len(steps) > 0) != class.Acts() {
			t.Errorf("%s plans %d steps and Acts reports %t", class, len(steps), class.Acts())
		}
		for _, st := range steps {
			if !listed[st.Kind] {
				t.Errorf("%s plans a %s step, which StepKinds does not list", class, st.Kind)
			}
			if st.Kind == custody.StepMove && (st.From != "s" || st.To != "w") {
				t.Errorf("%s moves from %s to %s, want from the submitted commit to the work", class, st.From, st.To)
			}
		}
	}
	if len(custody.Classes()) != 6 {
		t.Fatalf("PRD section 11 lists six classes and this build has %d", len(custody.Classes()))
	}
}

func kindsOf(steps []custody.Step) []custody.StepKind {
	out := make([]custody.StepKind, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Kind)
	}
	return out
}

// Adoption.Decide licenses a drop by content and not by the run's basis,
// which is the exception internal/safety's Decision.Rewritten documents. The
// branch stands at the person's commit A, and the proposed commit B is a
// sibling of it on the same base with an identical tree, so A is not
// reachable from B and the update drops A. With an empty Update.Basis the
// adoption is allowed as an anchored force naming A as rewritten, because the
// equivalence proof holds: merging A into B over their merge base changes
// nothing.
//
// The control is Guard.Decide over the same update, which reads the basis
// and refuses it as would-discard naming A, so the allowance above is the
// adoption's proof and not a basis rule that let everything through.
func TestAnAdoptionDropsBySiblingContentWithNoBasis(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newScenario(t)
	tree := git(t, s.working, "rev-parse", s.submitted+"^{tree}")
	sibling := git(t, s.working, "commit-tree", tree, "-p", s.base, "-m", "the same change, committed again")
	if git(t, s.working, "rev-parse", sibling+"^{tree}") != tree {
		t.Fatal("the sibling does not carry the person's tree")
	}
	w, _ := s.open(t)
	anchor, err := safety.RestoreObservedFromCheckpoint(safety.ObservationRecord{
		Remote: s.working, Ref: "refs/heads/feature", Exists: true, Commit: s.submitted})
	if err != nil {
		t.Fatal(err)
	}
	u := safety.Update{Target: custody.TargetOf(s.working, "feature"), Proposed: sibling, Anchor: anchor}

	decision, err := safety.NewAdoption(w).Decide(context.Background(), u, safety.Checkout{Here: true})
	if err != nil {
		t.Fatalf("adopting an identical sibling with no basis = %v, want allowed on the proof", err)
	}
	if decision.Kind() != safety.KindAnchoredForce || !slices.Equal(decision.Rewritten(), []string{s.submitted}) {
		t.Fatalf("the adoption is %v dropping %v, want an anchored force dropping %s",
			decision.Kind(), decision.Rewritten(), s.submitted)
	}
	if got := decision.Anchor().State().Commit; got != s.submitted {
		t.Fatalf("the adoption is leased on %q, want the observed %s", got, s.submitted)
	}

	_, err = safety.New(w).Decide(context.Background(), u)
	var r *safety.Refusal
	if !errors.As(err, &r) || r.Reason != safety.ReasonWouldDiscard || !slices.Equal(r.Discarded, []string{s.submitted}) {
		t.Fatalf("Guard.Decide over the same update = %v, want would-discard naming %s", err, s.submitted)
	}
}
