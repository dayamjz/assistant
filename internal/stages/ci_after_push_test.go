package stages_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
)

// openPullRequest is gh's pr view answer for pull request 7 of the checks
// tests, open, on head, with the mergeability gh reports in its own words.
func openPullRequest(head, mergeable string) forgetest.Response {
	return forgetest.PullRequest(forgetest.PullRequestView{Number: 7, State: "OPEN", Mergeable: mergeable,
		Head: "topic", HeadCommit: head, Base: "main", Title: "sum the values",
		URL: "https://github.com/dayamjz/assistant/pull/7"})
}

// passedChecks is gh's check rollup for head with one check that passed.
func passedChecks(head string) forgetest.Response {
	return forgetest.Checks(head, forgetest.CheckRunView{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"})
}

// failedChecks is gh's check rollup for head with one check that failed.
func failedChecks(head string) forgetest.Response {
	return forgetest.Checks(head, forgetest.CheckRunView{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE"})
}

// writesTo reports the calls a stand-in code host recorded that would change
// something there. The checks stage reads and nothing else.
func writesTo(code *forgetest.Provider) []forgetest.Call {
	var writes []forgetest.Call
	for _, call := range code.Calls() {
		switch call.Op {
		case forgetest.OpView, forgetest.OpChecks:
		default:
			writes = append(writes, call)
		}
	}
	return writes
}

// A pull request whose checks have passed, and which the code host then
// reports as conflicting with its base, holds the checks stage for a person
// with ci-conflicted rather than passing, and nothing is written to the code
// host. The pull request is read through the real GitHub adapter over the
// stand-in, so the mergeability the body acts on is one gh's own words
// produce.
//
// The first read finds the checks passed and the mergeability not yet
// reported, which is a wait; the second finds the conflict. The control is
// the same run with the second read reporting the pull request mergeable,
// which passes: without it, a body that held on every passed read would pass
// the first half.
func TestAPullRequestThatConflictsAfterItsChecksPassedHoldsTheChecksStage(t *testing.T) {
	t.Parallel()
	const head = "4f1c0de4f1c0de4f1c0de4f1c0de4f1c0de4f1c0"
	for _, tc := range []struct {
		name      string
		mergeable string
		wantHold  bool
	}{
		{"conflicting", "CONFLICTING", true},
		{"control: mergeable", "MERGEABLE", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newChecksRun(t)
			code := forgetest.New(t, forgetest.Script{
				forgetest.OpView:   {openPullRequest(head, "UNKNOWN"), openPullRequest(head, tc.mergeable)},
				forgetest.OpChecks: {passedChecks(head)},
			})
			r.host = code.Host()

			report := r.mustRun(t)

			if report.HasHeld() != tc.wantHold || hasFinding(report, "ci-conflicted") != tc.wantHold {
				t.Fatalf("with the pull request %s after its checks passed, held = %v, want %v: %+v",
					tc.mergeable, report.HasHeld(), tc.wantHold, report)
			}
			if r.pauses != 1 {
				t.Fatalf("the body paused %d times, want once: the first read found the mergeability unreported", r.pauses)
			}
			if writes := writesTo(code); len(writes) != 0 {
				t.Fatalf("the checks stage wrote to the code host: %+v", writes)
			}
			if !tc.wantHold {
				return
			}
			if stages.ChecksTimeoutHeld(report) {
				t.Fatalf("a conflict is a person's decision, and it reads as the elapsed hold the service may resolve: %+v", report)
			}
			for _, want := range []string{"conflict", "does not rebase", "push it through the gate"} {
				if !strings.Contains(report.Summary+" "+report.Findings[0].Description, want) {
					t.Fatalf("the conflict hold does not say %q: %+v", want, report)
				}
			}
		})
	}
}

// Checks that passed on a pull request the code host never reports as
// mergeable are a wait, and an elapsed one holds with ci-mergeability-unknown,
// which is the elapsed hold the service may re-read before resuming. The
// control is the conflict test above, whose second read reports MERGEABLE and
// passes after one pause.
func TestPassedChecksWaitForTheCodeHostToReportMergeability(t *testing.T) {
	t.Parallel()
	const head = "4f1c0de4f1c0de4f1c0de4f1c0de4f1c0de4f1c0"
	r := newChecksRun(t)
	code := forgetest.New(t, forgetest.Script{
		forgetest.OpView:   {openPullRequest(head, "UNKNOWN")},
		forgetest.OpChecks: {passedChecks(head)},
	})
	r.host = code.Host()

	report := r.mustRun(t)

	if !report.HasHeld() || !hasFinding(report, "ci-mergeability-unknown") {
		t.Fatalf("passed checks on a pull request never reported mergeable did not hold on its mergeability: %+v", report)
	}
	if waited := r.now.Sub(r.start); waited != checksTimeout {
		t.Fatalf("the stage held after waiting %s, want exactly the %s checks_timeout", waited, checksTimeout)
	}
	if !stages.ChecksTimeoutHeld(report) {
		t.Fatalf("an elapsed wait on mergeability is not recognized as the elapsed hold: %+v", report)
	}
}

// TestAPullRequestThatConflictsAfterThePushIsRebasedOnlyCleanlyAndPushedOnlyOnItsOwnAnchor
// is PRD section 14 decision 7's rebase of a conflicted pull request through a
// real pipeline: the real rebase, review, push and checks stages, over a
// subject with an upstream to push to and a stand-in code host the real
// GitHub adapter reads. The review passes, the push stage forwards the
// branch, and then a commit lands on the base and the code host reports the
// pull request conflicting, which sends the run back to rebase.
//
// The control is a base commit the change's own commit replays onto cleanly:
// the run rebases, reviews the result in a session of its own, runs the
// checks before push, pushes the rebased head anchored at the commit it
// pushed the first time, and reaches the checks stage again on that head,
// where the pull request now merges and the run completes.
//
// The other three are where it must not: a base commit touching the line the
// change touched, which does not replay cleanly and holds at rebase with
// nothing rewritten; the branch moved out of band after the first push and
// before the second pass observed it, which holds at rebase; and the branch
// moved out of band after the second pass observed it and before the second
// push, which the push stage refuses and holds. In each, the commit somebody
// else put on the base or the branch is still where they put it.
//
// This is P6 for the second update of a branch: the rewrite is anchored to the
// commit the run itself pushed, never to a tip read just before pushing, and
// a fact that cannot be verified holds. It is P4 for the second review: every
// review invocation the run made is session-free.
func TestAPullRequestThatConflictsAfterThePushIsRebasedOnlyCleanlyAndPushedOnlyOnItsOwnAnchor(t *testing.T) {
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P4)
	t.Parallel()

	type outcome struct {
		status   graph.Status
		position string
		finding  string
	}
	for _, tc := range []struct {
		name string
		// clean says the commit landing on the base replays the change
		// cleanly; otherwise it touches the line the change touched.
		clean bool
		// moveAfterPush and moveBeforeSecondPush push a commit onto the
		// branch out of band, before the second pass observes it or after.
		moveAfterPush        bool
		moveBeforeSecondPush bool
		want                 outcome
	}{
		{name: "control: a clean rebase is reviewed, pushed again, and checked again", clean: true,
			want: outcome{graph.StatusCompleted, "", ""}},
		{name: "conflicting content holds at rebase",
			want: outcome{graph.StatusHalted, pipeline.StageRebase.HoldNode(), "rebase-after-push-conflict"}},
		{name: "a branch moved after the first push holds at rebase", clean: true, moveAfterPush: true,
			want: outcome{graph.StatusHalted, pipeline.StageRebase.HoldNode(), "rebase-after-push-branch-moved"}},
		{name: "a branch moved after the second observation refuses the second push", clean: true,
			moveBeforeSecondPush: true,
			want:                 outcome{graph.StatusHalted, pipeline.StagePush.HoldNode(), "push-refused"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := newChecksPipeline(t, 0, func(head string) forgetest.Script {
				return forgetest.Script{
					forgetest.OpView:   {openPullRequest(head, "CONFLICTING"), openPullRequest(head, "MERGEABLE")},
					forgetest.OpChecks: {passedChecks(head)},
				}
			})
			var moved string
			// The base moves on the upstream once the first push has
			// happened, which is the other pull request that merged first.
			run.afterPush = func(t *testing.T, pass int) {
				if pass != 1 {
					return
				}
				run.conflicting = run.landOnBase(t, tc.clean)
				if tc.moveAfterPush {
					moved = run.landOnBranch(t)
				}
			}
			run.beforePush = func(t *testing.T, pass int) {
				if pass == 2 && tc.moveBeforeSecondPush {
					moved = run.landOnBranch(t)
				}
			}
			result := run.execute(t)

			report := func(stage pipeline.Stage) findings.Report {
				t.Helper()
				r, err := pipeline.StageReport(result.State, stage)
				if err != nil {
					t.Fatalf("reading the %s stage's report: %v", stage, err)
				}
				return r
			}
			if result.Status != tc.want.status || result.Position != tc.want.position {
				t.Fatalf("the run is %s at %q, want %s at %q:\nrebase: %+v\npush: %+v\nchecks: %+v",
					result.Status, result.Position, tc.want.status, tc.want.position,
					report(pipeline.StageRebase), report(pipeline.StagePush), report(pipeline.StageCI))
			}
			if run.conflicting == "" {
				t.Fatalf("nothing landed on the base after the push, so this run had nothing to conflict with")
			}
			if got := git(t, run.upstream, "rev-parse", "refs/heads/main"); got != run.conflicting {
				t.Fatalf("the base stands at %s, want the commit that landed first, %s, left where it landed", got, run.conflicting)
			}
			for _, call := range run.agent.Calls() {
				if call.Session() != "" {
					t.Fatalf("an invocation resumed session %q, and every one here is a review:\n%v", call.Session(), call)
				}
			}
			upstream := git(t, run.upstream, "rev-parse", "refs/heads/topic")

			if tc.want.finding != "" {
				stage, _ := pipeline.StageOfNode(tc.want.position)
				if !hasFinding(report(stage), tc.want.finding) {
					t.Fatalf("the %s stage holds without %s: %+v", stage, tc.want.finding, report(stage))
				}
				want := run.subject.head
				if moved != "" {
					want = moved
				}
				if upstream != want {
					t.Fatalf("the pushed branch stands at %s, want %s, which nothing in this run may overwrite", upstream, want)
				}
				if tc.moveBeforeSecondPush && len(run.agent.Calls()) != 2 {
					t.Fatalf("the agent was invoked %d time(s), want the first review and the second:\n%v",
						len(run.agent.Calls()), run.agent.Calls())
				}
				if !tc.moveBeforeSecondPush && len(run.agent.Calls()) != 1 {
					t.Fatalf("the agent was invoked %d time(s), want the first review alone: nothing was rebased to review:\n%v",
						len(run.agent.Calls()), run.agent.Calls())
				}
				if copied := run.copyHead(t); !tc.moveBeforeSecondPush && copied != run.subject.head {
					t.Fatalf("the run's copy stands at %s after the hold, want the pushed commit %s", copied, run.subject.head)
				}
				return
			}

			// The control: a second review of the rebased head, a second push
			// of it anchored on the first, and the checks read again.
			rebased := textAt(t, result.State, pipeline.KeyPushed)
			if rebased == run.subject.head || upstream != rebased {
				t.Fatalf("the pushed branch stands at %s and the run pushed %s, want a rebased head other than %s on both",
					upstream, rebased, run.subject.head)
			}
			// git exits non-zero, which fails the test, unless the commit that
			// landed first is in the rebased head's history.
			git(t, run.upstream, "merge-base", "--is-ancestor", run.conflicting, rebased)
			if r := report(pipeline.StageReview); r.Revision != rebased {
				t.Fatalf("the review stage last read %s, want the rebased head %s", r.Revision, rebased)
			}
			calls := run.agent.Calls()
			if len(calls) != 2 || !strings.Contains(calls[1].Prompt, rebased) {
				t.Fatalf("the agent was invoked %d time(s), want the first review and a second naming %s:\n%v",
					len(calls), rebased, calls)
			}
			if !hasFinding(report(pipeline.StagePush), "push-rewrote-branch") ||
				!strings.Contains(report(pipeline.StagePush).Findings[0].Description, run.subject.head) {
				t.Fatalf("the second push does not say it replaced the first push's %s: %+v",
					run.subject.head, report(pipeline.StagePush))
			}
			if got := pipeline.StageOutcome(result.State, pipeline.StageCI); got != pipeline.OutcomePassed {
				t.Fatalf("the checks stage ended %q on the rebased head, want passed", got)
			}
		})
	}
}

// TestARedCIRunIsRepairedReviewedAgainAndPushedOnlyOnItsOwnAnchor is PRD
// section 5's repair of a red CI run, as the owner decided it on #325,
// through a real pipeline: the real rebase, review, push and checks stages
// and the real fix body, over a subject with an upstream to push to and a
// stand-in code host the real GitHub adapter reads. The review passes, the
// push stage forwards the branch, the checks fail, and the checks stage's one
// fix round commits a repair in the run's copy.
//
// The control is the repair going all the way: the checks stage observes the
// branch where the run pushed it and sends the run back to review, which
// reviews the repaired head in a session of its own; the push stage pushes it
// anchored on the commit the run pushed first; and the checks stage reads the
// checks again on what it pushed, where they pass and the run completes with
// no hold. The run's record says one repair was sent back and pushed again.
//
// The other three are where it must not go on. The branch moved out of band
// after the first push and before the checks stage observed it, which holds
// at the checks stage with ci-fix-branch-moved and sends nothing for review;
// the branch moved out of band after that observation and before the second
// push, which the push stage refuses and holds; and checks still failing on
// the repair, which parks the run on the checks round limit in front of the
// fixer rather than taking a second round. In the first two, the commit
// somebody else put on the branch is still where they put it.
//
// This is P6 for the second update of a branch: the update is anchored to the
// commit the run itself pushed, observed before the review and the push that
// follow, never to a tip read just before pushing, and a branch that moved is
// refused rather than adopted. It is P5 for the repair: the commit a fix round
// wrote after the push reaches the pull request only after a full review of
// it. And it is P4: the review of the repair resumes no session, and no
// invocation the run made resumed one.
func TestARedCIRunIsRepairedReviewedAgainAndPushedOnlyOnItsOwnAnchor(t *testing.T) {
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P5)
	principles.Cite(t, principles.P4)
	t.Parallel()

	type outcome struct {
		status   graph.Status
		position string
		finding  string
	}
	for _, tc := range []struct {
		name string
		// moveBeforeObservation and moveBeforeSecondPush push a commit onto
		// the branch out of band, after the first push and before the checks
		// stage observes it for the repair, or after that and before the
		// second push.
		moveBeforeObservation bool
		moveBeforeSecondPush  bool
		// stillRed has the checks fail on the repaired head as well.
		stillRed bool
		want     outcome
	}{
		{name: "control: the repair is reviewed again, pushed on its anchor, and checked green",
			want: outcome{graph.StatusCompleted, "", ""}},
		{name: "a branch moved before the repair was observed holds at the checks stage",
			moveBeforeObservation: true,
			want:                  outcome{graph.StatusHalted, pipeline.StageCI.HoldNode(), "ci-fix-branch-moved"}},
		{name: "a branch moved after the observation refuses the second push",
			moveBeforeSecondPush: true,
			want:                 outcome{graph.StatusHalted, pipeline.StagePush.HoldNode(), "push-refused"}},
		{name: "checks still red on the repair park on the round limit",
			stillRed: true,
			want:     outcome{graph.StatusRoundsExhausted, pipeline.StageCI.FixNode(), ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := newChecksPipeline(t, 1, func(head string) forgetest.Script {
				return forgetest.Script{
					forgetest.OpView:   {openPullRequest(head, "MERGEABLE")},
					forgetest.OpChecks: {failedChecks(head)},
				}
			})
			var moved string
			run.afterPush = func(t *testing.T, pass int) {
				if pass == 1 {
					if tc.moveBeforeObservation {
						moved = run.landOnBranch(t)
					}
					return
				}
				// The second push has happened, so the code host reports the
				// pull request on the commit it forwarded, and its checks.
				pushed := git(t, run.upstream, "rev-parse", "refs/heads/topic")
				checks := passedChecks(pushed)
				if tc.stillRed {
					checks = failedChecks(pushed)
				}
				run.code.Rescript(t, forgetest.Script{
					forgetest.OpView:   {openPullRequest(pushed, "MERGEABLE")},
					forgetest.OpChecks: {checks},
				})
			}
			run.beforePush = func(t *testing.T, pass int) {
				if pass == 2 && tc.moveBeforeSecondPush {
					moved = run.landOnBranch(t)
				}
			}
			result := run.execute(t)

			report := func(stage pipeline.Stage) findings.Report {
				t.Helper()
				r, err := pipeline.StageReport(result.State, stage)
				if err != nil {
					t.Fatalf("reading the %s stage's report: %v", stage, err)
				}
				return r
			}
			if result.Status != tc.want.status || result.Position != tc.want.position {
				t.Fatalf("the run is %s at %q, want %s at %q:\nreview: %+v\npush: %+v\nchecks: %+v",
					result.Status, result.Position, tc.want.status, tc.want.position,
					report(pipeline.StageReview), report(pipeline.StagePush), report(pipeline.StageCI))
			}
			repaired := run.copyHead(t)
			if repaired == run.subject.head {
				t.Fatalf("the checks round committed nothing, so this run has no repair")
			}
			// The harness's script answers a review of later commits at step
			// 0, a review at step 1, and the fixer at step 2.
			reviews := 0
			for _, call := range run.agent.Calls() {
				if call.Session() != "" {
					t.Fatalf("an invocation resumed session %q:\n%v", call.Session(), call)
				}
				if call.Step == 0 {
					t.Fatalf("the push stage reviewed later commits, and the review stage had already read the repair:\n%s",
						call.Prompt)
				}
				if call.Step == 1 && strings.Contains(call.Prompt, "It must be exactly "+repaired) {
					reviews++
				}
			}
			upstream := git(t, run.upstream, "rev-parse", "refs/heads/topic")
			repairs, err := pipeline.RepairsOf(result.State)
			if err != nil {
				t.Fatalf("reading the run's repairs: %v", err)
			}

			if tc.moveBeforeObservation || tc.moveBeforeSecondPush {
				stage, _ := pipeline.StageOfNode(tc.want.position)
				r := report(stage)
				if !hasFinding(r, tc.want.finding) {
					t.Fatalf("the %s stage holds without %s: %+v", stage, tc.want.finding, r)
				}
				if upstream != moved {
					t.Fatalf("the pushed branch stands at %s, want %s, which somebody else pushed and nothing in "+
						"this run may overwrite", upstream, moved)
				}
				if tc.moveBeforeObservation {
					if reviews != 0 || len(repairs) != 0 {
						t.Fatalf("a repair the checks stage refused was reviewed %d time(s) and recorded as %+v",
							reviews, repairs)
					}
					for _, want := range []string{repaired, run.subject.head} {
						if !strings.Contains(r.Summary, want) || !strings.Contains(r.Findings[0].Description, want) {
							t.Fatalf("the hold does not name %s: %+v", want, r)
						}
					}
					return
				}
				if reviews != 1 || len(repairs) != 1 || repairs[0].Republished {
					t.Fatalf("the refused repair was reviewed %d time(s) and recorded as %+v, want one review "+
						"and one repair not pushed again", reviews, repairs)
				}
				return
			}

			// The repair went through review and a second push.
			if reviews != 1 {
				t.Fatalf("the repaired head %s was reviewed %d time(s), want once:\n%v", repaired, reviews, run.agent.Calls())
			}
			if r := report(pipeline.StageReview); r.Revision != repaired {
				t.Fatalf("the review stage last read %s, want the repaired head %s", r.Revision, repaired)
			}
			if upstream != repaired || textAt(t, result.State, pipeline.KeyPushed) != repaired {
				t.Fatalf("the pushed branch stands at %s and the run pushed %s, want the repair %s on both",
					upstream, textAt(t, result.State, pipeline.KeyPushed), repaired)
			}
			want := []pipeline.Repair{{Pushed: run.subject.head, Repaired: repaired, Republished: true}}
			if fmt.Sprint(repairs) != fmt.Sprint(want) {
				t.Fatalf("the run records repairs %+v, want %+v", repairs, want)
			}
			if tc.stillRed {
				if result.ParkedAt == nil || result.ParkedAt.To != pipeline.StageCI.FixNode() {
					t.Fatalf("the run parked on %+v, want the edge into the checks stage's fixer", result.ParkedAt)
				}
				return
			}
			if got := pipeline.StageOutcome(result.State, pipeline.StageCI); got != pipeline.OutcomePassed {
				t.Fatalf("the checks stage ended %q on the repair, want passed", got)
			}
			if r := report(pipeline.StageCI); r.HasHeld() || r.Revision != repaired {
				t.Fatalf("the checks stage's last report is %+v, want a pass read on %s", r, repaired)
			}
		})
	}
}

// A repair whose head does not carry the commit the run pushed is held at the
// checks stage with ci-fix-repair-not-on-pushed and is not sent for review.
// Here the fix round rewinds the copy to the pushed commit's parent and
// commits beside it. The control is the repair test above, whose repair is on
// top of the pushed commit and is reviewed and pushed again.
func TestARepairThatDoesNotContainThePushedCommitIsHeld(t *testing.T) {
	principles.Cite(t, principles.P6)
	t.Parallel()

	run := newChecksPipelineOpts(t, 1, func(head string) forgetest.Script {
		return forgetest.Script{
			forgetest.OpView:   {openPullRequest(head, "MERGEABLE")},
			forgetest.OpChecks: {failedChecks(head)},
		}
	}, checksPipelineOpts{repairSibling: true})
	result := run.execute(t)

	report, err := pipeline.StageReport(result.State, pipeline.StageCI)
	if err != nil {
		t.Fatalf("reading the checks stage's report: %v", err)
	}
	if result.Status != graph.StatusHalted || result.Position != pipeline.StageCI.HoldNode() {
		t.Fatalf("the run is %s at %q, want halted at the checks hold: %+v", result.Status, result.Position, report)
	}
	if !hasFinding(report, "ci-fix-repair-not-on-pushed") {
		t.Fatalf("the checks stage holds without saying the repair does not carry the pushed commit: %+v", report)
	}
	sibling := run.copyHead(t)
	if isAncestor(t, run.subject.dir, run.subject.head, sibling) {
		t.Fatalf("the sibling repair %s carries the pushed commit %s, so this run does not test a dropped push",
			sibling, run.subject.head)
	}
	for _, call := range run.agent.Calls() {
		if call.Step == 1 && strings.Contains(call.Prompt, sibling) {
			t.Fatalf("the repair was sent for review:\n%v", call)
		}
	}
	if repairs, err := pipeline.RepairsOf(result.State); err != nil || len(repairs) != 0 {
		t.Fatalf("a refused repair was recorded as %+v (%v)", repairs, err)
	}
}

// With one checks fix round allowed, a repair sent back for review that never
// reaches the pull request is not sent back again without another fix round:
// the resubmission counts toward the same limit and parks in front of the
// fixer. Here the review stage holds on the repair, the person skips review,
// approves the push hold without a push happening, and the checks fail again
// on the commit still on the pull request.
func TestAResubmittedRepairWithoutAnotherFixRoundParksOnTheFixRoundLimit(t *testing.T) {
	t.Parallel()

	run := newChecksPipelineOpts(t, 1, func(head string) forgetest.Script {
		return forgetest.Script{
			forgetest.OpView:   {openPullRequest(head, "MERGEABLE")},
			forgetest.OpChecks: {failedChecks(head)},
		}
	}, checksPipelineOpts{holdRepairReview: true})
	result := run.executeAnswering(t, map[pipeline.Stage]string{
		pipeline.StageReview: string(pipeline.OutcomeSkipped),
		pipeline.StagePush:   string(pipeline.OutcomeApproved),
	})

	if result.Status != graph.StatusRoundsExhausted || result.Position != pipeline.StageCI.FixNode() {
		t.Fatalf("the run is %s at %q, want rounds-exhausted at the checks fixer", result.Status, result.Position)
	}
	repairs, err := pipeline.RepairsOf(result.State)
	if err != nil {
		t.Fatalf("reading repairs: %v", err)
	}
	if len(repairs) != 1 {
		t.Fatalf("the run recorded repairs %+v, want exactly one", repairs)
	}
	if result.ParkedAt == nil || result.ParkedAt.To != pipeline.StageCI.FixNode() {
		t.Fatalf("the run parked on %+v, want the edge into the checks fixer", result.ParkedAt)
	}
}

// A fix round's commit the path back to review cannot take holds with
// ci-fix-not-pushed, as it did before the path existed, naming both commits
// and why: here the run skips the push stage, so it pushed nothing and has no
// branch of its own for a repair to go on. The commit reaches neither the
// upstream nor a reviewer. The control is the repair test above, whose run
// skips nothing and takes the path.
func TestARepairARunCannotReviewAgainIsHeldAndNeverPushed(t *testing.T) {
	principles.Cite(t, principles.P5)
	t.Parallel()

	run := newChecksPipelineSkipping(t, 1, []pipeline.Stage{pipeline.StagePush}, func(head string) forgetest.Script {
		return forgetest.Script{
			forgetest.OpView:   {openPullRequest(head, "MERGEABLE")},
			forgetest.OpChecks: {failedChecks(head)},
		}
	}, checksPipelineOpts{})
	result := run.execute(t)

	report, err := pipeline.StageReport(result.State, pipeline.StageCI)
	if err != nil {
		t.Fatalf("reading the checks stage's report: %v", err)
	}
	if result.Status != graph.StatusHalted || result.Position != pipeline.StageCI.HoldNode() {
		t.Fatalf("the run is %s at %q after the checks round, want halted at %q: %+v",
			result.Status, result.Position, pipeline.StageCI.HoldNode(), report)
	}
	round := run.copyHead(t)
	if round == run.subject.head {
		t.Fatalf("the checks round committed nothing, so this run has no commit to hold")
	}
	if !hasFinding(report, "ci-fix-not-pushed") {
		t.Fatalf("the checks stage holds without saying the round's commit is not pushed: %+v", report)
	}
	for _, want := range []string{round, "pushed no commit"} {
		if !strings.Contains(report.Summary, want) || !strings.Contains(report.Findings[0].Description, want) {
			t.Fatalf("the hold does not say %q: %+v", want, report)
		}
	}
	run.mustNotExistUpstream(t, "refs/heads/topic")
	for _, call := range run.agent.Calls() {
		if call.Step != 2 && strings.Contains(call.Prompt, round) {
			t.Fatalf("a review read the round's commit, and nothing in this run pushes it:\n%v", call)
		}
	}
	if repairs, err := pipeline.RepairsOf(result.State); err != nil || len(repairs) != 0 {
		t.Fatalf("a repair the run could not review again was recorded as %+v (%v)", repairs, err)
	}
}

// checksPipeline is a pipeline whose review, push, checks and fix path are
// the real ones, over a subject with an upstream to push to and a stand-in
// code host. The pull request stage is a constant that records pull request
// 7, which is all the checks stage reads of it.
type checksPipeline struct {
	*laterRun
	code *forgetest.Provider
	// afterPush runs each time the pull request stage runs, which is after
	// each push, and beforePush each time the test stage runs, which is
	// between the rebase stage's observation and the push; pass counts from
	// one. conflicting is the commit a test landed on the base.
	afterPush        func(t *testing.T, pass int)
	beforePush       func(t *testing.T, pass int)
	conflicting      string
	repairSibling    bool
	holdRepairReview bool
}

// checksPipelineOpts configures optional behaviour of a checks pipeline run.
type checksPipelineOpts struct {
	repairSibling    bool
	holdRepairReview bool
}

// newChecksPipeline builds the run with rounds automatic fix rounds at the
// checks stage. script is what the code host answers, given the head the
// push stage will forward.
func newChecksPipeline(t *testing.T, rounds int, script func(head string) forgetest.Script) *checksPipeline {
	t.Helper()
	return newChecksPipelineOpts(t, rounds, script, checksPipelineOpts{})
}

// newChecksPipelineOpts is newChecksPipeline with optional behaviour.
func newChecksPipelineOpts(t *testing.T, rounds int, script func(head string) forgetest.Script,
	opts checksPipelineOpts) *checksPipeline {
	t.Helper()
	return newChecksPipelineSkipping(t, rounds, nil, script, opts)
}

// newChecksPipelineSkipping is newChecksPipeline for a run whose skip list
// names skip.
func newChecksPipelineSkipping(t *testing.T, rounds int, skip []pipeline.Stage,
	script func(head string) forgetest.Script, opts checksPipelineOpts) *checksPipeline {
	t.Helper()
	s := newSubject(t)
	git(t, s.dir, "config", "user.name", "test")
	git(t, s.dir, "config", "user.email", "test@example.invalid")
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	git(t, filepath.Dir(upstream), "init", "--quiet", "--bare", "-b", "main", upstream)
	git(t, s.dir, "remote", "add", stages.UpstreamRemote, upstream)
	git(t, s.dir, "push", "--quiet", stages.UpstreamRemote, "main")

	code := forgetest.New(t, script(s.head))
	steps := []standin.Step{
		{Match: standin.Match{PromptContains: laterLandmark}, Times: standin.Always, Reply: standin.Reviewed(
			"objecting", findings.Finding{ID: "should-never-be-asked", Severity: findings.SeverityError,
				Action: findings.ActionAsk, Description: "a review of later commits ran"})},
		{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("nothing to report")},
		{Times: standin.Always, Reply: standin.Text(fixerAccount)},
	}
	if opts.holdRepairReview {
		repairHold := findings.Finding{
			ID:          "repair-not-reviewed-yet",
			Severity:    findings.SeverityError,
			Action:      findings.ActionAsk,
			Description: "the repair should wait for a person before review continues",
		}
		steps = []standin.Step{
			{Match: standin.Match{PromptContains: laterLandmark}, Times: standin.Always, Reply: standin.Reviewed(
				"objecting", findings.Finding{ID: "should-never-be-asked", Severity: findings.SeverityError,
					Action: findings.ActionAsk, Description: "a review of later commits ran"})},
			{Match: standin.Match{PromptContains: fixedLine}, Times: 1, Reply: standin.Reviewed("hold the repair", repairHold)},
			{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("nothing to report")},
			{Times: standin.Always, Reply: standin.Text(fixerAccount)},
		}
	}
	agent := standin.New(t, standin.Script{Steps: steps})

	cfg := config.Defaults()
	cfg.ChecksTimeout = checksTimeout
	deps := stages.NewStageDeps(agents.NewStageAgent(agent.Runner()), s.home, cfg, code.Host(), redact.New())
	fixDeps := stages.NewFixDeps(
		func(ctx context.Context, _ string) (agents.Fixer, error) {
			return agents.OpenFixer(ctx, agent.Runner(), "")
		},
		s.home, cfg, nil, redact.New(),
	)

	run := &checksPipeline{code: code, repairSibling: opts.repairSibling, holdRepairReview: opts.holdRepairReview}
	all := pipeline.ConstantStages("nothing to report")
	all.Rebase = stages.Rebase(deps)
	all.Review = stages.Review(deps)
	all.Test = calling("the targeted check passed", func(pass int) {
		if run.beforePush != nil {
			run.beforePush(t, pass)
		}
	})
	all.Push = stages.Push(deps)
	all.PR = recordingPullRequest(func(pass int) {
		if run.afterPush != nil {
			run.afterPush(t, pass)
		}
	})
	all.CI = stages.CI(deps)

	real := stages.Fix(fixDeps)
	fixer := real
	fixer.NewBody = func() pipeline.FixBody {
		body := real.NewBody()
		return func(ctx context.Context, in pipeline.FixInput) (pipeline.FixOutput, error) {
			if run.repairSibling {
				parent := git(t, s.dir, "rev-parse", s.head+"^")
				git(t, s.dir, "reset", "--hard", parent)
				write(t, s.dir, totalPath, strings.Replace(baseTotal, "sum := 0", "sum := 0 "+fixedLine, 1))
				git(t, s.dir, "add", totalPath)
				git(t, s.dir, "commit", "--quiet", "-m", "repair beside the pushed commit")
				sibling := git(t, s.dir, "rev-parse", "HEAD")
				out, err := body(ctx, in)
				if err != nil {
					return out, err
				}
				if out.Writes == nil {
					out.Writes = map[pipeline.Key]graph.Value{}
				}
				out.Writes[pipeline.KeyHead] = graph.TextValue(sibling)
				return out, nil
			}
			write(t, s.dir, totalPath, strings.Replace(baseTotal, "sum := 0", "sum := 0 "+fixedLine, 1))
			return body(ctx, in)
		}
	}

	p, err := pipeline.New(pipeline.Options{
		Stages: all,
		Rounds: config.FixRounds{Checks: rounds},
		Budget: config.DefaultRunBudget,
		Fixer:  fixer,
	})
	if err != nil {
		t.Fatalf("building the pipeline: %v", err)
	}
	exec, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("building an executor: %v", err)
	}
	start := s.start()
	start.ForgeRepository = "dayamjz/assistant"
	start.Skip = skip
	state, err := p.NewState(start)
	if err != nil {
		t.Fatalf("building the run's initial state: %v", err)
	}
	run.laterRun = &laterRun{subject: s, upstream: upstream, agent: agent, exec: exec, state: state}
	return run
}

// landOnBase puts a commit on the upstream's base branch from a checkout of
// its own and returns it. A clean one adds a file of its own, which the
// change's commit replays onto without a conflict; any other touches the line
// the change under validation touched.
func (r *checksPipeline) landOnBase(t *testing.T, clean bool) string {
	t.Helper()
	other := t.TempDir()
	git(t, other, "clone", "--quiet", r.upstream, other)
	git(t, other, "config", "user.name", "elsewhere")
	git(t, other, "config", "user.email", "elsewhere@example.invalid")
	if clean {
		write(t, other, "elsewhere.go", "package total\n\n// landed first\n")
	} else {
		write(t, other, totalPath, strings.Replace(baseTotal, "sum := 0", "sum := 0 // landed first", 1))
	}
	git(t, other, "add", "-A")
	git(t, other, "commit", "--quiet", "-m", "another change lands first")
	git(t, other, "push", "--quiet", "origin", "main")
	return git(t, other, "rev-parse", "HEAD")
}

// landOnBranch puts a commit on the upstream's copy of the branch under
// validation from a checkout of its own, which is somebody else pushing to
// the pull request's branch, and returns it.
func (r *checksPipeline) landOnBranch(t *testing.T) string {
	t.Helper()
	other := t.TempDir()
	git(t, other, "clone", "--quiet", "--branch", "topic", r.upstream, other)
	git(t, other, "config", "user.name", "elsewhere")
	git(t, other, "config", "user.email", "elsewhere@example.invalid")
	write(t, other, "notes.txt", "pushed to the branch by somebody else\n")
	git(t, other, "add", "-A")
	git(t, other, "commit", "--quiet", "-m", "somebody else pushes to the branch")
	git(t, other, "push", "--quiet", "origin", "topic")
	return git(t, other, "rev-parse", "HEAD")
}

// executeAnswering runs the pipeline, answering each hold with the outcome the
// map names for that stage, until the run completes, parks, or no answer is
// given for the halt point it reached.
func (r *checksPipeline) executeAnswering(t *testing.T, answers map[pipeline.Stage]string) graph.Result {
	t.Helper()
	result := r.execute(t)
	for steps := 0; result.Status == graph.StatusHalted; steps++ {
		if steps > 8 {
			t.Fatalf("the run is still halted at %q after %d answers", result.Position, steps)
		}
		stage, ok := pipeline.StageOfNode(result.Position)
		if !ok || !strings.HasSuffix(result.Position, ".hold") {
			return result
		}
		answer, ok := answers[stage]
		if !ok {
			t.Fatalf("the run halted at %q (%s) with no answer", result.Position, stage)
		}
		var err error
		result, err = r.exec.Answer(t.Context(), r.subject.run, answer)
		if err != nil {
			t.Fatalf("answering %s at %q: %v", answer, result.Position, err)
		}
	}
	return result
}

// calling is a stage that passes with summary and calls then with how many
// times it has run, which is where a test changes the world partway through
// a pass.
func calling(summary string, then func(pass int)) pipeline.Implementation {
	var mu sync.Mutex
	passes := 0
	return pipeline.Implementation{
		NewBody: func() pipeline.Body {
			return func(context.Context, pipeline.Input) (pipeline.Output, error) {
				mu.Lock()
				passes++
				pass := passes
				mu.Unlock()
				then(pass)
				return pipeline.Output{Report: findings.Report{Summary: summary}}, nil
			}
		},
	}
}

// textAt reads a text key off a run's state.
func textAt(t *testing.T, state graph.State, key pipeline.Key) string {
	t.Helper()
	v, ok := state.Get(string(key))
	if !ok {
		t.Fatalf("the state declares no %s", key)
	}
	text, _ := v.Text()
	return text
}

// recordingPullRequest stands in for the pull request stage: it records pull
// request 7 and calls then with how many times it has run, which is where a
// test changes the world after a push.
func recordingPullRequest(then func(pass int)) pipeline.Implementation {
	var mu sync.Mutex
	passes := 0
	return pipeline.Implementation{
		Writes: []pipeline.Key{pipeline.KeyPullRequest},
		NewBody: func() pipeline.Body {
			return func(context.Context, pipeline.Input) (pipeline.Output, error) {
				mu.Lock()
				passes++
				pass := passes
				mu.Unlock()
				then(pass)
				return pipeline.Output{
					Report: findings.Report{Summary: "recorded pull request 7"},
					Writes: map[pipeline.Key]graph.Value{pipeline.KeyPullRequest: graph.TextValue("7")},
				}, nil
			}
		},
	}
}
