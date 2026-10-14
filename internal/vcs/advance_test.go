package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// A branch checked out nowhere moves as a reference and nothing else: the
// working copy's own checkout stays where it was. The move is a
// compare-and-swap on from, so an anchor the branch no longer names is
// ErrBranchMoved and moves nothing; the first move is the control.
func TestAdvanceBranchMovesABranchNotCheckedOut(t *testing.T) {
	gitEnvironment(t)
	dir, first, second := sourceRepo(t)
	rawGit(t, dir, "branch", "landing", first)
	rawGit(t, dir, "checkout", "--quiet", "-b", "elsewhere", first)
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AdvanceBranch(ctx(t), "landing", first, second); err != nil {
		t.Fatalf("AdvanceBranch: %v", err)
	}
	if got := strings.TrimSpace(rawGit(t, dir, "rev-parse", "refs/heads/landing")); got != second {
		t.Fatalf("landing names %s, want %s", got, second)
	}
	if got := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD")); got != first {
		t.Fatalf("the checked-out branch moved to %s", got)
	}
	err = repo.AdvanceBranch(ctx(t), "landing", first, second)
	if !errors.Is(err, vcs.ErrBranchMoved) {
		t.Fatalf("a move anchored to a commit the branch left = %v, want ErrBranchMoved", err)
	}
}

// A checked-out branch moves with its tree: after the move the files are the
// new commit's and the tree is clean against it. Over uncommitted changes or
// an untracked file the move is ErrTreeNotClean and nothing moves.
func TestAdvanceBranchMovesACheckedOutBranchWithItsTree(t *testing.T) {
	gitEnvironment(t)
	dir, first, second := sourceRepo(t)
	rawGit(t, dir, "checkout", "--quiet", "-B", "main", first)
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dir, "stray.txt"), "untracked\n")
	if err := repo.AdvanceBranch(ctx(t), "main", first, second); !errors.Is(err, vcs.ErrTreeNotClean) {
		t.Fatalf("a move over an untracked file = %v, want ErrTreeNotClean", err)
	}
	if err := os.Remove(filepath.Join(dir, "stray.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "kept.txt"), "edited by a person\n")
	if err := repo.AdvanceBranch(ctx(t), "main", first, second); !errors.Is(err, vcs.ErrTreeNotClean) {
		t.Fatalf("a move over an uncommitted change = %v, want ErrTreeNotClean", err)
	}
	if got := strings.TrimSpace(rawGit(t, dir, "rev-parse", "main")); got != first {
		t.Fatalf("a refused move left main at %s", got)
	}
	rawGit(t, dir, "checkout", "--quiet", "--", "kept.txt")

	if err := repo.AdvanceBranch(ctx(t), "main", first, second); err != nil {
		t.Fatalf("AdvanceBranch: %v", err)
	}
	if got := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD")); got != second {
		t.Fatalf("HEAD is %s, want %s", got, second)
	}
	if status := rawGit(t, dir, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("the tree does not match the moved branch:\n%s", status)
	}
	if _, err := os.Stat(filepath.Join(dir, "added.txt")); err != nil {
		t.Fatalf("the new commit's file is not in the tree: %v", err)
	}
}
