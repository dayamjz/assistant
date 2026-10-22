package stages_test

import (
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// recordingRun is a pushRun whose rebase body writes to a real store holding
// the run's row, submitted the run's commit, and returns the store.
func recordingRun(t *testing.T, run *pushRun) *store.Store {
	t.Helper()
	opened, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening a store: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	if _, err := opened.UpsertRepository(t.Context(), store.Repository{
		ID: run.repositoryID, WorkingPath: run.source, UpstreamURL: run.upstream, DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	if _, err := opened.CreateRun(t.Context(), store.Run{
		ID: run.runID, RepositoryID: run.repositoryID, Branch: "topic", SubmittedHead: run.submitted, Base: run.baseCommit,
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("recording the run: %v", err)
	}
	run.deps = run.deps.WithRebaseRecords(opened)
	git(t, run.source, "config", "user.name", "test")
	git(t, run.source, "config", "user.email", "test@example.invalid")
	return opened
}

// rebasesOf is the run's rebase records.
func rebasesOf(t *testing.T, records *store.Store, run string) []store.RunRebase {
	t.Helper()
	got, err := records.RunRebases(t.Context(), run)
	if err != nil {
		t.Fatalf("reading the run's rebases: %v", err)
	}
	return got
}

// PRD principles P6 and P12: the rebase stage records, at the point it makes
// it, each rebase that rewrote the run's submitted work, so a later cleanup
// can follow a commit a task's copy holds to the commit the run published
// for it. A clean replay of the submitted commit onto a base that moved is
// one record, from the head it replayed to the head it left, onto the base
// it fetched. A replay that conflicts records nothing, because nothing was
// rewritten. A second replay, after a commit of the run's own on top of the
// first one's result, is recorded as carrying the submitted commit too,
// although that commit is no longer in its history: the first record's head
// is. It is not recorded once that commit of the run's took the submitted
// change out or rewrote its lines, although the replay itself was faithful:
// a row names a rebased head that holds what the run was submitted. A replay
// that left the submitted work out records nothing either. And a head
// already on the base records nothing.
func TestTheRebaseStageRecordsEachRebaseThatCarriedTheSubmittedWork(t *testing.T) {
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P12)

	t.Run("a clean replay is one record", func(t *testing.T) {
		run := newPushRun(t)
		records := recordingRun(t, run)
		submitted := run.head
		onto := run.commitOnTopOfUpstream(t, "main", "elsewhere.go", "package subject\n\n// landed first\n")

		run.rebase(t)
		if run.head == submitted {
			t.Fatalf("the rebase left the head at the submitted %s, so it rewrote nothing", submitted)
		}
		got := rebasesOf(t, records, run.runID)
		if len(got) != 1 {
			t.Fatalf("the stage recorded %+v, want one rebase", got)
		}
		if r := got[0]; r.Submitted != submitted || r.Before != submitted || r.Onto != onto || r.After != run.head {
			t.Fatalf("the stage recorded %+v, want %s replayed onto %s as %s", r, submitted, onto, run.head)
		}
	})

	t.Run("a conflicting replay records nothing", func(t *testing.T) {
		run := newPushRun(t)
		records := recordingRun(t, run)
		run.commitOnTopOfUpstream(t, "main", "change.go", "package subject\n\n// somebody else's change\n")

		out := run.rebase(t)
		if len(out.Report.Findings) != 1 || out.Report.Findings[0].ID != "rebase-conflict" {
			t.Fatalf("the replay did not report its conflict: %+v", out.Report)
		}
		if got := rebasesOf(t, records, run.runID); len(got) != 0 {
			t.Fatalf("a replay that conflicted was recorded: %+v", got)
		}
	})

	t.Run("a second replay after the run's own commit still carries the submitted work", func(t *testing.T) {
		run := newPushRun(t)
		records := recordingRun(t, run)
		submitted := run.head
		run.commitOnTopOfUpstream(t, "main", "elsewhere.go", "package subject\n\n// landed first\n")
		run.rebase(t)
		first := run.head

		// A commit the run made on top of the first replay, as a fix round
		// leaves one, and the base moving again before the stage runs again.
		write(t, run.copy, "fixed.go", "package subject\n\n// a fix round's commit\n")
		git(t, run.copy, "add", "-A")
		git(t, run.copy, "commit", "--quiet", "-m", "a fix round's commit")
		fixed := git(t, run.copy, "rev-parse", "HEAD")
		run.head = fixed
		onto := run.commitOnTopOfUpstream(t, "main", "later.go", "package subject\n\n// landed later\n")
		if isAncestor(t, run.source, submitted, fixed) {
			t.Fatalf("the fixed head %s still reaches the submitted %s, so the chain is not exercised", fixed, submitted)
		}

		run.rebase(t)
		got := rebasesOf(t, records, run.runID)
		if len(got) != 2 {
			t.Fatalf("the stage recorded %+v, want two rebases", got)
		}
		if r := got[1]; r.Submitted != submitted || r.Before != fixed || r.Onto != onto || r.After != run.head || r.After == first {
			t.Fatalf("the second record is %+v, want %s replayed onto %s as %s, carrying %s", r, fixed, onto, run.head, submitted)
		}
	})

	t.Run("a second replay after a fix round that removed the submitted work records nothing", func(t *testing.T) {
		run := newPushRun(t)
		records := recordingRun(t, run)
		submitted := run.head
		run.commitOnTopOfUpstream(t, "main", "elsewhere.go", "package subject\n\n// landed first\n")
		run.rebase(t)
		if got := rebasesOf(t, records, run.runID); len(got) != 1 {
			t.Fatalf("the first replay recorded %+v, want one rebase", got)
		}

		// A fix round that takes the submitted change back out, then the base
		// moving again before the stage runs again. The second replay is
		// faithful to what it replayed, so only the submitted commit's
		// content tells this rebase from one that carried the work.
		git(t, run.copy, "rm", "--quiet", "change.go")
		git(t, run.copy, "commit", "--quiet", "-m", "a fix round that removes the change")
		fixed := git(t, run.copy, "rev-parse", "HEAD")
		run.head = fixed
		run.commitOnTopOfUpstream(t, "main", "later.go", "package subject\n\n// landed later\n")

		run.rebase(t)
		if run.head == fixed {
			t.Fatalf("the second rebase left the head at %s, so it rewrote nothing", fixed)
		}
		if _, err := tryGit(run.copy, "cat-file", "-e", run.head+":change.go"); err == nil {
			t.Fatalf("the rebased head %s still holds change.go, so the case is not exercised", run.head)
		}
		if got := rebasesOf(t, records, run.runID); len(got) != 1 {
			t.Fatalf("a rebase whose head %s no longer holds the submitted %s's change was recorded as carrying it: %+v",
				run.head, submitted, got[1:])
		}
	})

	t.Run("a second replay after a fix round that rewrote the submitted lines records nothing", func(t *testing.T) {
		run := newPushRun(t)
		records := recordingRun(t, run)
		run.commitOnTopOfUpstream(t, "main", "elsewhere.go", "package subject\n\n// landed first\n")
		run.rebase(t)

		// The cost the rebaserecord.go comment states: a fix round that
		// rewrites the submitted commit's own lines leaves a head that no
		// longer holds them as submitted, so no row follows it.
		write(t, run.copy, "change.go", "package subject\n\n// the change, as the fix round rewrote it\n")
		git(t, run.copy, "commit", "--quiet", "-am", "a fix round that rewrites the change")
		rewritten := git(t, run.copy, "rev-parse", "HEAD")
		run.head = rewritten
		run.commitOnTopOfUpstream(t, "main", "later.go", "package subject\n\n// landed later\n")

		out := run.rebase(t)
		if len(out.Report.Findings) != 0 || run.head == rewritten {
			t.Fatalf("the second rebase did not replay %s cleanly (head %s, report %+v), so the case is not exercised", rewritten, run.head, out.Report)
		}
		if got := rebasesOf(t, records, run.runID); len(got) != 1 {
			t.Fatalf("a rebase of a fix round that rewrote the submitted lines was recorded: %+v", got[1:])
		}
	})

	t.Run("a replay that dropped a merge's own content records nothing", func(t *testing.T) {
		run := newPushRun(t)
		dropped := mergeOnlyContent(t, run)
		records := recordingRun(t, run)
		submitted := run.head
		run.commitOnTopOfUpstream(t, "main", "elsewhere.go", "package subject\n\n// landed first\n")

		run.rebase(t)
		if run.head == submitted {
			t.Fatalf("the rebase left the head at the submitted %s, so it rewrote nothing", submitted)
		}
		if _, err := tryGit(run.copy, "cat-file", "-e", run.head+":"+dropped); err == nil {
			t.Fatalf("the replayed head %s still holds %s, so the replay dropped nothing and the case is not exercised", run.head, dropped)
		}
		if got := rebasesOf(t, records, run.runID); len(got) != 0 {
			t.Fatalf("a replay that dropped %s, which the submitted merge holds, was recorded as carrying it: %+v", dropped, got)
		}
	})

	t.Run("a replay that skipped a patch the base applied and then reverted records nothing", func(t *testing.T) {
		run := newPushRun(t)
		skipped := run.head
		// The base applies the run's own change and then reverts it, so the
		// replay finds the change already upstream and skips it, while the
		// base no longer holds it.
		run.commitOnTopOfUpstream(t, "main", "change.go", "package subject\n\n// the change under validation\n")
		other := t.TempDir()
		git(t, other, "clone", "--quiet", run.upstream, other)
		git(t, other, "rm", "--quiet", "change.go")
		git(t, other, "commit", "--quiet", "-m", "revert the change")
		git(t, other, "push", "--quiet", "origin", "HEAD:main")
		// A second commit of the run's, so the replay leaves something to
		// publish.
		write(t, run.copy, "second.go", "package subject\n\n// the run's second commit\n")
		git(t, run.copy, "add", "-A")
		git(t, run.copy, "commit", "--quiet", "-m", "the run's second commit")
		run.head = git(t, run.copy, "rev-parse", "HEAD")
		run.submitted = run.head
		records := recordingRun(t, run)

		run.rebase(t)
		if _, err := tryGit(run.copy, "cat-file", "-e", run.head+":change.go"); err == nil {
			t.Fatalf("the replayed head %s still holds change.go, so %s was not skipped and the case is not exercised", run.head, skipped)
		}
		if got := rebasesOf(t, records, run.runID); len(got) != 0 {
			t.Fatalf("a replay that skipped %s's change was recorded as carrying it: %+v", skipped, got)
		}
	})

	t.Run("a head already on the base records nothing", func(t *testing.T) {
		run := newPushRun(t)
		records := recordingRun(t, run)
		before := run.head

		run.rebase(t)
		if run.head != before {
			t.Fatalf("the rebase moved a head already on the base from %s to %s", before, run.head)
		}
		if got := rebasesOf(t, records, run.runID); len(got) != 0 {
			t.Fatalf("a rebase that rewrote nothing was recorded: %+v", got)
		}
	})
}

// mergeOnlyContent makes the run's submitted commit a merge, in the copy, of
// a side branch off the base into the run's commit, whose result adds a file
// neither parent holds, and returns the file. A replay that linearizes the
// history replays the two parents' commits and not the merge, so the file is
// what such a replay drops.
func mergeOnlyContent(t *testing.T, run *pushRun) string {
	t.Helper()
	change := run.head
	git(t, run.copy, "checkout", "--quiet", "--detach", run.baseCommit)
	write(t, run.copy, "side.go", "package subject\n\n// a side branch\n")
	git(t, run.copy, "add", "-A")
	git(t, run.copy, "commit", "--quiet", "-m", "a side branch")
	side := git(t, run.copy, "rev-parse", "HEAD")
	git(t, run.copy, "checkout", "--quiet", "--detach", change)
	git(t, run.copy, "merge", "--quiet", "--no-ff", "--no-commit", side)
	write(t, run.copy, "important.go", "package subject\n\n// written only in the merge\n")
	git(t, run.copy, "add", "-A")
	git(t, run.copy, "commit", "--quiet", "-m", "merge the side branch")
	run.head = git(t, run.copy, "rev-parse", "HEAD")
	run.submitted = run.head
	return "important.go"
}

// tryGit runs git in dir on gitCommand's terms and returns its output and
// error, for a question whose answer is the exit status.
func tryGit(dir string, args ...string) (string, error) {
	out, err := gitCommand(dir, args...).CombinedOutput()
	return string(out), err
}
