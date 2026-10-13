package vcs_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// DiffPath is one file's part of what Diff returns, and nothing else's. The
// discriminator is a sibling whose change must not appear, and a file whose
// name is a wildcard pattern that would select that sibling if the path were
// read as a pattern rather than as written.
func TestDiffPathIsOneFilesPartOfTheDiff(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	writeFile(t, filepath.Join(dir, "a.txt"), "a before\n")
	writeFile(t, filepath.Join(dir, "ab.txt"), "ab before\n")
	writeFile(t, filepath.Join(dir, "a[b].txt"), "bracket before\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "first")
	first := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(dir, "a.txt"), "a after\n")
	writeFile(t, filepath.Join(dir, "ab.txt"), "ab after\n")
	writeFile(t, filepath.Join(dir, "a[b].txt"), "bracket after\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "second")
	second := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))

	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatalf("opening the working copy: %v", err)
	}
	got, err := repo.DiffPath(ctx(t), first, second, "a.txt")
	if err != nil {
		t.Fatalf("DiffPath: %v", err)
	}
	if !strings.Contains(got, "+a after") || strings.Contains(got, "ab after") || strings.Contains(got, "bracket after") {
		t.Fatalf("the diff of a.txt is not a.txt's change alone:\n%s", got)
	}
	star, err := repo.DiffPath(ctx(t), first, second, "a[b].txt")
	if err != nil {
		t.Fatalf("DiffPath of a wildcard name: %v", err)
	}
	if !strings.Contains(star, "+bracket after") || strings.Contains(star, "ab after") || strings.Contains(star, "+a after") {
		t.Fatalf("a file named with a wildcard was read as a pattern:\n%s", star)
	}
	whole, err := repo.Diff(ctx(t), first, second)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(whole, got) {
		t.Fatalf("one file's diff is not a part of the whole diff:\n%s\n---\n%s", got, whole)
	}
	if _, err := repo.DiffPath(ctx(t), first, second, " "); !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Fatalf("an empty path returned %v, want ErrInvalidArgument rather than the whole tree", err)
	}
}
