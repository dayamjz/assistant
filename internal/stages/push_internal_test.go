package stages

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/vcs"
)

// These tests are in the package rather than beside it because the anchor a
// run records travels in this package's own notation, and building one the way
// the rebase stage will has to go through encodeObservation. Writing that JSON
// out by hand instead would state a shape the encoder need not produce, and
// would leave the two halves of the pair free to drift apart.

// pushWorld is a subject a push stage body can be run against: a bare
// repository standing in for the code host, a branch published on it, and the
// run's isolated copy where the home puts one.
//
// It is built with git directly rather than through internal/vcs, on the same
// terms internal/vcs's, internal/gate's and internal/fixture's own tests use: a
// subject assembled with the code under test cannot show that code wrong.
type pushWorld struct {
	t         *testing.T
	home      *home.Home
	gitConfig string
	upstream  string
	copyPath  string
	repo      *vcs.Repository
	branch    string
	repoID    string
	runID     string
	published string
}

// newPushWorld builds the subject: a default branch and a feature branch, both
// published, and a detached copy of the feature branch for the run to work in.
func newPushWorld(t *testing.T) *pushWorld {
	t.Helper()
	gitConfig := pushGitEnvironment(t)
	root := t.TempDir()
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("opening a home under %s: %v", root, err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home at %s: %v", h.Root(), err)
	}
	w := &pushWorld{t: t, home: h, gitConfig: gitConfig, branch: "feature", repoID: "repository-1", runID: "run-1"}
	w.upstream = filepath.Join(root, "upstream.git")
	pushGit(t, root, "init", "--quiet", "--bare", w.upstream)

	author := filepath.Join(root, "author")
	pushGit(t, root, "init", "--quiet", author)
	pushWrite(t, author, "base.txt", "base\n")
	pushGit(t, author, "add", "-A")
	pushGit(t, author, "commit", "--quiet", "-m", "base")
	pushGit(t, author, "push", "--quiet", w.upstream, "HEAD:refs/heads/main")
	pushGit(t, author, "checkout", "--quiet", "-b", w.branch)
	pushWrite(t, author, "change.txt", "the change\n")
	pushGit(t, author, "add", "-A")
	pushGit(t, author, "commit", "--quiet", "-m", "the change")
	pushGit(t, author, "push", "--quiet", w.upstream, "HEAD:refs/heads/"+w.branch)
	w.published = pushGit(t, author, "rev-parse", "HEAD")

	w.copyPath = h.Worktree(w.repoID, w.runID)
	if err := os.MkdirAll(filepath.Dir(w.copyPath), 0o700); err != nil {
		t.Fatalf("making the directory above %s: %v", w.copyPath, err)
	}
	pushGit(t, root, "clone", "--quiet", w.upstream, w.copyPath)
	pushGit(t, w.copyPath, "checkout", "--quiet", "--detach", "origin/"+w.branch)

	if w.repo, err = vcs.OpenWorktree(context.Background(), w.copyPath); err != nil {
		t.Fatalf("opening the run's copy at %s: %v", w.copyPath, err)
	}
	return w
}

// target is the reference the run's branch lives at on the remote, addressed
// by the name the run's copy has for that remote.
func (w *pushWorld) target() safety.Target {
	return safety.Target{Remote: "origin", Ref: "refs/heads/" + w.branch}
}

// observe takes the run's observation of its branch target, the way the rebase
// stage will before the run does its work.
func (w *pushWorld) observe() safety.Observation {
	w.t.Helper()
	observed, err := safety.New(w.repo).Observe(context.Background(), w.target())
	if err != nil {
		w.t.Fatalf("observing %s: %v", w.target(), err)
	}
	return observed
}

// recorded renders an observation as the run's state carries it.
func (w *pushWorld) recorded(observed safety.Observation) string {
	w.t.Helper()
	text, err := encodeObservation(observed)
	if err != nil {
		w.t.Fatalf("recording the observation of %s: %v", observed.Target(), err)
	}
	return text
}

// commitInCopy adds a commit to the run's copy and returns it. This is what a
// rebase or a fix round leaves behind: the copy's HEAD moves during a run.
func (w *pushWorld) commitInCopy(content, message string) string {
	w.t.Helper()
	pushWrite(w.t, w.copyPath, "change.txt", content)
	pushGit(w.t, w.copyPath, "add", "-A")
	pushGit(w.t, w.copyPath, "commit", "--quiet", "-m", message)
	return pushGit(w.t, w.copyPath, "rev-parse", "HEAD")
}

// advanceOutOfBand lands a commit on the branch from a second clone, which is
// somebody else pushing while the run was working. It returns that commit.
func (w *pushWorld) advanceOutOfBand(message string) string {
	w.t.Helper()
	colleague := filepath.Join(w.t.TempDir(), "colleague")
	pushGit(w.t, filepath.Dir(colleague), "clone", "--quiet", w.upstream, colleague)
	pushGit(w.t, colleague, "checkout", "--quiet", w.branch)
	pushWrite(w.t, colleague, "theirs.txt", "somebody else's work\n")
	pushGit(w.t, colleague, "add", "-A")
	pushGit(w.t, colleague, "commit", "--quiet", "-m", message)
	pushGit(w.t, colleague, "push", "--quiet", "origin", "HEAD:refs/heads/"+w.branch)
	return pushGit(w.t, colleague, "rev-parse", "HEAD")
}

// remoteTip reads what the branch names on the remote, read from the bare
// repository directly so that a test asserting nothing was pushed is not
// asking the code that would have pushed it.
func (w *pushWorld) remoteTip() string {
	w.t.Helper()
	return pushGit(w.t, w.upstream, "rev-parse", "refs/heads/"+w.branch)
}

// runPush runs the push stage body over a run state, and returns what it
// reported. The state names only the keys a caller wants set; every other
// declared key reads as the empty text, which is what a run that never wrote
// one carries.
func (w *pushWorld) runPush(state map[pipeline.Key]graph.Value) (pipeline.Output, error) {
	w.t.Helper()
	base := map[pipeline.Key]graph.Value{
		pipeline.KeyRepository: graph.TextValue(w.repoID),
		pipeline.KeyRun:        graph.TextValue(w.runID),
		pipeline.KeyBranch:     graph.TextValue(w.branch),
	}
	for key, value := range state {
		base[key] = value
	}
	// The push stage starts no agent, so the zero StageAgent is what it is
	// given: a value it never calls rather than one standing in for a call.
	deps := NewStageDeps(agents.StageAgent{}, w.home, config.Config{}, nil)
	implementation := Push(deps)
	allowed := make(map[pipeline.Key]bool, len(implementation.Reads))
	for _, key := range implementation.Reads {
		allowed[key] = true
	}
	return implementation.NewBody()(context.Background(), pipeline.Input{
		Stage: pipeline.StagePush,
		State: pushReader{allowed: allowed, state: base},
	})
}

// pushReader is the pipeline.Reader a stage body is given: it answers the keys
// the implementation declared and refuses the rest, so a test cannot read a
// key the real stage node would not have handed over.
//
// It has no way to fail a declared read, and that is deliberate, on the same
// grounds intent_test.go's reader states: the real reader records every
// refusal and the node adapter takes the recorded error whatever the body
// returned, so a fake that failed a read without that consequence would let a
// body look like it had recovered from something no body can.
//
// It is a second such fake in this package because the first is in the
// external test package and these tests are in this one. Every key this stage
// reads is text, so the zero it answers an unwritten key with is the empty
// text, which is what the graph gives a text key nothing has written.
type pushReader struct {
	allowed map[pipeline.Key]bool
	state   map[pipeline.Key]graph.Value
}

// Get implements pipeline.Reader.
func (r pushReader) Get(key pipeline.Key) (graph.Value, error) {
	if !r.allowed[key] {
		return graph.Value{}, errors.New("the push stage did not declare a read of " + string(key))
	}
	if value, ok := r.state[key]; ok {
		return value, nil
	}
	return graph.TextValue(""), nil
}

// refusalProblem says why a report is not this stage refusing, and returns the
// empty string when it is one: one finding, an ask that holds the run for a
// person under P3, carrying the identifier the caller expects, over a report
// the pipeline would accept, and asking for no state write.
//
// It is a predicate rather than a set of assertions so that it can be held to
// a report that does not refuse.
// TestTheRefusalPredicateRejectsAReportThatDoesNotHold does that, which is
// what keeps every refusal below from passing on any report at all.
func refusalProblem(out pipeline.Output, err error, id string) string {
	if err != nil {
		return "the push stage failed the step instead of refusing: " + err.Error()
	}
	report := out.Report.Normalize()
	if validateErr := report.Validate(); validateErr != nil {
		return "the push stage produced a report the pipeline refuses: " + validateErr.Error()
	}
	if len(out.Writes) != 0 {
		return fmt.Sprintf("a refused push asked for state writes %v, so a later stage would read it as done", out.Writes)
	}
	if len(report.Findings) != 1 {
		return fmt.Sprintf("a refused push reported %d findings, want exactly one: %+v", len(report.Findings), report)
	}
	found := report.Findings[0]
	if !found.Holds() || found.Action != findings.ActionAsk {
		return fmt.Sprintf("a refused push reported a %q finding, which does not hold the run for a person: %+v",
			found.Action, found)
	}
	if found.ID != id {
		return fmt.Sprintf("a refused push reported %q, want %q: %s", found.ID, id, found.Description)
	}
	if !strings.Contains(report.Summary, "refused") {
		return fmt.Sprintf("the summary of a refused push does not say it was refused: %q", report.Summary)
	}
	return ""
}

// assertRefused fails the test unless the report is this stage refusing, and
// returns the finding's description so a caller can check what it says.
func assertRefused(t *testing.T, out pipeline.Output, err error, id string) string {
	t.Helper()
	if problem := refusalProblem(out, err, id); problem != "" {
		t.Fatal(problem)
	}
	return out.Report.Normalize().Findings[0].Description
}

// assertNamesAll fails unless every wanted substring is in the text.
func assertNamesAll(t *testing.T, what, text string, want ...string) {
	t.Helper()
	for _, s := range want {
		if !strings.Contains(text, s) {
			t.Fatalf("%s does not contain %q:\n%s", what, s, text)
		}
	}
}

// The stage forwards the verified commit and records what it forwarded. It
// also names the anchor it decided on, which is what PRD section 13 asks a
// test of P6 to assert: an update leased on the tip read a moment before
// pushing behaves identically to one leased on an old observation whenever the
// target has not moved, so the anchor value is the only thing that separates
// them there.
func TestThePushStageForwardsTheVerifiedCommitAndNamesTheAnchorItDecidedOn(t *testing.T) {
	principles.Cite(t, principles.P6)
	w := newPushWorld(t)
	observed := w.observe()
	head := w.commitInCopy("the change, fixed\n", "fix the change")

	out, err := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
	})
	if err != nil {
		t.Fatalf("the push stage failed: %v", err)
	}
	report := out.Report.Normalize()
	if err := report.Validate(); err != nil {
		t.Fatalf("the push stage produced a report the pipeline refuses: %v", err)
	}
	if report.HasHeld() {
		t.Fatalf("a push that succeeded held the run for a person: %+v", report)
	}
	if got := w.remoteTip(); got != head {
		t.Fatalf("the branch on the remote is %s, want the forwarded commit %s", got, head)
	}
	pushed, ok := out.Writes[pipeline.KeyPushed]
	if !ok {
		t.Fatalf("the push stage wrote no %s, so nothing records what it forwarded: %v",
			pipeline.KeyPushed, out.Writes)
	}
	if text, _ := pushed.Text(); text != head {
		t.Fatalf("the push stage recorded %q as pushed, want %s", text, head)
	}
	assertNamesAll(t, "the report of a completed push", report.Summary,
		head, "refs/heads/"+w.branch, "anchored on "+w.published)
}

// A remote that advanced out of band with a commit the run did not incorporate
// is refused, and the refusal names that commit and an action that resolves
// it. This is PRD principle P6's own sentence and the condition
// internal/fixture plants for it.
//
// It also holds the fetch this body performs before deciding. internal/safety
// answers reachability from the local repository, so without that fetch the
// commit somebody else landed is one the run's copy has never seen, the
// comparison cannot be answered, and this refusal arrives as unverifiable with
// nothing named. Asserting the identifier rather than only the refusal is what
// makes that difference visible here.
func TestARemoteAdvancedOutOfBandIsRefusedNamingWhatWouldBeDiscarded(t *testing.T) {
	principles.Cite(t, principles.P6, principles.P3)
	w := newPushWorld(t)
	observed := w.observe()
	landed := w.advanceOutOfBand("somebody else's commit")
	head := w.commitInCopy("the change, fixed\n", "fix the change")

	out, err := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
	})
	description := assertRefused(t, out, err, "push-refused-would-discard")
	assertNamesAll(t, "the refusal", description,
		landed,
		"safety: refused to update refs/heads/"+w.branch,
		"(would-discard)",
		"holds commits that",
		"does not contain",
		"would discard ",
		"rebase "+head+" onto refs/heads/"+w.branch,
		"allows a fast-forward",
	)
	if got := w.remoteTip(); got != landed {
		t.Fatalf("the branch on the remote is %s, want it left where somebody else put it, %s", got, landed)
	}
}

// The decision is taken against the anchor the run recorded and never against
// a read taken now. The two halves run the same world and differ only in which
// observation the run carries: the one taken before the work refuses, and one
// taken after the remote moved allows the update and drops the commit.
//
// The second half is what keeps the first from passing vacuously. Without it,
// a body that refused everything would satisfy the refusal above.
func TestThePushStageDecidesOnTheAnchorTheRunObservedAndNotOnAFreshRead(t *testing.T) {
	principles.Cite(t, principles.P6)
	w := newPushWorld(t)
	beforeTheWork := w.observe()
	landed := w.advanceOutOfBand("somebody else's commit")
	head := w.commitInCopy("the change, fixed\n", "fix the change")

	out, err := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(beforeTheWork)),
	})
	assertRefused(t, out, err, "push-refused-would-discard")
	if got := w.remoteTip(); got != landed {
		t.Fatalf("an anchor taken before the work let the update through: the branch is at %s, want %s",
			got, landed)
	}

	// The same proposed commit, the same remote, and the same body, anchored
	// on where the target stands now rather than on where the run saw it.
	afterTheAdvance := w.observe()
	if afterTheAdvance.State().Commit == beforeTheWork.State().Commit {
		t.Fatal("the remote did not move between the two observations, so this compares one anchor with itself")
	}
	out, err = w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(afterTheAdvance)),
	})
	if err != nil {
		t.Fatalf("the push stage failed on an anchor naming the current tip: %v", err)
	}
	if out.Report.Normalize().HasHeld() {
		t.Fatalf("an anchor naming the current tip was refused, so the refusal above is not caused by "+
			"the anchor being older than the work: %+v", out.Report)
	}
	if got := w.remoteTip(); got != head {
		t.Fatalf("the branch on the remote is %s, want %s", got, head)
	}
}

// An allowed update that drops commits names every one of them, so a person
// reading the run can find what left the branch.
func TestAnAllowedForceNamesEveryCommitItDropped(t *testing.T) {
	principles.Cite(t, principles.P6)
	w := newPushWorld(t)
	landed := w.advanceOutOfBand("somebody else's commit")
	// Observed after the advance, so the target still stands where the run saw
	// it, and a proposed commit that does not contain it is an anchored force.
	observed := w.observe()
	head := w.commitInCopy("the change, fixed\n", "fix the change")

	out, err := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
	})
	if err != nil {
		t.Fatalf("the push stage failed: %v", err)
	}
	report := out.Report.Normalize()
	if report.HasHeld() {
		t.Fatalf("an allowed update held the run for a person: %+v", report)
	}
	if len(report.Findings) != 1 {
		t.Fatalf("an update that dropped a commit reported %d findings, want one naming it: %+v",
			len(report.Findings), report)
	}
	found := report.Findings[0]
	if found.Action != findings.ActionNote {
		t.Fatalf("the report of a completed update carries a %s finding, want a note: %+v", found.Action, found)
	}
	assertNamesAll(t, "the note on a completed force update", found.Description, landed, "no longer on it")
	if got := w.remoteTip(); got != head {
		t.Fatalf("the branch on the remote is %s, want %s", got, head)
	}
}

// What is forwarded is the commit the run's state names, not whatever the
// copy has checked out. The two are not the same thing: the copy is a linked
// worktree whose HEAD moves during a run, because the rebase stage moves it
// and every fix round commits to it.
//
// So this leaves HEAD somewhere that is neither the verified commit nor the
// commit the branch was submitted at, and requires the verified commit to be
// the one that lands. A body that read HEAD would forward the wrong commit
// here, and one that read the submitted head would forward a commit no fix
// round had reached.
func TestWhatIsForwardedIsTheStatesHeadAndNotWhateverTheCopyHasCheckedOut(t *testing.T) {
	principles.Cite(t, principles.P6)
	w := newPushWorld(t)
	observed := w.observe()
	head := w.commitInCopy("the change, fixed\n", "fix the change")
	pushGit(t, w.copyPath, "checkout", "--quiet", "--detach", "origin/main")
	elsewhere := pushGit(t, w.copyPath, "rev-parse", "HEAD")
	if elsewhere == head || elsewhere == w.published {
		t.Fatal("the copy's HEAD was not moved off both the verified and the submitted commit, " +
			"so this cannot tell the three apart")
	}

	out, err := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
	})
	if err != nil {
		t.Fatalf("the push stage failed: %v", err)
	}
	if out.Report.Normalize().HasHeld() {
		t.Fatalf("a push whose copy has another commit checked out was refused: %+v", out.Report)
	}
	if got := w.remoteTip(); got != head {
		t.Fatalf("the branch on the remote is %s, want the verified commit %s; the copy had %s "+
			"checked out and was submitted at %s", got, head, elsewhere, w.published)
	}
}

// An update proposed twice on the same anchor is not performed twice. The
// second attempt finds the target standing where the first attempt put it,
// which is not where the run observed it, and is refused as a target that
// moved.
//
// This is the claim push.go makes about a push invocation that failed for a
// reason other than the remote refusing: nothing records the update as done,
// so a later attempt runs on the same anchor, and this is what that attempt
// does when the first one had in fact landed.
func TestAnUpdateProposedAgainOnTheSameAnchorIsRefusedRatherThanRepeated(t *testing.T) {
	principles.Cite(t, principles.P6)
	w := newPushWorld(t)
	observed := w.observe()
	head := w.commitInCopy("the change, fixed\n", "fix the change")
	state := map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
	}

	if _, err := w.runPush(state); err != nil {
		t.Fatalf("the first push failed: %v", err)
	}
	if got := w.remoteTip(); got != head {
		t.Fatalf("the first push left the branch at %s, want %s", got, head)
	}

	out, err := w.runPush(state)
	description := assertRefused(t, out, err, "push-refused-target-moved")
	assertNamesAll(t, "the refusal", description, w.published, head)
	if got := w.remoteTip(); got != head {
		t.Fatalf("the branch on the remote is %s, want it left at %s", got, head)
	}
}

// The fetch this body performs before deciding moves nothing the run is
// working on. It is local bookkeeping: the branch on the remote is untouched,
// and the commit the copy has checked out is where the run left it.
//
// It is checked over a refusal, so the fetch has happened and the update has
// not, which is the state where a stray local write would be hardest to
// notice.
func TestTheFetchBeforeADecisionMovesNothingTheRunIsWorkingOn(t *testing.T) {
	w := newPushWorld(t)
	observed := w.observe()
	landed := w.advanceOutOfBand("somebody else's commit")
	head := w.commitInCopy("the change, fixed\n", "fix the change")
	before := pushGit(t, w.copyPath, "rev-parse", "HEAD")

	out, err := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
	})
	assertRefused(t, out, err, "push-refused-would-discard")
	if got := w.remoteTip(); got != landed {
		t.Fatalf("the branch on the remote is %s, want it left at %s", got, landed)
	}
	if got := pushGit(t, w.copyPath, "rev-parse", "HEAD"); got != before {
		t.Fatalf("the copy's HEAD is %s, want it left at %s", got, before)
	}
}

// A run that recorded no observation of its target has no anchor, and this
// stage refuses rather than reading one now, which is the anchor P6 forbids.
func TestAPushRefusesWhenTheRunObservedNothing(t *testing.T) {
	principles.Cite(t, principles.P6, principles.P3)
	w := newPushWorld(t)
	head := w.commitInCopy("the change, fixed\n", "fix the change")

	out, err := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:     graph.TextValue(head),
		pipeline.KeyApproved: graph.TextValue(w.published),
	})
	description := assertRefused(t, out, err, "push-no-observation")
	assertNamesAll(t, "the refusal", description, "refs/heads/"+w.branch, "Start the run again")
	if got := w.remoteTip(); got != w.published {
		t.Fatalf("the branch on the remote is %s, want it untouched at %s", got, w.published)
	}
}

// An observation this stage cannot read back is not an anchor, and a run
// carrying one is refused rather than served by a read taken now.
func TestAPushRefusesAnObservationItCannotReadBack(t *testing.T) {
	principles.Cite(t, principles.P6)
	w := newPushWorld(t)
	head := w.commitInCopy("the change, fixed\n", "fix the change")

	out, err := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue("not the notation this package writes"),
	})
	assertRefused(t, out, err, "push-unreadable-observation")
	if got := w.remoteTip(); got != w.published {
		t.Fatalf("the branch on the remote is %s, want it untouched at %s", got, w.published)
	}
}

// An observation of some other reference is a perfectly good observation and
// is not an anchor for this branch. Honouring it would forward the run's
// commit to a reference nobody asked about.
func TestAPushRefusesAnObservationTakenAgainstAnotherReference(t *testing.T) {
	principles.Cite(t, principles.P6)
	w := newPushWorld(t)
	elsewhere, err := safety.New(w.repo).Observe(context.Background(),
		safety.Target{Remote: "origin", Ref: "refs/heads/main"})
	if err != nil {
		t.Fatalf("observing refs/heads/main: %v", err)
	}
	head := w.commitInCopy("the change, fixed\n", "fix the change")

	out, runErr := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(elsewhere)),
	})
	description := assertRefused(t, out, runErr, "push-observation-of-another-reference")
	assertNamesAll(t, "the refusal", description, "refs/heads/main", "refs/heads/"+w.branch)
	if got := pushGit(t, w.upstream, "rev-parse", "refs/heads/main"); got == head {
		t.Fatal("the run's commit was forwarded to refs/heads/main, which nobody asked about")
	}
	if got := w.remoteTip(); got != w.published {
		t.Fatalf("the branch on the remote is %s, want it untouched at %s", got, w.published)
	}
}

// A credential in the remote the run recorded does not reach a report.
//
// The leak this closes is real rather than hypothetical. internal/store runs
// the redactor over exactly repository.upstream_url and repository.fork_url,
// and a graph checkpoint payload is opaque bytes nothing inspects, so a remote
// the rebase stage wrote into pipeline.KeyTargetObserved is stored exactly as
// it recorded it. Every message this stage builds names the target, and a
// report is persisted and shown.
//
// Both cases run the real body over a recorded observation carrying a
// credential, and each reaches a different message: the first is refused
// before any git runs, and the second gets as far as the fetch.
func TestACredentialInTheRecordedRemoteDoesNotReachAReport(t *testing.T) {
	const secret = "s3cr3t-token"
	credentialed := "https://" + secret + "@example.invalid/repo.git"

	for _, tc := range []struct {
		name string
		ref  string
		id   string
	}{
		// An observation of another reference, which is refused before the
		// stage opens the run's copy or runs git at all.
		{"an anchor on another reference", "refs/heads/somewhere-else", "push-observation-of-another-reference"},
		// An observation of the right reference on a remote nothing can read,
		// which is refused at the fetch. The scheme has no helper, so git
		// fails immediately and contacts nothing.
		{"a target that cannot be fetched", "", "push-target-unreadable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newPushWorld(t)
			remote := credentialed
			ref := tc.ref
			if ref == "" {
				ref = "refs/heads/" + w.branch
				remote = "xyz://" + secret + "@example.invalid/repo.git"
			}
			observed, err := safety.RestoreObservedFromCheckpoint(safety.ObservationRecord{
				Remote: remote,
				Ref:    ref,
				Exists: true,
				Commit: w.published,
			})
			if err != nil {
				t.Fatalf("restoring an observation of %s on %s: %v", ref, remote, err)
			}

			out, runErr := w.runPush(map[pipeline.Key]graph.Value{
				pipeline.KeyHead:           graph.TextValue(w.published),
				pipeline.KeyApproved:       graph.TextValue(w.published),
				pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
			})
			description := assertRefused(t, out, runErr, tc.id)
			report := out.Report.Normalize()
			for what, text := range map[string]string{
				"the refusal": description,
				"the summary": report.Summary,
			} {
				if strings.Contains(text, secret) {
					t.Fatalf("%s carries the credential the run recorded, and a report is persisted "+
						"and shown:\n%s", what, text)
				}
			}
			// The host survives, so a person still reads which remote it was:
			// a report that named no remote would pass the check above by
			// saying nothing.
			assertNamesAll(t, "the refusal", description, "example.invalid", redact.Marker)
		})
	}
}

// A credential in the recorded remote does not reach a step error either.
//
// This is the same leak as the test above on the path a report never takes. A
// push that failed for a reason other than the remote refusing is the one case
// this stage reports as a step error, and an error is not a report: it leaves
// as an error, and internal/service writes it to the home's log before
// returning it, so a credential in one is persisted to disk.
//
// The subject reaches that path through git's own URL rewriting rather than
// through a double: the recorded remote is a credentialed URL that reads as
// the upstream this test built, so the fetch and the decision succeed against
// real history, and pushes to a transport that does not exist, so the push
// fails outright instead of being refused.
func TestAStepErrorDoesNotCarryACredentialFromTheRecordedRemote(t *testing.T) {
	const secret = "s3cr3t-token"
	credentialed := "https://" + secret + "@example.invalid/repo.git"
	w := newPushWorld(t)
	appendPushConfig(t, w.gitConfig,
		"[url \""+w.upstream+"\"]\n\tinsteadOf = "+credentialed+"\n"+
			"[url \"xyz://nowhere/repo.git\"]\n\tpushInsteadOf = "+credentialed+"\n")

	observed, err := safety.RestoreObservedFromCheckpoint(safety.ObservationRecord{
		Remote: credentialed,
		Ref:    "refs/heads/" + w.branch,
		Exists: true,
		Commit: w.published,
	})
	if err != nil {
		t.Fatalf("restoring an observation of the branch on %s: %v", credentialed, err)
	}
	head := w.commitInCopy("the change, fixed\n", "fix the change")

	out, runErr := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
	})
	if runErr == nil {
		t.Fatalf("the push stage reported %+v rather than failing the step: this subject makes the "+
			"push fail for a reason other than the remote refusing, which is the one case this "+
			"stage does not turn into a finding", out.Report)
	}
	if strings.Contains(runErr.Error(), secret) {
		t.Fatalf("the step error carries the credential the run recorded, and internal/service "+
			"writes a stage's error to the home's log before returning it:\n%v", runErr)
	}
	// The host survives, so the error still says which remote it was: an error
	// naming no remote would pass the check above by saying nothing.
	assertNamesAll(t, "the step error", runErr.Error(), "example.invalid", redact.Marker)
	if got := w.remoteTip(); got != w.published {
		t.Fatalf("the branch on the remote is %s, want it untouched at %s", got, w.published)
	}
}

// PRD section 5 has this stage require a durable record that a completed
// review approved a commit this one descends from. A run with no such record
// is refused.
func TestAPushRefusesWithoutARecordOfAnApprovedCommit(t *testing.T) {
	principles.Cite(t, principles.P3)
	w := newPushWorld(t)
	observed := w.observe()
	head := w.commitInCopy("the change, fixed\n", "fix the change")

	out, err := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
	})
	description := assertRefused(t, out, err, "push-no-approval")
	assertNamesAll(t, "the refusal", description, "review", head)
	if got := w.remoteTip(); got != w.published {
		t.Fatalf("the branch on the remote is %s, want it untouched at %s", got, w.published)
	}
}

// Approving one commit says nothing about a later one, so what is required is
// that the commit being forwarded contains the approved one.
func TestAPushRefusesWhenTheApprovedCommitIsNotContainedInWhatWouldBeForwarded(t *testing.T) {
	principles.Cite(t, principles.P3)
	w := newPushWorld(t)
	observed := w.observe()
	// A commit on a history of its own, so it is related to the branch and not
	// contained in it: approving it approves something else.
	pushGit(t, w.copyPath, "checkout", "--quiet", "--detach", "origin/main")
	unrelated := w.commitInCopy("a different change\n", "a different change")
	pushGit(t, w.copyPath, "checkout", "--quiet", "--detach", w.published)
	head := w.commitInCopy("the change, fixed\n", "fix the change")

	out, err := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(unrelated),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
	})
	description := assertRefused(t, out, err, "push-approval-not-contained")
	assertNamesAll(t, "the refusal", description, unrelated, head)
	if got := w.remoteTip(); got != w.published {
		t.Fatalf("the branch on the remote is %s, want it untouched at %s", got, w.published)
	}
}

// A target whose history cannot be fetched is a target whose current contents
// are unknown, and an update to it cannot be shown to discard nothing.
func TestAPushRefusesWhenTheTargetCannotBeFetched(t *testing.T) {
	principles.Cite(t, principles.P6)
	w := newPushWorld(t)
	observed := w.observe()
	head := w.commitInCopy("the change, fixed\n", "fix the change")
	if err := os.RemoveAll(w.upstream); err != nil {
		t.Fatalf("removing %s: %v", w.upstream, err)
	}

	out, err := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
	})
	description := assertRefused(t, out, err, "push-target-unreadable")
	assertNamesAll(t, "the refusal", description, "refs/heads/"+w.branch, "Nothing was pushed")
}

// A remote that refuses the update leaves the branch where it was, and the
// stage reports the remote's own reason rather than a verdict of its own.
func TestAPushRefusesWhenTheRemoteRejectsTheUpdate(t *testing.T) {
	principles.Cite(t, principles.P3)
	w := newPushWorld(t)
	observed := w.observe()
	head := w.commitInCopy("the change, fixed\n", "fix the change")
	hook := filepath.Join(w.upstream, "hooks", "pre-receive")
	pushWrite(t, filepath.Dir(hook), "pre-receive", "#!/bin/sh\necho 'this branch is protected' >&2\nexit 1\n")
	if err := os.Chmod(hook, 0o700); err != nil {
		t.Fatalf("making %s executable: %v", hook, err)
	}

	out, err := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(w.published),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
	})
	description := assertRefused(t, out, err, "push-rejected")
	assertNamesAll(t, "the refusal", description, "refs/heads/"+w.branch, w.published)
	if got := w.remoteTip(); got != w.published {
		t.Fatalf("the branch on the remote is %s, want it untouched at %s", got, w.published)
	}
}

// The pair that carries the anchor across a restart has to round trip: what
// the rebase stage will record is what this stage decodes.
func TestTheRecordedObservationRoundTrips(t *testing.T) {
	w := newPushWorld(t)
	observed := w.observe()
	restored, err := decodeObservation(w.recorded(observed))
	if err != nil {
		t.Fatalf("reading back a recorded observation: %v", err)
	}
	if !restored.Observed() {
		t.Fatal("a restored observation does not report itself as one, so it is not an anchor")
	}
	if restored.Target() != observed.Target() || restored.State() != observed.State() {
		t.Fatalf("a recorded observation read back as %s, want %s", restored, observed)
	}
}

// The predicate the refusal tests are built on has to reject a report that
// does not refuse. Without this it would accept any report at all, and every
// refusal above would be checking nothing.
func TestTheRefusalPredicateRejectsAReportThatDoesNotHold(t *testing.T) {
	principles.Cite(t, principles.P3)
	refusing := findings.Finding{
		ID:          "push-no-observation",
		Severity:    findings.SeverityError,
		Action:      findings.ActionAsk,
		Description: "this needs a decision",
	}
	report := func(found findings.Finding) pipeline.Output {
		return pipeline.Output{Report: findings.Report{
			Summary:  "The push was refused: it says so.",
			Findings: []findings.Finding{found},
		}}
	}
	for _, c := range []struct {
		name string
		out  pipeline.Output
		err  error
	}{
		{"a note, which does not hold the run for a person", report(findings.Finding{
			ID: "push-no-observation", Action: findings.ActionNote, Description: "a note"}), nil},
		{"a fix finding, which a machine may act on", report(findings.Finding{
			ID: "push-no-observation", Action: findings.ActionFix, Description: "a fix"}), nil},
		{"a report with no findings at all", pipeline.Output{Report: findings.Report{
			Summary: "The push was refused, it says, with nothing to answer."}}, nil},
		{"a refusal that also asks for a state write", pipeline.Output{
			Report: report(refusing).Report,
			Writes: map[pipeline.Key]graph.Value{pipeline.KeyPushed: graph.TextValue("a commit")},
		}, nil},
		{"a summary that does not say the push was refused", pipeline.Output{Report: findings.Report{
			Summary: "Forwarded a commit.", Findings: []findings.Finding{refusing}}}, nil},
		{"a step that failed rather than refusing", pipeline.Output{}, errors.New("the step failed")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if refusalProblem(c.out, c.err, "push-no-observation") == "" {
				t.Fatalf("the refusal predicate accepted %+v, so every use of it above would pass "+
					"whatever the push stage did", c.out)
			}
		})
	}
	if problem := refusalProblem(report(refusing), nil, "push-no-observation"); problem != "" {
		t.Fatalf("the refusal predicate rejected a report that does refuse, so it rejects everything: %s", problem)
	}
}

// The summary a refusal carries is the refusal plus its opening sentence, and
// a sentence ends at a full stop followed by a space, a line break, or
// nothing. A full stop inside an identifier does not end one.
func TestTheRefusalSummaryIsTheOpeningSentence(t *testing.T) {
	for _, c := range []struct{ name, text, want string }{
		{"ended by a space", "The push was refused. It says why here.", "The push was refused."},
		{"ended by a line break", "Nothing was pushed.\n\nAnd here is why.", "Nothing was pushed."},
		{"ended by the end of the text", "Nothing was pushed.", "Nothing was pushed."},
		{"no full stop at all", "nothing was pushed", "nothing was pushed"},
		{"a full stop inside a name", "The file a.txt could not be read. Then this.",
			"The file a.txt could not be read."},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := firstSentence(c.text); got != c.want {
				t.Fatalf("firstSentence(%q) = %q, want %q", c.text, got, c.want)
			}
		})
	}
}

// The condition internal/fixture plants for P6 is met by this stage, down to
// the substrings that package records the message has to carry.
//
// It runs against the fixture rather than against a copy of its expectations,
// so a change to what the fixture demands reaches this test. The advance is
// applied through AdvanceRemoteOutOfBand at the point the fixture says it
// belongs, which is after the run has observed the branch and before it
// submits its update.
func TestTheFixtureConditionForARemoteAdvancedOutOfBandIsMet(t *testing.T) {
	principles.Cite(t, principles.P6)
	pushGitEnvironment(t)
	root := t.TempDir()
	built, err := fixture.Build(root)
	if err != nil {
		t.Fatalf("building the fixture under %s: %v", root, err)
	}
	condition, ok := built.Condition("refusal-remote-advanced-out-of-band")
	if !ok {
		t.Fatal("the fixture no longer plants refusal-remote-advanced-out-of-band, so this checks nothing")
	}
	if len(condition.Expect.MessageContains) == 0 {
		t.Fatal("the fixture records no expected message substrings, so this checks nothing")
	}
	scenario, ok := built.Scenario(fixture.ScenarioRemoteAdvanced)
	if !ok {
		t.Fatalf("the fixture has no %s scenario", fixture.ScenarioRemoteAdvanced)
	}

	w := &pushWorld{t: t, home: nil, branch: scenario.Branch, repoID: "repository-1", runID: "run-1"}
	w.upstream = scenario.Origin
	if w.home, err = home.Open(filepath.Join(root, "home")); err != nil {
		t.Fatalf("opening a home: %v", err)
	}
	if err := w.home.Create(); err != nil {
		t.Fatalf("creating the home at %s: %v", w.home.Root(), err)
	}
	w.copyPath = w.home.Worktree(w.repoID, w.runID)
	if err := os.MkdirAll(filepath.Dir(w.copyPath), 0o700); err != nil {
		t.Fatalf("making the directory above %s: %v", w.copyPath, err)
	}
	pushGit(t, root, "clone", "--quiet", scenario.Origin, w.copyPath)
	pushGit(t, w.copyPath, "checkout", "--quiet", "--detach", "origin/"+scenario.Branch)
	if w.repo, err = vcs.OpenWorktree(context.Background(), w.copyPath); err != nil {
		t.Fatalf("opening the run's copy: %v", err)
	}
	head := pushGit(t, w.copyPath, "rev-parse", "HEAD")

	observed := w.observe()
	landed, err := fixture.AdvanceRemoteOutOfBand(scenario)
	if err != nil {
		t.Fatalf("advancing the remote out of band: %v", err)
	}

	out, runErr := w.runPush(map[pipeline.Key]graph.Value{
		pipeline.KeyHead:           graph.TextValue(head),
		pipeline.KeyApproved:       graph.TextValue(head),
		pipeline.KeyTargetObserved: graph.TextValue(w.recorded(observed)),
	})
	description := assertRefused(t, out, runErr, "push-refused-"+string(safety.ReasonWouldDiscard))
	assertNamesAll(t, "the refusal", description, condition.Expect.MessageContains...)
	assertNamesAll(t, "the refusal", description, landed)
	if got := w.remoteTip(); got != landed {
		t.Fatalf("the branch on the remote is %s, want it left where the colleague put it, %s", got, landed)
	}
}

// pushGitEnvironment points git at a configuration file this test owns, so a
// developer's own git configuration cannot change what these tests prove. It
// is what internal/vcs's and internal/gate's own tests do, and it is why the
// tests in this file do not run in parallel. It returns that file so a test
// that needs git to behave a particular way can add to it.
func pushGitEnvironment(t *testing.T) string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	pushWrite(t, filepath.Dir(cfg), "gitconfig",
		"[user]\n\tname = Test\n\temail = test@example.invalid\n[init]\n\tdefaultBranch = main\n")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return cfg
}

// appendPushConfig adds lines to the configuration file pushGitEnvironment
// wrote.
func appendPushConfig(t *testing.T, cfg, text string) {
	t.Helper()
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("opening %s: %v", cfg, err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("appending to %s: %v", cfg, err)
	}
}

// pushGit runs git directly, without the package under test, and fails the
// test if it does not succeed.
func pushGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// pushWrite puts a file in a directory, creating the directories above it.
func pushWrite(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", full, err)
	}
}
