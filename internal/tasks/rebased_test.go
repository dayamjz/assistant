package tasks

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// rebasedLanding is a task whose pushed commit the gate rebased before it
// published it: the commit the worker made in the copy, the main tip the
// gate replayed it onto, and the commit the gate published for it.
type rebasedLanding struct {
	tk        task
	worker    string
	onto      string
	published string
}

// rebaseInGate cuts a delivery task on branch, has its worker commit an edit
// of line two, moves the upstream's main on with an edit of line twelve, and
// then does what the gate's rebase stage does with the worker's push: in a
// repository of its own, replays the worker's commit onto main's new tip and
// publishes the result to the upstream's branch. Nothing is recorded: the
// caller decides what the run's record says.
func (s *cleanupSubject) rebaseInGate(t *testing.T, branch string) rebasedLanding {
	t.Helper()
	tk := s.deliveryTask(t, branch)
	worker := commit(t, tk.copy, "f.txt", strings.Replace(twelveLines, "one\nsame\n", "one\nchanged\n", 1), "edit line two")
	onto := s.advanceMain(t, strings.Replace(twelveLines, "twelve\n", "TWELVE\n", 1), "edit line twelve")

	gateCopy := filepath.Join(t.TempDir(), "gate")
	rawGit(t, filepath.Dir(gateCopy), "clone", "-q", s.upstream, gateCopy)
	rawGit(t, gateCopy, "fetch", "-q", tk.copy, branch)
	rawGit(t, gateCopy, "switch", "-q", "--detach", worker)
	rawGit(t, gateCopy, "rebase", "-q", onto)
	published := rawGit(t, gateCopy, "rev-parse", "HEAD")
	rawGit(t, gateCopy, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	if published == worker {
		t.Fatalf("the replay published %s, the worker's own commit", worker)
	}
	return rebasedLanding{tk: tk, worker: worker, onto: onto, published: published}
}

// advanceMain commits content as f.txt on the upstream's main from a clone
// of its own and returns main's new tip.
func (s *cleanupSubject) advanceMain(t *testing.T, content, message string) string {
	t.Helper()
	work := filepath.Join(t.TempDir(), "advance")
	rawGit(t, filepath.Dir(work), "clone", "-q", s.upstream, work)
	return commitAndPush(t, work, content, message)
}

// commitAndPush commits content as f.txt in work and pushes it to the
// upstream's main.
func commitAndPush(t *testing.T, work, content, message string) string {
	t.Helper()
	tip := commit(t, work, "f.txt", content, message)
	rawGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")
	return tip
}

// rebasedRun records a run of branch attributed to the task, submitted the
// worker's commit, whose pull request's read ended on a merge at the
// published head, as a merge on the code host leaves it.
func (s *cleanupSubject) rebasedRun(t *testing.T, id, branch string, number int, submitted, published string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.store.CreateRun(ctx, store.Run{
		ID: id, RepositoryID: cleanupRepository, Branch: branch, SubmittedHead: submitted, Base: "main",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := s.store.TransitionRun(ctx, id, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}
	if _, err := s.store.TransitionRunOnPullRequest(ctx, id, []store.RunStatus{store.RunRunning}, store.RunPassed,
		store.PullRequestRead{PullRequest: number, Head: published, End: store.PullRequestMerged}, store.HoldClosure{}); err != nil {
		t.Fatalf("TransitionRunOnPullRequest: %v", err)
	}
}

// recordRebase writes the run rebase record the gate's rebase stage writes
// for the replay: the run's submitted commit replayed onto onto as after.
func (s *cleanupSubject) recordRebase(t *testing.T, run, submitted, onto, after string) {
	t.Helper()
	if _, err := s.store.RecordRunRebase(context.Background(), store.RunRebase{
		Run: run, Submitted: submitted, Before: submitted, Onto: onto, After: after,
	}); err != nil {
		t.Fatalf("RecordRunRebase: %v", err)
	}
}

// mergedAt is the code host reading pull request number merged into main at
// head.
func mergedAt(number int, head string) forgetest.Script {
	return forgetest.Script{forgetest.OpView: {forgetest.PullRequest(forgetest.PullRequestView{
		Number: number, State: "MERGED", Head: "topic", HeadCommit: head, Base: "main",
	})}}
}

// PRD principles P12 and P6, over a landing the gate rebased. The worker's
// commit in the copy was replayed by the gate onto a newer main and
// published under another identifier, the pull request merged at that
// identifier, and main then moved on with an edit of the worker's own line,
// so integrating the worker's commit would change main and the content check
// cannot hold either. The gate's run rebase record is what lets the copy go:
// with the upstream's branch standing, through the remote-branch proof; once
// a host deleted it after the merge, through the merged-pull-request proof.
// The proof's examined commits name the worker's commit, the run's submitted
// commit and the published one, so the record says what the removal rested
// on.
//
// Each counterfeit is the same landing with one fact changed, and each keeps
// the copy, because none is a commit the program saw become one the upstream
// holds: no record at all, though the published commit carries the worker's
// change exactly (nothing here matches commits by what they hold); a worker
// commit made after the push, which no rebase carried; a record whose
// published head is not the one the upstream holds, which is what a record
// of a replay that was later rewritten again by hand leaves; and a record of
// a run the snapshot does not attribute to the task.
func TestATaskCopyWhoseLandingTheGateRebasedIsRemovedOnlyOnTheRecord(t *testing.T) {
	principles.Cite(t, principles.P12)
	principles.Cite(t, principles.P6)

	t.Run("the branch standing passes through the remote-branch proof", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		l := s.rebaseInGate(t, "topic")
		s.rebasedRun(t, "run-topic", "topic", 7, l.worker, l.published)
		s.recordRebase(t, "run-topic", l.worker, l.onto, l.published)
		s.advanceMain(t, strings.Replace(twelveLines, "one\nsame\n", "one\nchanged again\n", 1), "rewrite line two")

		got, err := s.cleanUp(t, l.tk.id)
		record := s.removedOn(t, l.tk, got, err, store.CleanupProofRemoteBranch)
		for _, c := range []string{l.worker, l.published} {
			if !slices.Contains(record.Examined, c) {
				t.Fatalf("the proof examined %v, which does not name %s", record.Examined, c)
			}
		}
	})

	t.Run("the branch deleted passes through the merged-pull-request proof", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		l := s.rebaseInGate(t, "topic")
		s.forge.Rescript(t, mergedAt(7, l.published))
		s.rebasedRun(t, "run-topic", "topic", 7, l.worker, l.published)
		s.recordRebase(t, "run-topic", l.worker, l.onto, l.published)
		s.mergeIntoMain(t, "topic", false)
		s.deleteUpstreamBranch(t, "topic")
		s.advanceMain(t, strings.Replace(twelveLines, "one\nsame\n", "one\nchanged again\n", 1), "rewrite line two")

		got, err := s.cleanUp(t, l.tk.id)
		record := s.removedOn(t, l.tk, got, err, store.CleanupProofMergedPullRequest)
		for _, c := range []string{l.worker, l.published} {
			if !slices.Contains(record.Examined, c) {
				t.Fatalf("the proof examined %v, which does not name %s", record.Examined, c)
			}
		}
	})

	t.Run("no record keeps the copy, whatever the published commit holds", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		l := s.rebaseInGate(t, "topic")
		s.forge.Rescript(t, mergedAt(7, l.published))
		s.rebasedRun(t, "run-topic", "topic", 7, l.worker, l.published)
		s.advanceMain(t, strings.Replace(twelveLines, "one\nsame\n", "one\nchanged again\n", 1), "rewrite line two")

		_, err := s.cleanUp(t, l.tk.id)
		refusal := refusedWith(t, err, ReasonUnproven)
		stillStands(t, l.tk)
		for _, proof := range []store.CleanupProof{store.CleanupProofRemoteBranch, store.CleanupProofMergedPullRequest} {
			if detail := attemptFor(t, refusal, proof); !strings.Contains(detail, l.worker) ||
				!strings.Contains(detail, "since none of the task's runs recorded one") {
				t.Errorf("the %s attempt says %q, want it to name %s and say no rebase was recorded", proof, detail, l.worker)
			}
		}
	})

	t.Run("a commit the worker made after the push keeps the copy", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		l := s.rebaseInGate(t, "topic")
		s.forge.Rescript(t, mergedAt(7, l.published))
		s.rebasedRun(t, "run-topic", "topic", 7, l.worker, l.published)
		s.recordRebase(t, "run-topic", l.worker, l.onto, l.published)
		later := commit(t, l.tk.copy, "f.txt", strings.Replace(strings.Replace(twelveLines, "one\nsame\n", "one\nchanged\n", 1),
			"seven\n", "SEVEN\n", 1), "edit line seven after the push")

		_, err := s.cleanUp(t, l.tk.id)
		refusal := refusedWith(t, err, ReasonUnproven)
		stillStands(t, l.tk)
		if detail := attemptFor(t, refusal, store.CleanupProofRemoteBranch); !strings.Contains(detail, "commit "+later) ||
			!strings.Contains(detail, "the one rebase the task's runs recorded") {
			t.Errorf("the remote-branch attempt says %q, want it to name %s and the one record", detail, later)
		}
	})

	t.Run("a record whose published head the upstream does not hold keeps the copy", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		l := s.rebaseInGate(t, "topic")
		// The branch is moved to a commit with the worker's message that
		// drops the worker's hunk, which is what a later hand rewrite of the
		// replay leaves; the record still names the replay.
		dropped := filepath.Join(t.TempDir(), "dropped")
		rawGit(t, filepath.Dir(dropped), "clone", "-q", s.upstream, dropped)
		rawGit(t, dropped, "switch", "-q", "--detach", l.onto)
		rawGit(t, dropped, "commit", "-q", "--allow-empty", "-m", "edit line two")
		lookalike := rawGit(t, dropped, "rev-parse", "HEAD")
		rawGit(t, dropped, "push", "-q", "--force", "origin", "HEAD:refs/heads/topic")
		s.forge.Rescript(t, mergedAt(7, lookalike))
		s.rebasedRun(t, "run-topic", "topic", 7, l.worker, lookalike)
		s.recordRebase(t, "run-topic", l.worker, l.onto, l.published)

		_, err := s.cleanUp(t, l.tk.id)
		refusedWith(t, err, ReasonUnproven)
		stillStands(t, l.tk)
	})

	t.Run("a record of a run not attributed to the task keeps the copy", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		l := s.rebaseInGate(t, "topic")
		s.forge.Rescript(t, mergedAt(7, l.published))
		s.rebasedRun(t, "run-topic", "topic", 7, l.worker, l.published)
		// The record is written by a run of another branch that happened to
		// be given the same commit; the snapshot does not attribute it.
		s.rebasedRun(t, "run-other", "elsewhere", 8, l.worker, l.published)
		s.recordRebase(t, "run-other", l.worker, l.onto, l.published)
		s.advanceMain(t, strings.Replace(twelveLines, "one\nsame\n", "one\nchanged again\n", 1), "rewrite line two")

		_, err := s.cleanUp(t, l.tk.id)
		refusedWith(t, err, ReasonUnproven)
		stillStands(t, l.tk)
	})
}
