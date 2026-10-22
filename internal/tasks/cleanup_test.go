package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

// PRD principle P12: removing an isolated copy requires positive proof that
// its work landed, anything inconclusive refuses, and a refusal means stop
// and investigate rather than a flag. PRD section 13 turns it into three
// cases, and this test drives each over a real clone of a real upstream,
// with the code host answered over the wire by the stand-in the real
// adapter reads: committed, unpushed work refuses; a squash merge followed
// by the branch's deletion passes through the content check; and a provider
// lookup failure that leaves the question open refuses. The delivery copy
// is the one PRD section 5 protects, so the three are driven over one.
//
// What the citation claims is the decision over a task's copy at package
// reach: internal/service's tests claim the same decision over a run's copy
// from its own site, and no journey walk reaches either, which
// internal/journey's coverage says.
func TestCleanupRefusesUnlandedWorkAndPassesASquashThroughTheContentCheck(t *testing.T) {
	principles.Cite(t, principles.P12)

	t.Run("committed unpushed work refuses", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk := s.deliveryTask(t, "topic")
		work := commit(t, tk.copy, "f.txt", strings.Replace(twelveLines, "one\nsame\n", "one\nchanged\n", 1), "edit line two")

		got, err := s.cleanUp(t, tk.id)
		refusal := refusedWith(t, err, ReasonUnproven)
		if got.Result != "" || got.Proof != "" {
			t.Fatalf("a refusal came with a cleanup inside it: %+v", got)
		}
		stillStands(t, tk)
		if detail := attemptFor(t, refusal, store.CleanupProofRemoteBranch); !strings.Contains(detail, "advertises no branch topic") {
			t.Errorf("the remote-branch attempt says %q", detail)
		}
		if detail := attemptFor(t, refusal, store.CleanupProofMergedPullRequest); !strings.Contains(detail, "no merge record") {
			t.Errorf("the merged-pull-request attempt says %q", detail)
		}
		if detail := attemptFor(t, refusal, store.CleanupProofDefaultBranch); !strings.Contains(detail, "would change it") {
			t.Errorf("the default-branch attempt says %q", detail)
		}
		// The work set is every commit a local reference reaches: the clone's
		// main, which the copy was cut with, and the branch's tip.
		base := rawGit(t, tk.copy, "rev-parse", "refs/heads/main")
		if !reflect.DeepEqual(refusal.Examined, []string{base, work}) {
			t.Errorf("the refusal examined %v, want the clone's main %s and the unpushed commit %s", refusal.Examined, base, work)
		}
		if _, err := s.store.TaskCleanup(context.Background(), tk.id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a refusal wrote a cleanup record: %v", err)
		}
	})

	t.Run("a squash merge and the branch's deletion pass through the content check", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk := s.deliveryTask(t, "topic")
		first := commit(t, tk.copy, "f.txt", strings.Replace(twelveLines, "one\nsame\n", "one\nchanged\n", 1), "edit line two")
		second := commit(t, tk.copy, "f.txt", strings.Replace(
			strings.Replace(twelveLines, "one\nsame\n", "one\nchanged\n", 1), "seven\n", "SEVEN\n", 1), "edit line seven")
		s.push(t, tk.copy, "topic")
		squashed := s.mergeIntoMain(t, "topic", true)
		s.deleteUpstreamBranch(t, "topic")

		base := rawGit(t, tk.copy, "rev-parse", "refs/heads/main")
		got, err := s.cleanUp(t, tk.id)
		record := s.removedOn(t, tk, got, err, store.CleanupProofDefaultBranch)
		// The proof names what it examined: the commits the copy's
		// references reach, main's base and the branch's tip, and the tip
		// they were checked against, which is the squash. The first commit
		// is reached only through the second and so is not named itself.
		_ = first
		if !reflect.DeepEqual(record.Examined, []string{base, second, squashed}) {
			t.Fatalf("the proof examined %v, want main's %s, the branch's tip %s and the squash %s", record.Examined, base, second, squashed)
		}
	})

	t.Run("a provider lookup failure leaves the question open and refuses", func(t *testing.T) {
		s := newCleanupSubject(t, forgetest.Script{forgetest.OpView: {forgetest.Exit(1, "GraphQL: Could not resolve to a PullRequest")}})
		tk := s.deliveryTask(t, "topic")
		work := commit(t, tk.copy, "f.txt", strings.Replace(twelveLines, "one\nsame\n", "one\nchanged\n", 1), "edit line two")
		s.push(t, tk.copy, "topic")
		s.landedRun(t, "topic", 7, work)
		// The branch is gone from the upstream and its content is not on
		// main, so the pull request is the one proof left, and the provider
		// cannot describe it.
		s.deleteUpstreamBranch(t, "topic")

		got, err := s.cleanUp(t, tk.id)
		refusal := refusedWith(t, err, ReasonUnproven)
		if got.Result != "" || got.Proof != "" {
			t.Fatalf("a refusal came with a cleanup inside it: %+v", got)
		}
		stillStands(t, tk)
		if detail := attemptFor(t, refusal, store.CleanupProofMergedPullRequest); !strings.Contains(detail, "pull request 7 could not be read") ||
			!strings.Contains(detail, "Could not resolve") {
			t.Errorf("the merged-pull-request attempt says %q, want the provider's refusal", detail)
		}
		if calls := s.forge.Calls(); len(calls) != 1 || calls[0].Op != forgetest.OpView {
			t.Fatalf("the code host was asked %+v, want one pull request read", calls)
		}
	})
}

// A multi-commit branch squashed into main passes; the same branch with one
// more commit the squash does not carry refuses, naming the commit.
func TestASquashPassesAndAnExtraUnmergedCommitRefuses(t *testing.T) {
	s := newCleanupSubject(t, nil)
	tk := s.deliveryTask(t, "topic")
	commit(t, tk.copy, "f.txt", strings.Replace(twelveLines, "one\nsame\n", "one\nchanged\n", 1), "edit line two")
	commit(t, tk.copy, "f.txt", strings.Replace(
		strings.Replace(twelveLines, "one\nsame\n", "one\nchanged\n", 1), "seven\n", "SEVEN\n", 1), "edit line seven")
	s.push(t, tk.copy, "topic")
	s.mergeIntoMain(t, "topic", true)
	s.deleteUpstreamBranch(t, "topic")
	extra := commit(t, tk.copy, "extra.txt", "not in the squash\n", "extra")

	_, err := s.cleanUp(t, tk.id)
	refusal := refusedWith(t, err, ReasonUnproven)
	stillStands(t, tk)
	if detail := attemptFor(t, refusal, store.CleanupProofDefaultBranch); !strings.Contains(detail, "would change it") ||
		!strings.Contains(detail, extra) {
		t.Fatalf("the default-branch attempt says %q, want the extra commit %s named as changing the tip", detail, extra)
	}
}

// The remote-branch proof is read fresh. A remote-tracking reference the
// copy still carries from a push proves only that something was once
// pushed: with the upstream's branch gone it refuses, and with the branch
// there it passes on the tip the upstream advertises now.
func TestTheRemoteBranchProofReadsTheUpstreamAndNotATrackingReference(t *testing.T) {
	s := newCleanupSubject(t, nil)
	tk := s.deliveryTask(t, "topic")
	work := commit(t, tk.copy, "g.txt", "new file\n", "add g")
	s.push(t, tk.copy, "topic")
	// The push left a remote-tracking reference naming the work.
	if got := rawGit(t, tk.copy, "rev-parse", "refs/remotes/assistant/topic"); got != work {
		t.Fatalf("the copy's tracking reference is %s, want %s", got, work)
	}
	s.deleteUpstreamBranch(t, "topic")

	_, err := s.cleanUp(t, tk.id)
	refusal := refusedWith(t, err, ReasonUnproven)
	stillStands(t, tk)
	if detail := attemptFor(t, refusal, store.CleanupProofRemoteBranch); !strings.Contains(detail, "advertises no branch topic") {
		t.Fatalf("the remote-branch attempt says %q, want the upstream's branch missing", detail)
	}

	s.push(t, tk.copy, "topic")
	base := rawGit(t, tk.copy, "rev-parse", "refs/heads/main")
	got, err := s.cleanUp(t, tk.id)
	record := s.removedOn(t, tk, got, err, store.CleanupProofRemoteBranch)
	if !reflect.DeepEqual(record.Examined, []string{base, work, work}) {
		t.Fatalf("the proof examined %v, want main's %s, the work and the upstream's tip, which is the work", record.Examined, base)
	}
}

// The merged-pull-request proof is read fresh too, and it holds the pull
// request to the merge record and the record to the copy: it passes when
// the pull request reads merged into the default branch at the head the
// merge was recorded at and that head, fetched by identifier, contains the
// work; it refuses when the base is not the default branch, when the head
// moved after the merge, and when the record is there but the head it names
// lacks this copy's commits. Every answer is the stand-in's JSON read by the
// real adapter, and the branch is gone from the upstream with its content
// unmerged, so the pull request is the one proof that could hold.
func TestTheMergedPullRequestProofHoldsTheHeadAndTheBase(t *testing.T) {
	merged := func(base, headCommit string) forgetest.PullRequestView {
		return forgetest.PullRequestView{Number: 7, State: "MERGED", Mergeable: "UNKNOWN", Base: base, Head: "topic",
			HeadCommit: headCommit, Title: "topic", URL: "https://github.com/owner/name/pull/7"}
	}
	// arrange builds a copy whose branch reached the upstream and was then
	// deleted there unmerged, with a merge record naming head, and returns
	// the copy's one work commit.
	arrange := func(t *testing.T, s *cleanupSubject, recorded func(work string) string) (task, string) {
		t.Helper()
		tk := s.deliveryTask(t, "topic")
		work := commit(t, tk.copy, "g.txt", "the work\n", "the work")
		s.push(t, tk.copy, "topic")
		s.deleteUpstreamBranch(t, "topic")
		s.landedRun(t, "topic", 7, recorded(work))
		return tk, work
	}
	same := func(work string) string { return work }

	t.Run("passes on a head containing the work, fetched by identifier", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk, work := arrange(t, s, same)
		// The merged head is pushed under another name so the upstream
		// still holds the object, as a code host does after a merge of an
		// unsquashed branch; the branch itself is gone.
		rawGit(t, s.upstream, "update-ref", "refs/heads/kept", work)
		s.forge.Rescript(t, forgetest.Script{forgetest.OpView: {forgetest.PullRequest(merged("main", work))}})
		base := rawGit(t, tk.copy, "rev-parse", "refs/heads/main")
		got, err := s.cleanUp(t, tk.id)
		record := s.removedOn(t, tk, got, err, store.CleanupProofMergedPullRequest)
		if !reflect.DeepEqual(record.Examined, []string{base, work, work}) {
			t.Fatalf("the proof examined %v, want main's %s, the work and the merged head", record.Examined, base)
		}
	})
	t.Run("base not the default branch refuses", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk, work := arrange(t, s, same)
		rawGit(t, s.upstream, "update-ref", "refs/heads/kept", work)
		s.forge.Rescript(t, forgetest.Script{forgetest.OpView: {forgetest.PullRequest(merged("release", work))}})
		_, err := s.cleanUp(t, tk.id)
		refusal := refusedWith(t, err, ReasonUnproven)
		stillStands(t, tk)
		if detail := attemptFor(t, refusal, store.CleanupProofMergedPullRequest); !strings.Contains(detail, "merged into release, which is not the default branch main") {
			t.Fatalf("the attempt says %q", detail)
		}
	})
	t.Run("a head moved after the merge refuses", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk, work := arrange(t, s, same)
		rawGit(t, s.upstream, "update-ref", "refs/heads/kept", work)
		s.forge.Rescript(t, forgetest.Script{forgetest.OpView: {forgetest.PullRequest(merged("main", strings.Repeat("1", 40)))}})
		_, err := s.cleanUp(t, tk.id)
		refusal := refusedWith(t, err, ReasonUnproven)
		stillStands(t, tk)
		if detail := attemptFor(t, refusal, store.CleanupProofMergedPullRequest); !strings.Contains(detail, "the head moved after the merge") {
			t.Fatalf("the attempt says %q", detail)
		}
	})
	t.Run("a head that does not contain this copy's commits refuses", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		// The merge record names the commit the branch started on, which the
		// upstream holds as main, and the copy's work came after it.
		base := rawGit(t, s.upstream, "rev-parse", "refs/heads/main")
		tk, _ := arrange(t, s, func(string) string { return base })
		s.forge.Rescript(t, forgetest.Script{forgetest.OpView: {forgetest.PullRequest(merged("main", base))}})
		_, err := s.cleanUp(t, tk.id)
		refusal := refusedWith(t, err, ReasonUnproven)
		stillStands(t, tk)
		if detail := attemptFor(t, refusal, store.CleanupProofMergedPullRequest); !strings.Contains(detail, "is not in the merged head "+base) {
			t.Fatalf("the attempt says %q", detail)
		}
	})
	t.Run("a pull request that reads open refuses", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk, work := arrange(t, s, same)
		view := merged("main", work)
		view.State = "OPEN"
		s.forge.Rescript(t, forgetest.Script{forgetest.OpView: {forgetest.PullRequest(view)}})
		_, err := s.cleanUp(t, tk.id)
		refusal := refusedWith(t, err, ReasonUnproven)
		stillStands(t, tk)
		if detail := attemptFor(t, refusal, store.CleanupProofMergedPullRequest); !strings.Contains(detail, "reads open, not merged") {
			t.Fatalf("the attempt says %q", detail)
		}
	})
}

// The clone's inventory is the work set, and anything in it a proof does
// not cover refuses: a commit on a local branch HEAD is not on, a stash, an
// untracked file, a modified file, a detached HEAD on a commit no reference
// reaches, and a copy that changes between the proof and the removal. A
// terminal that was not observed refuses before the copy is read at all.
func TestTheInventoryIsTheWorkSetAndTheRecheckIsItUnchanged(t *testing.T) {
	landed := func(t *testing.T) (*cleanupSubject, task) {
		t.Helper()
		s := newCleanupSubject(t, nil)
		tk := s.deliveryTask(t, "topic")
		commit(t, tk.copy, "g.txt", "on topic\n", "on topic")
		s.push(t, tk.copy, "topic")
		return s, tk
	}
	t.Run("a commit on a local branch not on HEAD", func(t *testing.T) {
		s, tk := landed(t)
		rawGit(t, tk.copy, "switch", "-q", "-c", "aside")
		aside := commit(t, tk.copy, "aside.txt", "on aside\n", "on aside")
		rawGit(t, tk.copy, "switch", "-q", "topic")
		_, err := s.cleanUp(t, tk.id)
		refusal := refusedWith(t, err, ReasonUnproven)
		stillStands(t, tk)
		if detail := attemptFor(t, refusal, store.CleanupProofRemoteBranch); !strings.Contains(detail, aside) {
			t.Fatalf("the remote-branch attempt says %q, want the commit on the other branch named", detail)
		}
	})
	t.Run("a stash", func(t *testing.T) {
		s, tk := landed(t)
		if err := os.WriteFile(filepath.Join(tk.copy, "g.txt"), []byte("stashed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rawGit(t, tk.copy, "stash", "-q")
		_, err := s.cleanUp(t, tk.id)
		refusedWith(t, err, ReasonCopyStash)
		stillStands(t, tk)
	})
	t.Run("an untracked file", func(t *testing.T) {
		s, tk := landed(t)
		if err := os.WriteFile(filepath.Join(tk.copy, "notes.txt"), []byte("not committed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := s.cleanUp(t, tk.id)
		refusedWith(t, err, ReasonCopyUntracked)
		stillStands(t, tk)
	})
	t.Run("a modified tracked file", func(t *testing.T) {
		s, tk := landed(t)
		if err := os.WriteFile(filepath.Join(tk.copy, "g.txt"), []byte("edited\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := s.cleanUp(t, tk.id)
		refusedWith(t, err, ReasonCopyDirty)
		stillStands(t, tk)
	})
	t.Run("a detached HEAD on a commit no reference reaches", func(t *testing.T) {
		s, tk := landed(t)
		rawGit(t, tk.copy, "switch", "-q", "--detach")
		loose := commit(t, tk.copy, "loose.txt", "on no reference\n", "loose")
		_, err := s.cleanUp(t, tk.id)
		refusal := refusedWith(t, err, ReasonUnproven)
		stillStands(t, tk)
		if !strings.Contains(attemptFor(t, refusal, store.CleanupProofRemoteBranch), loose) {
			t.Fatalf("the refusal does not name the commit HEAD stands on: %v", refusal)
		}
	})
	t.Run("the copy changing between the proof and the removal", func(t *testing.T) {
		s, tk := landed(t)
		s.cleaner.betweenProofAndRemoval = func() {
			commit(t, tk.copy, "late.txt", "committed after the proof\n", "late")
		}
		_, err := s.cleanUp(t, tk.id)
		refusedWith(t, err, ReasonCopyChanged)
		stillStands(t, tk)
		// The proof stands on the record with no removal, which is what a
		// later cleanup resumes from and re-reads: the late commit is not in
		// it, so that cleanup refuses too rather than removing on a proof of
		// a copy that no longer stands.
		record, err := s.store.TaskCleanup(context.Background(), tk.id)
		if err != nil || record.RemovedAt.IsKnown() {
			t.Fatalf("after the refusal the record is %+v (%v), want the proof with no removal", record, err)
		}
		s.cleaner.betweenProofAndRemoval = nil
		_, err = s.cleanUp(t, tk.id)
		refusedWith(t, err, ReasonCopyChanged)
		stillStands(t, tk)
	})
	t.Run("a terminal that is not dead", func(t *testing.T) {
		s, tk := landed(t)
		for liveness, want := range map[taskstate.Liveness]CleanupReason{
			taskstate.Unobserved: ReasonTerminalUnobserved,
			taskstate.Live:       ReasonTerminalLive,
			"":                   ReasonTerminalUnobserved,
		} {
			_, err := s.cleaner.CleanUp(context.Background(), TaskCopy{Task: tk.id, Terminal: liveness})
			refusedWith(t, err, want)
			stillStands(t, tk)
		}
		// The control: the same copy, with its terminal dead, is removed.
		got, err := s.cleanUp(t, tk.id)
		s.removedOn(t, tk, got, err, store.CleanupProofRemoteBranch)
	})
}

// An investigation copy skips the proofs only as scratch: its inventory
// equal to the start snapshot and its record naming no run and no pull
// request. A new reference, a run or a pull request on the record, and an
// absent snapshot each hold it to the proofs like a delivery copy, which is
// visible in the proof recorded: the content check, which such a copy
// passes because its content is main's, and never scratch. A copy holding a
// new commit is held to the same proofs and, carrying content main lacks,
// refuses; an investigation copy names no branch and no pull request, so
// the content check is the one proof that can hold it.
func TestAnInvestigationCopyIsScratchOnlyWhenUnchanged(t *testing.T) {
	t.Run("unchanged, with its report stored, is removed as scratch", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk := s.investigationTask(t)
		head := rawGit(t, tk.copy, "rev-parse", "HEAD")
		s.storeReport(t, tk.id)
		got, err := s.cleanUp(t, tk.id)
		record := s.removedOn(t, tk, got, err, store.CleanupProofScratch)
		if !reflect.DeepEqual(record.Examined, []string{head}) {
			t.Fatalf("the scratch proof examined %v, want the one commit the copy started on", record.Examined)
		}
		// The task directory and the report's evidence copy survive the
		// copy's removal.
		evidence, _ := s.home.TaskReport(tk.id)
		if _, err := os.Stat(evidence); err != nil {
			t.Fatalf("the evidence copy of the report went with the copy: %v", err)
		}
	})
	t.Run("a new commit is held to the proofs and refuses", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk := s.investigationTask(t)
		s.storeReport(t, tk.id)
		commit(t, tk.copy, "g.txt", "investigated\n", "investigated")
		_, err := s.cleanUp(t, tk.id)
		refusal := refusedWith(t, err, ReasonUnproven)
		stillStands(t, tk)
		if _, err := s.store.TaskCleanup(context.Background(), tk.id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a refusal wrote a cleanup record: %v", err)
		}
		if !strings.Contains(attemptFor(t, refusal, store.CleanupProofRemoteBranch), "names no branch") ||
			!strings.Contains(attemptFor(t, refusal, store.CleanupProofMergedPullRequest), "no merge record") ||
			!strings.Contains(attemptFor(t, refusal, store.CleanupProofDefaultBranch), "would change it") {
			t.Fatalf("the attempts are %+v", refusal.Attempts)
		}
	})
	escalations := map[string]func(t *testing.T, s *cleanupSubject, tk task){
		"a new reference": func(t *testing.T, _ *cleanupSubject, tk task) {
			rawGit(t, tk.copy, "branch", "kept", "HEAD")
		},
		"a run on the record": func(t *testing.T, s *cleanupSubject, tk task) {
			s.pendingRun(t, "run-x")
			if err := s.store.SetTaskRun(context.Background(), tk.id, "run-x"); err != nil {
				t.Fatal(err)
			}
		},
		"a pull request on the record": func(t *testing.T, s *cleanupSubject, tk task) {
			if err := s.store.SetTaskPullRequest(context.Background(), tk.id, "7"); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, escalate := range escalations {
		t.Run(name+" is held to the proofs", func(t *testing.T) {
			s := newCleanupSubject(t, nil)
			tk := s.investigationTask(t)
			s.storeReport(t, tk.id)
			escalate(t, s, tk)
			got, err := s.cleanUp(t, tk.id)
			s.removedOn(t, tk, got, err, store.CleanupProofDefaultBranch)
		})
	}
	t.Run("an absent snapshot is held to the proofs", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		// The copy is exactly as a dispatch leaves it, and no snapshot was
		// recorded, which is the state a dispatch that never reached proven
		// leaves behind.
		id := s.record(t, store.TaskInvestigation, store.Unknown[string]())
		copyPath, _ := s.home.TaskCopy(id)
		rawGit(t, filepath.Dir(copyPath), "clone", "-q", s.upstream, copyPath)
		rawGit(t, copyPath, "remote", "remove", "origin")
		rawGit(t, copyPath, "switch", "-q", "--detach")
		s.storeReport(t, id)
		got, err := s.cleanUp(t, id)
		s.removedOn(t, task{id: id, copy: copyPath}, got, err, store.CleanupProofDefaultBranch)
	})
}

// PRD section 11: cleanup verifies that every decision the report surfaced
// is registered as a durable hold, and refuses without it. The refusal case
// is built the way internal/journey builds a counterfeit, by mutating the
// real observation: the report is stored through the product, which
// registers its holds in the same transaction, and one hold row is then
// deleted through the store's test-only seam. The positive control is the
// unmutated state, which passes with the hold still open afterwards, and an
// unstored report refuses before anything else is read.
func TestCleanupVerifiesTheReportsHoldsAreRegistered(t *testing.T) {
	t.Run("an unstored report refuses", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk := s.investigationTask(t)
		_, err := s.cleanUp(t, tk.id)
		refusedWith(t, err, ReasonReportUnstored)
		stillStands(t, tk)
	})
	t.Run("a stored report with its holds registered passes, and the hold stays open", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk := s.investigationTask(t)
		stored := s.storeReport(t, tk.id)
		if len(stored.Holds) != 1 || !stored.Holds[0].Open() {
			t.Fatalf("the report registered %+v, want one open hold for its one ask finding", stored.Holds)
		}
		got, err := s.cleanUp(t, tk.id)
		s.removedOn(t, tk, got, err, store.CleanupProofScratch)
		hold, err := s.store.Hold(context.Background(), stored.Holds[0].Key)
		if err != nil || !hold.Open() {
			t.Fatalf("after the cleanup the hold is %+v (%v), want still open: a hold closes only when a person answers it", hold, err)
		}
	})
	t.Run("the same report with one hold row gone refuses", func(t *testing.T) {
		s := newCleanupSubject(t, nil)
		tk := s.investigationTask(t)
		stored := s.storeReport(t, tk.id)
		if err := s.store.ForgetHoldForTest(context.Background(), t, stored.Holds[0].Key); err != nil {
			t.Fatalf("building the counterfeit: %v", err)
		}
		_, err := s.cleanUp(t, tk.id)
		refusal := refusedWith(t, err, ReasonHoldUnregistered)
		stillStands(t, tk)
		// The detail counts and does not quote: the hold's text is the
		// worker's and is evidence, not text to relay.
		if !strings.Contains(refusal.Detail, "1 of the 1 decisions") || strings.Contains(refusal.Detail, "retry") {
			t.Fatalf("the refusal says %q", refusal.Detail)
		}
	})
}

// A cleanup interrupted between the proof and the removal mark is completed
// by a second call: on an absent copy at once, and on a standing one after
// re-reading it. The interruption is a panic from inside the window, which
// unwinds the first call the way a crash would end its process, after the
// record carries the proof and before anything marks a removal.
func TestASecondCleanupCompletesARecordACrashLeftBehind(t *testing.T) {
	for _, standing := range []bool{false, true} {
		name := "on an absent copy"
		if standing {
			name = "on a copy still standing"
		}
		t.Run(name, func(t *testing.T) {
			s := newCleanupSubject(t, nil)
			tk := s.deliveryTask(t, "topic")
			commit(t, tk.copy, "g.txt", "on topic\n", "on topic")
			s.push(t, tk.copy, "topic")
			s.cleaner.betweenProofAndRemoval = func() {
				if !standing {
					// The crash came after the directory went and before
					// the mark.
					if err := os.RemoveAll(tk.copy); err != nil {
						t.Fatal(err)
					}
				}
				panic("the process died here")
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("the first cleanup ran past the planted crash")
					}
				}()
				_, _ = s.cleanUp(t, tk.id)
			}()
			record, err := s.store.TaskCleanup(context.Background(), tk.id)
			if err != nil || record.RemovedAt.IsKnown() {
				t.Fatalf("after the crash the record is %+v (%v), want the proof with no removal", record, err)
			}
			s.cleaner.betweenProofAndRemoval = nil
			got, err := s.cleanUp(t, tk.id)
			s.removedOn(t, tk, got, err, store.CleanupProofRemoteBranch)
			again, err := s.cleanUp(t, tk.id)
			if err != nil || again.Result != CleanupAlreadyRemoved {
				t.Fatalf("a third cleanup = %+v, %v; want already removed", again, err)
			}
		})
	}
}

// Nothing a caller can hand the decision stands for a proof, and no remedy
// names a flag. The first half walks the two request types and the
// Cleaner's exported surface the way internal/agents/route walks a type: a
// field or a result that is, or yields, a Removal, or a bool a caller could
// set, would be a route around the refusal, and the walk finds none; the
// control is a type written to carry one, which it does find. The second
// half holds every remedy and every refusal's own text to P12's "never a
// force flag".
func TestNoRequestFieldBypassesARefusalAndNoRemedyNamesAForce(t *testing.T) {
	removal := reflect.TypeOf(Removal{})
	for _, root := range []reflect.Type{reflect.TypeOf(TaskCopy{}), reflect.TypeOf(RunCopy{}), reflect.TypeOf(Cleaner{}), reflect.TypeOf(CleanupDeps{})} {
		if routes := routesToARemoval(root, removal); len(routes) != 0 {
			t.Fatalf("%s offers a caller a way to a proof: %v", root, routes)
		}
	}
	type bypass struct {
		Task  string
		Force bool
	}
	if routes := routesToARemoval(reflect.TypeOf(bypass{}), removal); len(routes) == 0 {
		t.Fatal("the walk reports nothing for a request type carrying a flag, so it is not looking")
	}
	type handed struct{ Proof Removal }
	if routes := routesToARemoval(reflect.TypeOf(handed{}), removal); len(routes) == 0 {
		t.Fatal("the walk reports nothing for a request type carrying a proof, so it is not looking")
	}
	// The Cleaner holds no route to an agent either, so a report it stores
	// cannot reach a fixer through it: nothing reachable from it or from
	// what it stores is a type of internal/agents, which is the only package
	// that starts one. internal/agents/route's walk is not used here because
	// it refuses every empty interface, and store.Optional carries one in
	// its driver.Valuer method; the control is a type that does carry an
	// agents.Runner.
	for _, root := range []reflect.Type{reflect.TypeOf(Cleaner{}), reflect.TypeOf(StoredReport{})} {
		if routes := routesToAgents(root); len(routes) != 0 {
			t.Fatalf("%s reaches an agent: %v", root, routes)
		}
	}
	type carries struct{ Runner agents.Runner }
	if routes := routesToAgents(reflect.TypeOf(carries{})); len(routes) == 0 {
		t.Fatal("the walk reports nothing for a type carrying an agents.Runner, so it is not looking")
	}

	seen := map[CleanupReason]bool{}
	for reason, remedy := range remedies {
		seen[reason] = true
		if strings.Contains(strings.ToLower(remedy), "force") {
			t.Errorf("the remedy for %s names a force: %q", reason, remedy)
		}
		if remedy == "" {
			t.Errorf("the remedy for %s is empty", reason)
		}
		refusal := &CleanupRefusal{Task: "t", Reason: reason, Detail: "d", Attempts: []ProofAttempt{{Proof: store.CleanupProofRemoteBranch, Detail: "x"}}}
		if text := strings.ToLower(refusal.Error() + refusal.Remedy()); strings.Contains(text, "force") {
			t.Errorf("a %s refusal reads %q", reason, text)
		}
	}
	for _, reason := range []CleanupReason{ReasonTerminalUnobserved, ReasonTerminalLive, ReasonCopyDirty, ReasonCopyUntracked,
		ReasonCopyStash, ReasonCopyChanged, ReasonUnproven, ReasonReportUnstored, ReasonHoldUnregistered, ReasonCopyMissing, ReasonShapeMoved} {
		if !seen[reason] {
			t.Errorf("%s has no remedy", reason)
		}
	}
	// The zero Removal is no proof, and a removal nobody proved is refused
	// before the directory is touched.
	dir := t.TempDir()
	if err := removeTaskCopy(dir, Removal{}); err == nil {
		t.Fatal("a directory was removed on a Removal nobody proved")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the refused removal took the directory: %v", err)
	}
}

// routesToARemoval walks what a caller outside this package can reach from
// root, exported fields and the results of exported methods, transitively,
// and reports every path that is a Removal, and every field of the root
// itself that is a bool: a proof anywhere in reach, or a flag on the
// request, would be a route around the refusal. A bool deeper in a record
// the request carries, such as a build's modified flag on a run, is that
// record's fact and not a switch on this decision, so only the root's own
// fields are held to it.
func routesToARemoval(root, removal reflect.Type) []string {
	return routes(root, func(t reflect.Type, depth int) (string, bool) {
		switch {
		case t == removal || t == reflect.PointerTo(removal):
			return "is a Removal", true
		case t.Kind() == reflect.Bool && depth == 1:
			return "is a flag", true
		}
		return "", false
	})
}

// routesToAgents walks on routesToARemoval's terms and reports every path
// that is a type of internal/agents.
func routesToAgents(root reflect.Type) []string {
	return routes(root, func(t reflect.Type, _ int) (string, bool) {
		if strings.HasSuffix(t.PkgPath(), "/internal/agents") {
			return "is " + t.String(), true
		}
		return "", false
	})
}

// routes is the walk the two above share: exported fields, the results of
// exported methods, and the elements of what they hold, transitively, with
// each type's reach reported once.
func routes(root reflect.Type, hit func(t reflect.Type, depth int) (string, bool)) []string {
	var found []string
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type, path string, depth int)
	walk = func(t reflect.Type, path string, depth int) {
		if t == nil {
			return
		}
		if why, ok := hit(t, depth); ok {
			found = append(found, path+" "+why)
			return
		}
		if seen[t] {
			return
		}
		seen[t] = true
		for i := range t.NumMethod() {
			m := t.Method(i)
			if !m.IsExported() {
				continue
			}
			for out := range m.Type.NumOut() {
				walk(m.Type.Out(out), path+"."+m.Name+"()", depth+1)
			}
		}
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
			walk(t.Elem(), path+" element", depth)
		case reflect.Map:
			walk(t.Elem(), path+" value", depth)
		case reflect.Struct:
			for i := range t.NumField() {
				if f := t.Field(i); f.IsExported() {
					walk(f.Type, path+"."+f.Name, depth+1)
				}
			}
			walk(reflect.PointerTo(t), path, depth)
		}
	}
	walk(root, root.String(), 0)
	slices.Sort(found)
	return found
}

// NewCleaner refuses a missing dependency, so a cleaner with no code host
// cannot be built to skip the merged-pull-request proof, and one with no git
// deadline cannot be built to run git under a bound the operator did not
// set. The control is the same dependencies whole, which it builds.
func TestNewCleanerRefusesAMissingDependency(t *testing.T) {
	s := newCleanupSubject(t, nil)
	for name, deps := range map[string]CleanupDeps{
		"no home":         {Store: s.store, Forge: s.forge.Host(), GitDeadline: vcs.DefaultInvocationDeadline},
		"no store":        {Home: s.home, Forge: s.forge.Host(), GitDeadline: vcs.DefaultInvocationDeadline},
		"no code host":    {Home: s.home, Store: s.store, GitDeadline: vcs.DefaultInvocationDeadline},
		"no git deadline": {Home: s.home, Store: s.store, Forge: s.forge.Host()},
	} {
		if _, err := NewCleaner(deps); !errors.Is(err, ErrIncomplete) {
			t.Errorf("NewCleaner with %s = %v, want ErrIncomplete", name, err)
		}
	}
	if _, err := NewCleaner(CleanupDeps{Home: s.home, Store: s.store, Forge: s.forge.Host(),
		GitDeadline: vcs.DefaultInvocationDeadline}); err != nil {
		t.Errorf("NewCleaner with every dependency = %v", err)
	}
}
