package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// TestCommitAllRecordsEveryKindOfChange is what a fix round rests on: an agent
// edits files and the round has to turn that into one commit the stages after
// it can read.
//
// Each kind of change is asserted separately because they fail separately. An
// implementation that staged modifications and not new files would pass a test
// that only edited an existing file, and would produce commits that do not
// contain the work the fixer was asked for.
func TestCommitAllRecordsEveryKindOfChange(t *testing.T) {
	// No t.Parallel: gitEnvironment sets process environment variables, which
	// testing refuses alongside a parallel test.
	repo, dir := newCommitSubject(t)

	writeFile(t, filepath.Join(dir, "existing.txt"), "edited\n")
	writeFile(t, filepath.Join(dir, "added.txt"), "new\n")
	if err := os.Remove(filepath.Join(dir, "removed.txt")); err != nil {
		t.Fatalf("deleting a tracked file: %v", err)
	}

	before, err := repo.ResolveCommit(ctx(t), "HEAD")
	if err != nil {
		t.Fatalf("reading HEAD before the commit: %v", err)
	}
	commit, err := repo.CommitAll(ctx(t), "fix: record every kind of change")
	if err != nil {
		t.Fatalf("committing: %v", err)
	}
	if commit == before {
		t.Fatal("CommitAll returned the commit that was already there, so nothing was recorded")
	}
	head, err := repo.ResolveCommit(ctx(t), "HEAD")
	if err != nil {
		t.Fatalf("reading HEAD after the commit: %v", err)
	}
	if head != commit {
		t.Fatalf("HEAD is %s and CommitAll reported %s, so the commit it named is not the one it made",
			head, commit)
	}

	changed, err := repo.ChangedFiles(ctx(t), before, commit)
	if err != nil {
		t.Fatalf("listing what the commit changed: %v", err)
	}
	got := make(map[string]bool, len(changed))
	for _, change := range changed {
		got[change.Path] = true
	}
	for _, want := range []string{"existing.txt", "added.txt", "removed.txt"} {
		if !got[want] {
			t.Errorf("the commit does not contain %s, and it changed: %v", want, got)
		}
	}
}

// TestCommitAllReportsAWorkingCopyWithNothingToRecord is the answer a fix loop
// converges on.
//
// A round whose agent changed nothing has to be distinguishable from one that
// made a change, or the loop cannot tell progress from none. Reporting success
// here would make an unchanged working copy look like a fix.
func TestCommitAllReportsAWorkingCopyWithNothingToRecord(t *testing.T) {
	repo, _ := newCommitSubject(t)

	before, err := repo.ResolveCommit(ctx(t), "HEAD")
	if err != nil {
		t.Fatalf("reading HEAD: %v", err)
	}
	if _, err := repo.CommitAll(ctx(t), "fix: nothing happened"); !errors.Is(err, vcs.ErrNothingToCommit) {
		t.Fatalf("committing an unchanged working copy returned %v, want ErrNothingToCommit", err)
	}
	after, err := repo.ResolveCommit(ctx(t), "HEAD")
	if err != nil {
		t.Fatalf("reading HEAD after the refusal: %v", err)
	}
	if after != before {
		t.Fatalf("the refusal moved HEAD from %s to %s, so it wrote something after all", before, after)
	}
}

// TestCommitAllRefusesABareRepository keeps the operation on the one kind of
// repository it can mean anything for. A bare repository has no working copy,
// so there is nothing for "every change in the working copy" to name.
func TestCommitAllRefusesABareRepository(t *testing.T) {
	gitEnvironment(t)
	bare, err := vcs.InitBare(ctx(t), filepath.Join(t.TempDir(), "bare.git"))
	if err != nil {
		t.Fatalf("creating a bare repository: %v", err)
	}
	if _, err := bare.CommitAll(ctx(t), "fix: nowhere to do this"); !errors.Is(err, vcs.ErrNotARepository) {
		t.Fatalf("committing in a bare repository returned %v, want ErrNotARepository", err)
	}
}

// newCommitSubject returns a working copy with one commit holding two tracked
// files, and the directory it stands in.
//
// The first commit is made with raw git rather than with CommitAll, so the
// subject a test asserts against is not built by the operation under test.
func newCommitSubject(t *testing.T) (*vcs.Repository, string) {
	t.Helper()
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	for _, name := range []string{"existing.txt", "removed.txt"} {
		writeFile(t, filepath.Join(dir, name), "first\n")
	}
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "first")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatalf("opening the working copy: %v", err)
	}
	return repo, dir
}
