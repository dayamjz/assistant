package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// TestAWorkingCopyWhoseNameEndsInASpaceIsTheOneAddressed builds two working
// copies side by side, "repo " and "repo", and stops a rebase on a conflict
// in the first. A path git reports is taken byte for byte, so the handle on
// "repo " names its own root, reads its own conflict, and stages its own
// file; the clean sibling whose name lacks the space is never touched.
func TestAWorkingCopyWhoseNameEndsInASpaceIsTheOneAddressed(t *testing.T) {
	gitEnvironment(t)
	parent := t.TempDir()
	spaced := filepath.Join(parent, "repo ")
	plain := filepath.Join(parent, "repo")
	for _, dir := range []string{spaced, plain} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
		commitFile(t, dir, "clash.txt", "base\n", "base")
	}
	rawGit(t, spaced, "checkout", "--quiet", "-b", "topic")
	commitFile(t, spaced, "clash.txt", "topic\n", "topic")
	rawGit(t, spaced, "checkout", "--quiet", "main")
	main := commitFile(t, spaced, "clash.txt", "main\n", "main")
	rawGit(t, spaced, "checkout", "--quiet", "topic")
	repo, err := vcs.OpenWorktree(ctx(t), spaced)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(spaced)
	if err != nil {
		t.Fatal(err)
	}
	if root, err := repo.WorkingRoot(ctx(t)); err != nil || root != want {
		t.Fatalf("working root %q, %v; want %q", root, err, want)
	}
	if err := repo.Rebase(ctx(t), vcs.RebaseSpec{Onto: main}); !errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("rebasing onto %s: %v, want a conflict", main, err)
	}
	if active, err := repo.RebaseStatus(ctx(t)); err != nil || !active {
		t.Fatalf("rebase in progress %v, %v; want true", active, err)
	}
	records, err := repo.ConflictRecords(ctx(t))
	if err != nil || len(records) != 1 {
		t.Fatalf("conflict records %v, %v; want clash.txt's", records, err)
	}
	if err := os.WriteFile(filepath.Join(plain, "clash.txt"), []byte("sibling edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repo.StagePaths(ctx(t), []string{"clash.txt"}); err != nil {
		t.Fatal(err)
	}
	if unmerged := rawGit(t, spaced, "ls-files", "-u"); unmerged != "" {
		t.Fatalf("staging left the conflict in %q unresolved:\n%s", spaced, unmerged)
	}
	if staged := strings.TrimSpace(rawGit(t, plain, "diff", "--cached", "--name-only")); staged != "" {
		t.Fatalf("staging through %q staged %s in the sibling %q", spaced, staged, plain)
	}
}
