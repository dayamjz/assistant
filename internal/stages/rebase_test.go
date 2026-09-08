package stages_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
	"github.com/dayamjz/assistant/internal/stages"
)

// A change whose commits still say something after the replay is rebased onto
// the freshly fetched target, the run's head moves to the result, and nothing
// says the run has no diff left.
//
// The target is advanced on the remote first, so the rebase has somewhere to
// move to: a stage that fetched nothing and rebased onto what the copy already
// held would leave the head where it was and pass this test if it only asked
// whether the head changed. What it asks instead is that the result contains
// the commit that was pushed after the copy was made, which nothing but a
// fresh fetch can produce.
func TestTheChangeIsRebasedOntoTheFreshlyFetchedTarget(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	advanced := s.advanceTarget("later.txt", "landed after the copy was made")

	out, err := s.runRebase(nil)
	if err != nil {
		t.Fatalf("the rebase stage returned an error: %v", err)
	}
	rebased := s.written(out, pipeline.KeyHead)
	if rebased == s.head {
		t.Fatal("the run's head did not move, so nothing was replayed onto the fetched target")
	}
	if !s.contains(rebased, advanced) {
		t.Fatalf("the rebased head %s does not contain %s, which was pushed to the target after "+
			"the copy was made: the stage did not rebase onto a freshly fetched target", rebased, advanced)
	}
	if _, wrote := out.Writes[pipeline.KeyDiffEmpty]; wrote {
		t.Fatalf("the stage said the run has nothing left to change over a change that still has a diff: %+v", out.Report)
	}
	if why := blocked(out.Report); why != "" {
		t.Fatalf("a clean rebase blocked the run: %s\nreport: %+v", why, out.Report)
	}
}

// PRD section 5: a change with nothing left after the rebase ends the run
// successfully with the rest skipped. This is the fixture's
// stage-no-diff-after-rebase condition, planted the way that condition plants
// it: the same edit lands on the target as a separate commit with a different
// message, so the two commits differ and the branch is not already contained in
// the target before the rebase runs.
//
// The stage has to report this as an outcome rather than an error, and it has
// to be internal/pipeline's short circuit rather than a word in a summary, so
// what is asserted is the state key every stage node reads.
func TestAChangeWithNothingLeftAfterTheRebaseIsAnOutcome(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	s.landTheSameChangeOnTheTarget()

	out, err := s.runRebase(nil)
	if err != nil {
		t.Fatalf("the rebase stage returned an error over a change that rebased to nothing: %v", err)
	}
	empty, wrote := out.Writes[pipeline.KeyDiffEmpty]
	if !wrote {
		t.Fatalf("the stage did not set %s over a change already on the target, so the run would go on "+
			"validating a change that no longer exists\nreport: %+v", pipeline.KeyDiffEmpty, out.Report)
	}
	if nothingLeft, _ := empty.Bool(); !nothingLeft {
		t.Fatalf("the stage set %s to %v, want true", pipeline.KeyDiffEmpty, empty)
	}
	if why := blocked(out.Report); why != "" {
		t.Fatalf("a change that rebased to nothing blocked the run: %s\nreport: %+v", why, out.Report)
	}
	if len(out.Report.Findings) != 0 {
		t.Fatalf("the stage reported %d finding(s) against a change that no longer exists: %+v",
			len(out.Report.Findings), out.Report.Findings)
	}
}

// And the assertion above has to be able to tell the two apart, or it reports
// the same result whatever the stage does. The subject here is the ordinary
// one, whose change still differs from the target after the replay, and the
// key must not be set for it.
func TestTheNothingLeftAssertionRejectsAChangeThatStillHasOne(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	out, err := s.runRebase(nil)
	if err != nil {
		t.Fatalf("the rebase stage returned an error: %v", err)
	}
	if _, wrote := out.Writes[pipeline.KeyDiffEmpty]; wrote {
		t.Fatalf("the stage set %s over a change that still differs from the target, so the test above "+
			"cannot tell a change that rebased to nothing from one that did not", pipeline.KeyDiffEmpty)
	}
}

// P6's anchor is taken before the run does its work and used after it, so this
// stage observes the branch target and records where it stood. What is checked
// is the record: the commit in it is the one the target named before the
// rebase, and internal/safety accepts it as an anchor.
func TestTheStageRecordsTheAnchorItObserved(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)

	s := newSubject(t)
	out, err := s.runRebase(nil)
	if err != nil {
		t.Fatalf("the rebase stage returned an error: %v", err)
	}
	observation := s.anchor(out)
	if got := observation.State().Commit; got != s.head {
		t.Fatalf("the anchor names %s, and the branch stood at %s when the run started: the stage "+
			"anchored on something other than what it observed", got, s.head)
	}
	if got := observation.Target().Ref; got != "refs/heads/"+subjectBranch {
		t.Fatalf("the anchor was taken against %s, want the branch under validation", got)
	}
	if !observation.Observed() {
		t.Fatal("internal/safety does not accept the recorded anchor as an observation, so the push " +
			"stage has nothing it may decide on")
	}
}

// The anchor is taken once per run and never again, which is the whole of what
// makes it worth anything: a fix round re-runs this stage, and re-observing
// there would replace an anchor taken before the work with one taken after it,
// which is the tip read a moment before writing that P6 names outright.
//
// The second run is given the record the first one wrote and a target that has
// moved since. A stage that observed again would record the new commit; the
// stage has to keep the old one, and it has to write nothing at all rather than
// rewriting the record with the same value, so nothing later can be pointed at
// a write that happened after the work.
func TestTheAnchorIsNotTakenAgainOnALaterRound(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)

	s := newSubject(t)
	first, err := s.runRebase(nil)
	if err != nil {
		t.Fatalf("the rebase stage returned an error: %v", err)
	}
	recorded := s.written(first, pipeline.KeyObservation)
	moved := s.advanceBranch("more.txt", "somebody else pushes to the branch")
	if moved == s.head {
		t.Fatal("the branch did not move, so this test cannot tell a stage that re-observes from one that does not")
	}

	second, err := s.runRebase(map[pipeline.Key]graph.Value{
		pipeline.KeyObservation: graph.TextValue(recorded),
	})
	if err != nil {
		t.Fatalf("the rebase stage returned an error on the round that already had an anchor: %v", err)
	}
	if _, wrote := second.Writes[pipeline.KeyObservation]; wrote {
		t.Fatalf("a later round rewrote the run's anchor, so the anchor no longer describes the "+
			"target as it stood before the run did its work: %+v", second.Writes[pipeline.KeyObservation])
	}
	kept := s.decodeAnchor(recorded)
	if got := kept.State().Commit; got != s.head {
		t.Fatalf("the run's anchor names %s after the branch moved to %s, want the commit the run "+
			"observed, %s", got, moved, s.head)
	}
}

// PRD section 5 stops this stage when the change would quietly carry commits
// from the person's local target branch that were never pushed. The copy's own
// target reference is what stands for that branch here, and the finding has to
// hold the stage for a person: nobody but them can say whether work that never
// reached the remote belongs in this change.
//
// Nothing is rebased when it fires, because folding the commits in and then
// reporting them would be doing the thing the stop exists to prevent.
func TestUnpushedTargetCommitsUnderTheChangeHoldTheStage(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)

	s := newSubject(t)
	carried := s.buildOnAnUnpushedTargetCommit()

	out, err := s.runRebase(nil)
	if err != nil {
		t.Fatalf("the rebase stage returned an error: %v", err)
	}
	if why := blocked(out.Report); why == "" {
		t.Fatalf("the stage did not hold over a change carrying a commit that never reached the "+
			"remote\nreport: %+v", out.Report)
	}
	held := out.Report.Normalize().Held()
	if len(held) == 0 {
		t.Fatalf("the stage reported no finding a person answers: %+v", out.Report)
	}
	if !strings.Contains(held[0].Description, carried) {
		t.Fatalf("the finding does not name %s, the commit that would be carried: %s", carried, held[0].Description)
	}
	if _, wrote := out.Writes[pipeline.KeyHead]; wrote {
		t.Fatal("the stage rebased anyway and moved the run's head, so the commits it reported carrying were carried")
	}
}

// The check above must not be one that concludes without comparing anything. A
// copy with no target reference of its own has nothing to compare, and the
// stage has to say the check was not made rather than report a clean result.
func TestTheUnpushedCheckSaysSoWhenItCannotBeMade(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	s.dropTheLocalTargetReference()

	out, err := s.runRebase(nil)
	if err != nil {
		t.Fatalf("the rebase stage returned an error: %v", err)
	}
	if !s.reports(out, "rebase-unpushed-check-not-made") {
		t.Fatalf("the stage reported nothing about a check it could not make, so a copy that cannot "+
			"answer the question looks exactly like one that answered it clean\nreport: %+v", out.Report)
	}
	if why := blocked(out.Report); why != "" {
		t.Fatalf("saying a check could not be made blocked the run: %s", why)
	}
}

// A conflict is what config.FixRounds.Rebase exists for, so it is reported as
// a fix-eligible finding rather than as an error that stops the run, and the
// copy is left with no rebase in progress: a caller that stops here is not
// holding a repository nothing can be done with.
func TestAConflictingRebaseIsAFixEligibleFinding(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	s.conflictWithTheChange()

	out, err := s.runRebase(nil)
	if err != nil {
		t.Fatalf("the rebase stage returned an error rather than a finding a fix round can act on: %v", err)
	}
	fixable := out.Report.Normalize().Fixable()
	if len(fixable) == 0 {
		t.Fatalf("a conflicting rebase produced no fix-eligible finding, so the stage's fix rounds "+
			"have nothing to act on\nreport: %+v", out.Report)
	}
	if !strings.Contains(fixable[0].Description, "change.txt") {
		t.Fatalf("the finding does not name the conflicting path: %s", fixable[0].Description)
	}
	if _, wrote := out.Writes[pipeline.KeyHead]; wrote {
		t.Fatal("the stage moved the run's head over a rebase that did not finish")
	}
	if s.rebaseInProgress() {
		t.Fatal("the copy was left mid-rebase, so the fix round would be handed a repository it cannot work in")
	}
}

// A copy whose remote is not the one this stage fetches from fails the stage,
// rather than fetching from whatever happens to be configured or rebasing onto
// what the copy already held.
//
// What is asserted is the diagnosis and not only the failure. Every git
// invocation this stage makes against a remote that is not there fails on its
// own, so a test asking only whether the stage failed would pass with no such
// check in the code at all. This stage's documentation promises a message
// naming what is missing, because that message is the difference between an
// operator reading "this copy has no origin remote" and reading whatever git
// says about a repository it could not reach, and a doc comment is a contract.
func TestACopyWithNoUpstreamRemoteFailsTheStage(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	git(t, s.copy, "remote", "rename", "origin", "somewhere-else")

	_, err := s.runRebase(nil)
	if err == nil {
		t.Fatal("the stage ran against a copy with no remote to fetch the upstream from")
	}
	for _, want := range []string{`no "origin" remote`, s.copy, "cannot be fetched"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the failure does not say %q, so it does not name what is missing: %v", want, err)
		}
	}
}

// The helpers the tests above lean on. Each one either plants a condition on
// the subject or reads an answer out of what the stage returned.

// written returns the value the stage asked to write for a key, failing when
// it wrote none.
func (s *subject) written(out pipeline.Output, key pipeline.Key) string {
	s.t.Helper()
	value, ok := out.Writes[key]
	if !ok {
		s.t.Fatalf("the stage wrote no %s: %+v", key, out.Writes)
	}
	text, _ := value.Text()
	return text
}

// anchor decodes the observation the stage recorded on this run.
func (s *subject) anchor(out pipeline.Output) safety.Observation {
	s.t.Helper()
	return s.decodeAnchor(s.written(out, pipeline.KeyObservation))
}

// decodeAnchor reads a recorded observation back the way the push stage will,
// through internal/safety rather than through this test's own reading of the
// fields.
func (s *subject) decodeAnchor(recorded string) safety.Observation {
	s.t.Helper()
	var rec safety.ObservationRecord
	if err := json.Unmarshal([]byte(recorded), &rec); err != nil {
		s.t.Fatalf("the recorded anchor is not a record internal/safety wrote: %v", err)
	}
	observation, err := safety.RestoreObservedFromCheckpoint(rec)
	if err != nil {
		s.t.Fatalf("internal/safety refused the recorded anchor: %v", err)
	}
	return observation
}

// reports says whether the stage reported a finding with this identifier.
func (s *subject) reports(out pipeline.Output, id string) bool {
	for _, finding := range out.Report.Normalize().Findings {
		if finding.ID == id {
			return true
		}
	}
	return false
}

// contains reports whether one commit's history holds another.
func (s *subject) contains(commit, ancestor string) bool {
	s.t.Helper()
	for _, line := range strings.Split(git(s.t, s.copy, "rev-list", commit), "\n") {
		if strings.TrimSpace(line) == ancestor {
			return true
		}
	}
	return false
}

// rebaseInProgress reports whether the copy is standing in a rebase. Both
// state directories are checked because which one git uses is the backend's
// choice and not something this test should pin.
func (s *subject) rebaseInProgress() bool {
	s.t.Helper()
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(s.copy, ".git", name)); err == nil {
			return true
		}
	}
	return false
}

// advanceTarget lands a commit on the target branch of the remote, after the
// copy was made.
func (s *subject) advanceTarget(name, message string) string {
	s.t.Helper()
	git(s.t, s.author, "checkout", "--quiet", subjectBase)
	write(s.t, s.author, name, message+"\n")
	commit := commit(s.t, s.author, message)
	git(s.t, s.author, "push", "--quiet", "origin", subjectBase)
	return commit
}

// advanceBranch lands a commit on the branch under validation of the remote,
// which is somebody else pushing to the branch a run is validating.
func (s *subject) advanceBranch(name, message string) string {
	s.t.Helper()
	git(s.t, s.author, "checkout", "--quiet", subjectBranch)
	write(s.t, s.author, name, message+"\n")
	commit := commit(s.t, s.author, message)
	git(s.t, s.author, "push", "--quiet", "origin", subjectBranch)
	git(s.t, s.author, "checkout", "--quiet", subjectBase)
	return commit
}

// landTheSameChangeOnTheTarget puts the change's own edit on the target under a
// different author's message, which is what somebody else shipping the change
// first looks like. The two commits differ, so the branch is not contained in
// the target until the rebase runs.
func (s *subject) landTheSameChangeOnTheTarget() {
	s.t.Helper()
	git(s.t, s.author, "checkout", "--quiet", subjectBase)
	write(s.t, s.author, "change.txt", "the change\n")
	commit(s.t, s.author, "ship the change under another name")
	git(s.t, s.author, "push", "--quiet", "origin", subjectBase)
}

// conflictWithTheChange lands an edit on the target to the file the change also
// edits, so replaying the change onto it cannot apply cleanly.
func (s *subject) conflictWithTheChange() {
	s.t.Helper()
	git(s.t, s.author, "checkout", "--quiet", subjectBase)
	write(s.t, s.author, "change.txt", "something else entirely\n")
	commit(s.t, s.author, "edit the same file on the target")
	git(s.t, s.author, "push", "--quiet", "origin", subjectBase)
}

// buildOnAnUnpushedTargetCommit puts a commit on the copy's own target branch
// that the remote does not have and rewrites the change to stand on it, which
// is a change built on a local target branch nobody pushed. It returns the
// commit that would be carried.
func (s *subject) buildOnAnUnpushedTargetCommit() string {
	s.t.Helper()
	git(s.t, s.copy, "checkout", "--quiet", subjectBase)
	write(s.t, s.copy, "local-only.txt", "never pushed anywhere\n")
	carried := commit(s.t, s.copy, "a commit that never left this machine")
	git(s.t, s.copy, "checkout", "--quiet", "-b", "restack", carried)
	write(s.t, s.copy, "change.txt", "the change\n")
	s.head = commit(s.t, s.copy, "make the change on top of it")
	git(s.t, s.copy, "checkout", "--quiet", "--detach", s.head)
	git(s.t, s.copy, "branch", "--quiet", "-D", "restack")
	return carried
}

// dropTheLocalTargetReference leaves the copy with no reference of its own for
// the target branch, which is a copy the unpushed check has nothing to compare.
func (s *subject) dropTheLocalTargetReference() {
	s.t.Helper()
	git(s.t, s.copy, "branch", "--quiet", "-D", subjectBase)
}

// PRD section 5 does not only say the stage records that nothing is left; it
// says the run then ends successfully with the rest of the stages skipped. A
// report-shaped assertion cannot see that, so this runs a real pipeline built
// from this build's own stages over a real subject.
//
// It is worth running against a pipeline whose later stages have no body,
// because that is what this build has: every one of them holds for a person,
// so a run that reaches any of them stops. Completing is therefore evidence
// that none of them ran.
func TestARunWithNothingLeftAfterTheRebaseCompletesWithTheRestSkipped(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	s.landTheSameChangeOnTheTarget()

	result := s.runPipeline()
	if result.Status != graph.StatusCompleted {
		t.Fatalf("the run came to %s rather than completing, and a change with nothing left in it "+
			"ends the run successfully", result.Status)
	}
	for _, stage := range pipeline.Order() {
		got := pipeline.StageOutcome(result.State, stage)
		want := pipeline.OutcomeSkipped
		if stage == pipeline.StageIntent || stage == pipeline.StageRebase {
			want = pipeline.OutcomePassed
		}
		if got != want {
			t.Fatalf("the %s stage came to %s, want %s: a run with no diff left validates nothing after the rebase",
				stage, got, want)
		}
	}
}

// And that run has to be able to stop, or completing says nothing about the
// empty-diff short circuit. The same pipeline over a change that still has a
// diff has to hold at the first stage without a body.
func TestTheSameRunStopsWhenTheChangeStillHasADiff(t *testing.T) {
	t.Parallel()

	s := newSubject(t)
	result := s.runPipeline()
	if result.Status == graph.StatusCompleted {
		t.Fatal("a run over a change that still has a diff completed through nine stages this build " +
			"has bodies for two of, so completing above proves nothing")
	}
	first := stages.Implemented()
	unimplemented := pipeline.StageInvalid
	for _, stage := range pipeline.Order() {
		if !slices.Contains(first, stage) {
			unimplemented = stage
			break
		}
	}
	if got := pipeline.StageOutcome(result.State, unimplemented); got != pipeline.OutcomeHeld {
		t.Fatalf("the run's %s stage came to %s, want held: the run did not stop where this build stops",
			unimplemented, got)
	}
}

// The fixture's out-of-band remote advance is recovered from by re-running,
// and what makes that work is this stage: a branch somebody else pushed to is
// replayed onto rather than dropped, so the commit the run proposes contains
// theirs.
func TestABranchAdvancedOnTheRemoteIsReplayedOntoRatherThanDropped(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)

	s := newSubject(t)
	colleague := s.advanceBranch("theirs.txt", "somebody else lands work on the branch")

	out, err := s.runRebase(nil)
	if err != nil {
		t.Fatalf("the rebase stage returned an error: %v", err)
	}
	rebased := s.written(out, pipeline.KeyHead)
	if !s.contains(rebased, colleague) {
		t.Fatalf("the commit this run proposes, %s, does not contain %s, which somebody else pushed "+
			"to the branch: the run would ask to discard their work", rebased, colleague)
	}
	if got := s.anchor(out).State().Commit; got != colleague {
		t.Fatalf("the anchor names %s and the branch stood at %s when the run looked: the anchor is "+
			"not what the run observed", got, colleague)
	}
}
