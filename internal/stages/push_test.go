package stages_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
)

// TestThePushStageForwardsTheVerifiedCommitToTheBranch is the stage doing its
// job: the commit the run validated reaches the upstream, on the branch under
// validation.
//
// The branch is what is asserted, not merely that something was pushed. A push
// to the base would put the change on the base branch itself, past the pull
// request stage that is supposed to propose it, and every other assertion here
// would still hold.
func TestThePushStageForwardsTheVerifiedCommitToTheBranch(t *testing.T) {
	run := newPushRun(t)

	report := run.mustPush(t)
	if report.HasHeld() {
		t.Fatalf("a run with a reviewed commit and an anchor held at the push stage: %+v", report)
	}

	if got := run.upstreamCommit(t, "refs/heads/topic"); got != run.head {
		t.Fatalf("the upstream branch stands at %q, want the verified commit %q", got, run.head)
	}
	// The base is where a pull request would merge into, and this stage does
	// not touch it.
	if got := run.upstreamCommit(t, "refs/heads/main"); got != run.baseCommit {
		t.Fatalf("the upstream base moved from %q to %q, and the push stage forwards to the branch",
			run.baseCommit, got)
	}
}

// TestThePushStageRefusesWithoutAnAnchor is the guard PRD principle P6 asks
// for, checked rather than described.
//
// The anchor is the observation an earlier stage took before the run did its
// work. A run without one could only be pushed on a read taken now, which
// always holds and so protects nothing; that is the trap P6 names outright and
// the stage refuses rather than falling into it.
func TestThePushStageRefusesWithoutAnAnchor(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)
	run.anchor = ""

	report := run.mustPush(t)
	if !report.HasHeld() {
		t.Fatalf("a run with no recorded anchor pushed anyway: %+v", report)
	}
	if !hasFinding(report, "push-no-anchor") {
		t.Fatalf("the refusal does not name the missing anchor: %+v", report)
	}
	run.mustNotExistUpstream(t, "refs/heads/topic")
}

// TestThePushStageRefusesAnAnchorForAnotherBranch keeps an anchor from
// protecting a reference the push does not touch.
//
// An observation of one branch and an update to another is the shape that
// satisfies every type in sight and establishes nothing: the lease would be
// checked against a branch this push leaves alone.
func TestThePushStageRefusesAnAnchorForAnotherBranch(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)
	run.anchor = run.anchorFor(t, "refs/heads/somewhere-else", false, "")

	report := run.mustPush(t)
	if !report.HasHeld() {
		t.Fatalf("a run whose anchor names another branch pushed anyway: %+v", report)
	}
	if !hasFinding(report, "push-anchor-names-another-target") {
		t.Fatalf("the refusal does not name the mismatched anchor: %+v", report)
	}
	run.mustNotExistUpstream(t, "refs/heads/topic")
}

// TestThePushStageRefusesWhenTheBranchMovedSinceTheRunObservedIt is the anchor
// doing the work it exists for.
//
// The branch acquires a commit after the run observed it absent, which is
// somebody else's work arriving out of band. internal/safety refuses because
// the target no longer matches the anchor, and nothing is pushed, so the
// commit that arrived is still there afterwards. That is P6's whole point: the
// refusal is an annoyance and the lost commit would not have been repairable.
func TestThePushStageRefusesWhenTheBranchMovedSinceTheRunObservedIt(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)

	// The run observed refs/heads/topic absent. Somebody else pushes to it.
	arrived := run.commitOnUpstreamBranch(t, "topic", "out-of-band.txt", "work from somewhere else\n")

	report := run.mustPush(t)
	if !report.HasHeld() {
		t.Fatalf("a push onto a branch that moved since the observation was allowed: %+v", report)
	}
	if !hasFinding(report, "push-refused") {
		t.Fatalf("the refusal does not come from the safety check: %+v", report)
	}
	if got := run.upstreamCommit(t, "refs/heads/topic"); got != arrived {
		t.Fatalf("the branch stands at %q and the commit that arrived out of band was %q, so the "+
			"refused push overwrote it", got, arrived)
	}
}

// TestThePushStageRefusesACommitNoReviewApproved is the other half of what PRD
// section 5 requires of this stage.
//
// A head that does not descend from the reviewed commit is carrying work no
// completed review looked at. This is the check that was a TODO returning nil
// before it was written, which is worse than absent: it read as a guard and
// permitted everything.
func TestThePushStageRefusesACommitNoReviewApproved(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)
	// A commit that exists and that the head does not descend from: the base,
	// which the branch was cut from and then moved away.
	run.reviewedCommit = run.divergent(t)

	report := run.mustPush(t)
	if !report.HasHeld() {
		t.Fatalf("a commit no review approved was pushed: %+v", report)
	}
	if !hasFinding(report, "push-not-descended-from-review") {
		t.Fatalf("the refusal does not name the ancestry check: %+v", report)
	}
	run.mustNotExistUpstream(t, "refs/heads/topic")
}

// TestThePushStageRefusesAReviewedCommitItCannotRead is P6's other direction:
// a safety fact that cannot be verified is refused rather than assumed.
//
// The reviewed commit here exists only on the upstream, so the run's copy
// cannot resolve it and the ancestry check cannot be made at all. That is a
// refusal and not a failure of the run, and it is distinct from the head
// genuinely not descending.
func TestThePushStageRefusesAReviewedCommitItCannotRead(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)
	run.reviewedCommit = run.commitOnUpstreamBranch(t, "elsewhere", "far.txt", "only on the upstream\n")

	report := run.mustPush(t)
	if !report.HasHeld() {
		t.Fatalf("a reviewed commit the copy cannot read satisfied the ancestry check: %+v", report)
	}
	if !hasFinding(report, "push-reviewed-commit-unreadable") {
		t.Fatalf("the refusal does not name the unreadable commit: %+v", report)
	}
	run.mustNotExistUpstream(t, "refs/heads/topic")
}

// TestThePushStageRefusesAReviewThatCompletedWithoutNamingACommit closes the
// way the ancestry check could pass by comparing against nothing.
func TestThePushStageRefusesAReviewThatCompletedWithoutNamingACommit(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)
	run.reviewedCommit = ""

	report := run.mustPush(t)
	if !report.HasHeld() {
		t.Fatalf("a review naming no commit satisfied the ancestry check: %+v", report)
	}
	if !hasFinding(report, "push-review-named-no-commit") {
		t.Fatalf("the refusal does not name the missing revision: %+v", report)
	}
	run.mustNotExistUpstream(t, "refs/heads/topic")
}

// TestThePushStageRefusesWithoutACompletedReview is the first thing PRD
// section 5 asks of the stage.
func TestThePushStageRefusesWithoutACompletedReview(t *testing.T) {
	run := newPushRun(t)
	run.reviewOutcome = pipeline.OutcomeHeld

	report := run.mustPush(t)
	if !report.HasHeld() {
		t.Fatalf("a run whose review did not complete pushed anyway: %+v", report)
	}
	if !hasFinding(report, "push-no-approval") {
		t.Fatalf("the refusal does not name the missing approval: %+v", report)
	}
	run.mustNotExistUpstream(t, "refs/heads/topic")
}

// TestTheAnchorTheRebaseStageRecordsIsTheOneThePushStageDecidesOn is the seam
// between the two halves of P6's anchor.
//
// One stage observes and another decides, several stages apart and possibly
// across a restart. Nothing in either body can notice that the other wrote
// something it cannot read, or observed a reference it does not push to: both
// would be a run that pushes on an anchor protecting nothing, and both would
// leave every test of either half green.
//
// So the anchor here is not written by this test. It is whatever the rebase
// stage recorded, carried into the push stage exactly as run state carries it.
func TestTheAnchorTheRebaseStageRecordsIsTheOneThePushStageDecidesOn(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)

	run.anchor = run.anchorFromRebaseStage(t)
	if run.anchor == "" {
		t.Fatal("the rebase stage recorded no anchor, so nothing carries P6's observation to the push")
	}

	report := run.mustPush(t)
	if report.HasHeld() {
		t.Fatalf("the push stage refused the anchor the rebase stage recorded, so the two halves "+
			"of the anchor do not agree: %+v", report)
	}
	if got := run.upstreamCommit(t, "refs/heads/topic"); got != run.head {
		t.Fatalf("the branch stands at %q, want the verified commit %q", got, run.head)
	}
}

// anchorFromRebaseStage runs the rebase stage over this run and returns the
// anchor it wrote.
func (r *pushRun) anchorFromRebaseStage(t *testing.T) string {
	t.Helper()
	impl := stages.Rebase(r.deps)
	allowed := make(map[pipeline.Key]bool, len(impl.Reads))
	for _, key := range impl.Reads {
		allowed[key] = true
	}
	out, err := impl.NewBody()(t.Context(), pipeline.Input{
		Stage: pipeline.StageRebase,
		State: declaredReader{allowed: allowed, state: map[pipeline.Key]graph.Value{
			pipeline.KeyRepository: graph.TextValue(r.repositoryID),
			pipeline.KeyRun:        graph.TextValue(r.runID),
			pipeline.KeyBranch:     graph.TextValue("topic"),
			pipeline.KeyBase:       graph.TextValue("main"),
			pipeline.KeySubmitted:  graph.TextValue(r.head),
			pipeline.KeyHead:       graph.TextValue(r.head),
		}},
	})
	if err != nil {
		t.Fatalf("running the rebase stage: %v", err)
	}
	// The rebase stage may move the head, and the push stage forwards what the
	// run stands on afterwards.
	if moved, ok := out.Writes[pipeline.KeyHead]; ok {
		if text, _ := moved.Text(); text != "" {
			r.head = text
		}
	}
	anchor, _ := out.Writes[pipeline.KeyPushAnchor].Text()
	return anchor
}

// pushRun is one run of the push stage against a real upstream.
type pushRun struct {
	home         *home.Home
	deps         stages.StageDeps
	repositoryID string
	runID        string
	// upstream is the bare repository the branch is forwarded to.
	upstream string
	// copy is the run's isolated copy, where the stage works.
	copy string
	// head is the commit the run validated and would forward.
	head string
	// baseCommit is where refs/heads/main stands on the upstream.
	baseCommit string
	// source is the repository the copy is a worktree of, which is where a
	// test makes a commit the copy can resolve.
	source string

	// The facts a test varies to exercise one guard at a time.
	anchor         string
	reviewedCommit string
	reviewOutcome  pipeline.Outcome
}

// newPushRun builds an upstream holding only main, and an isolated copy
// standing on a topic commit that descends from it.
//
// The upstream does not hold the topic branch, so the ordinary run here is the
// first push of a branch: the anchor records it absent and the decision is a
// creation. Every guard is then varied from that one starting point.
func newPushRun(t *testing.T) *pushRun {
	t.Helper()
	run := &pushRun{
		home:           newHome(t),
		repositoryID:   "repository-1",
		runID:          "run-1",
		reviewOutcome:  pipeline.OutcomePassed,
		upstream:       filepath.Join(t.TempDir(), "upstream.git"),
		reviewedCommit: "",
	}
	git(t, filepath.Dir(run.upstream), "init", "--quiet", "--bare", "-b", "main", run.upstream)

	source := t.TempDir()
	run.source = source
	git(t, source, "init", "--quiet", "-b", "main")
	write(t, source, "base.go", "package subject\n")
	git(t, source, "add", ".")
	git(t, source, "commit", "--quiet", "-m", "base")
	git(t, source, "remote", "add", "origin", run.upstream)
	git(t, source, "push", "--quiet", "origin", "main")
	run.baseCommit = git(t, source, "rev-parse", "HEAD")

	// The change under validation, on top of the base.
	write(t, source, "change.go", "package subject\n\n// the change under validation\n")
	git(t, source, "add", ".")
	git(t, source, "commit", "--quiet", "-m", "the change under validation")
	run.head = git(t, source, "rev-parse", "HEAD")

	run.copy = run.home.Worktree(run.repositoryID, run.runID)
	if err := os.MkdirAll(filepath.Dir(run.copy), 0o700); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(run.copy), err)
	}
	// The copy is a worktree of the source, and a worktree shares its
	// repository's configuration, so it already reaches the upstream under the
	// name the stage pushes to. That is the same arrangement production has,
	// where internal/service sets the remote on the gate repository every run
	// of it is cut from.
	git(t, source, "worktree", "add", "--quiet", "--detach", run.copy, run.head)

	run.reviewedCommit = run.head
	run.anchor = run.anchorFor(t, "refs/heads/topic", false, "")
	run.deps = stages.NewStageDeps(agents.StageAgent{}, run.home, config.Defaults(), nil, redact.New())
	return run
}

// anchorFor builds the recorded observation a run carries, in the form the
// rebase stage writes and the push stage reads.
func (r *pushRun) anchorFor(t *testing.T, ref string, exists bool, commit string) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"remote": "origin",
		"ref":    ref,
		"exists": exists,
		"commit": commit,
	})
	if err != nil {
		t.Fatalf("building an anchor: %v", err)
	}
	return string(encoded)
}

// divergent returns a commit the run's head does not descend from, and that
// the run's copy can resolve.
//
// It is made in the source repository, whose object store the copy shares as a
// worktree of it, so the ancestry check has both commits to compare and
// answers no rather than failing to resolve one. Those are different
// conditions with different reports, and this is the one about descent.
func (r *pushRun) divergent(t *testing.T) string {
	t.Helper()
	git(t, r.source, "checkout", "--quiet", "-b", "divergent", r.baseCommit)
	write(t, r.source, "elsewhere.txt", "not in the head's history\n")
	git(t, r.source, "add", ".")
	git(t, r.source, "commit", "--quiet", "-m", "a commit the head does not descend from")
	return git(t, r.source, "rev-parse", "HEAD")
}

// commitOnUpstreamBranch puts a commit on a branch of the upstream, from a
// checkout of its own, and returns it. It is how a test arranges work arriving
// out of band.
func (r *pushRun) commitOnUpstreamBranch(t *testing.T, branch, file, content string) string {
	t.Helper()
	other := t.TempDir()
	git(t, other, "clone", "--quiet", r.upstream, other)
	git(t, other, "checkout", "--quiet", "-b", branch, r.baseCommit)
	write(t, other, file, content)
	git(t, other, "add", ".")
	git(t, other, "commit", "--quiet", "-m", "out of band on "+branch)
	git(t, other, "push", "--quiet", "origin", branch)
	return git(t, other, "rev-parse", "HEAD")
}

// upstreamCommit is what the upstream holds for a reference.
func (r *pushRun) upstreamCommit(t *testing.T, ref string) string {
	t.Helper()
	return git(t, r.upstream, "rev-parse", ref)
}

// mustNotExistUpstream fails when the upstream holds the reference at all,
// which is what a refused first push has to leave behind.
func (r *pushRun) mustNotExistUpstream(t *testing.T, ref string) {
	t.Helper()
	out := git(t, r.upstream, "for-each-ref", "--format=%(refname)", ref)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("%s exists on the upstream after a refused push, standing at %s",
			ref, r.upstreamCommit(t, ref))
	}
}

// mustPush runs the push stage and returns the report the pipeline would
// record. A body that returned an error failed the stage rather than refusing
// it, which is a different thing and never what a guard here should do.
func (r *pushRun) mustPush(t *testing.T) findings.Report {
	t.Helper()
	impl := stages.Push(r.deps)
	allowed := make(map[pipeline.Key]bool, len(impl.Reads))
	for _, key := range impl.Reads {
		allowed[key] = true
	}
	out, err := impl.NewBody()(t.Context(), pipeline.Input{
		Stage: pipeline.StagePush,
		State: declaredReader{allowed: allowed, state: r.state(t)},
	})
	if err != nil {
		t.Fatalf("running the push stage: %v", err)
	}
	report := out.Report.Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the push stage produced a report the pipeline refuses: %v", err)
	}
	return report
}

// state is what an earlier stage of this run would have written by the time
// the push stage reads it.
//
// The review stage's report is encoded the way the stage node encodes it, so a
// body that read it some other way would not decode this.
func (r *pushRun) state(t *testing.T) map[pipeline.Key]graph.Value {
	t.Helper()
	review, err := json.Marshal(findings.Report{
		Summary:  "the review stage had nothing to report",
		Revision: r.reviewedCommit,
	}.Normalize())
	if err != nil {
		t.Fatalf("encoding the review stage's report: %v", err)
	}
	return map[pipeline.Key]graph.Value{
		pipeline.KeyRepository:            graph.TextValue(r.repositoryID),
		pipeline.KeyRun:                   graph.TextValue(r.runID),
		pipeline.KeyBranch:                graph.TextValue("topic"),
		pipeline.KeyBase:                  graph.TextValue("main"),
		pipeline.KeyHead:                  graph.TextValue(r.head),
		pipeline.KeyPushAnchor:            graph.TextValue(r.anchor),
		pipeline.StageReview.ReportKey():  graph.TextValue(string(review)),
		pipeline.StageReview.OutcomeKey(): graph.TextValue(string(r.reviewOutcome)),
	}
}
