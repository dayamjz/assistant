package stages_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/safety"
)

// TestThePushStageRefusesToReplaceABranchHoldingCommitsTheRunLacks is issue
// #396 at the stage, over a real upstream: the branch already holds a commit
// somebody else pushed when the run observes it, and the run's commit does
// not contain that commit. The anchor is the rebase stage's own observation,
// so it names the colleague's commit and the lease on it would hold; the
// stage refuses anyway, and the branch keeps the commit.
//
// Two shapes: the run's commit diverged from the colleague's, both on the
// base, and the run's commit standing behind it, the colleague having pushed
// on top of a branch at the run's commit. The refusal is asserted on the
// anchor value it reports, which is the observed colleague commit, and on
// naming exactly that commit as discarded.
func TestThePushStageRefusesToReplaceABranchHoldingCommitsTheRunLacks(t *testing.T) {
	principles.Cite(t, principles.P6)
	for _, tc := range []struct {
		name   string
		behind bool
	}{
		{"diverged", false},
		{"behind", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newPushRun(t)
			var colleague string
			if tc.behind {
				git(t, run.source, "push", "--quiet", "origin", run.head+":refs/heads/topic")
				colleague = run.commitOnTopOfUpstream(t, "topic", "colleague.txt", "pushed on top of the run's commit\n")
			} else {
				colleague = run.commitOnUpstreamBranch(t, "topic", "colleague.txt", "pushed beside the run's commit\n")
			}
			run.anchor = run.anchorFromRebaseStage(t)
			if run.anchor == "" {
				t.Fatal("the rebase stage recorded no anchor")
			}

			refused := refusedDescription(t, run.mustPush(t))
			if got := lineAfter(refused, "reason: "); got != string(safety.ReasonWouldDiscard) {
				t.Fatalf("the refusal's reason is %q, want %q:\n%s", got, safety.ReasonWouldDiscard, refused)
			}
			if got := lineAfter(refused, "discarded: "); got != colleague {
				t.Fatalf("the refusal names %q as discarded, want exactly the colleague's %s:\n%s",
					got, colleague, refused)
			}
			if _, anchored, _ := strings.Cut(lineAfter(refused, "anchor: "), "="); anchored != colleague {
				t.Fatalf("the refusal's anchor names %q, want the commit the rebase stage observed, %s:\n%s",
					anchored, colleague, refused)
			}
			if got := run.upstreamCommit(t, "refs/heads/topic"); got != colleague {
				t.Fatalf("the branch stands at %s, and the colleague's commit %s is no longer its tip", got, colleague)
			}
		})
	}
}

// TestThePushStageRewritesOnlyWhatTheRunWasGiven is the positive control for
// the refusal above: the branch holds the run's submitted commit, the base
// moves, and the rebase stage replays the run's commit onto it, so the push
// is not a fast-forward and drops the submitted commit. That commit is in
// what the run worked from, so the push goes through as an anchored force,
// leased on the commit the rebase stage observed, and reports what it
// dropped.
func TestThePushStageRewritesOnlyWhatTheRunWasGiven(t *testing.T) {
	principles.Cite(t, principles.P6)
	run := newPushRun(t)
	git(t, run.source, "push", "--quiet", "origin", run.head+":refs/heads/topic")
	submitted := run.head
	run.commitOnTopOfUpstream(t, "main", "base-moved.txt", "the base moved\n")

	run.anchor = run.anchorFromRebaseStage(t)
	if run.head == submitted {
		t.Fatal("the rebase stage did not rewrite the run's commit, so this control exercises no anchored force")
	}
	run.reviewedCommit = run.head

	report := run.mustPush(t)
	if report.HasHeld() {
		t.Fatalf("a push dropping only the run's own submitted commit held: %+v", report)
	}
	if !hasFinding(report, "push-rewrote-branch") {
		t.Fatalf("the push did not report the commit it dropped: %+v", report)
	}
	description := report.Findings[0].Description
	if !strings.Contains(description, "which named "+submitted+" when this run observed it") ||
		!strings.Contains(description, "branch held: "+submitted+".") {
		t.Fatalf("the report does not name the lease on the observed %s and the commit dropped:\n%s",
			submitted, description)
	}
	if got := run.upstreamCommit(t, "refs/heads/topic"); got != run.head {
		t.Fatalf("the branch stands at %s, want the rebased %s", got, run.head)
	}
}

// commitOnTopOfUpstream puts a commit on top of a branch the upstream already
// holds, from a checkout of its own, and returns it.
func (r *pushRun) commitOnTopOfUpstream(t *testing.T, branch, file, content string) string {
	t.Helper()
	other := t.TempDir()
	git(t, other, "clone", "--quiet", r.upstream, other)
	git(t, other, "checkout", "--quiet", branch)
	write(t, other, file, content)
	git(t, other, "add", ".")
	git(t, other, "commit", "--quiet", "-m", "on top of "+branch)
	git(t, other, "push", "--quiet", "origin", branch)
	return git(t, other, "rev-parse", "HEAD")
}
