package vcs_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// commitFile writes content to name in dir and commits it on whatever is
// checked out, returning the new commit.
func commitFile(t *testing.T, dir, name, content, message string) string {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), content)
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", message)
	return strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
}

// A three-way merge over a named base returns the tree the merge produces:
// two sides that changed different files merge to a tree holding both, which
// is neither side's own tree, and two sides that changed one line two ways
// are ErrMergeConflict rather than a tree. A side merged with itself over
// its parent is that side's own tree, which is the control that the answer
// is a tree and comparable to TreeOf's.
func TestMergeTreeAtMergesOverTheNamedBase(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	base := commitFile(t, dir, "a.txt", "one\n", "base")
	ours := commitFile(t, dir, "b.txt", "ours\n", "ours")
	rawGit(t, dir, "checkout", "--quiet", "-b", "other", base)
	theirs := commitFile(t, dir, "c.txt", "theirs\n", "theirs")
	rawGit(t, dir, "checkout", "--quiet", "-b", "clash", base)
	clash := commitFile(t, dir, "b.txt", "something else\n", "clash")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}

	own, err := repo.TreeOf(ctx(t), ours)
	if err != nil {
		t.Fatal(err)
	}
	if same, err := repo.MergeTreeAt(ctx(t), base, ours, ours); err != nil || same != own {
		t.Fatalf("a side merged with itself = %s, %v, want its own tree %s", same, err, own)
	}
	merged, err := repo.MergeTreeAt(ctx(t), base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	theirTree, _ := repo.TreeOf(ctx(t), theirs)
	if merged == own || merged == theirTree {
		t.Fatalf("the merge of two different changes is one side's tree %s", merged)
	}
	if listing := rawGit(t, dir, "ls-tree", "--name-only", merged); !strings.Contains(listing, "b.txt") || !strings.Contains(listing, "c.txt") {
		t.Fatalf("the merged tree does not hold both changes:\n%s", listing)
	}
	if _, err := repo.MergeTreeAt(ctx(t), base, ours, clash); !errors.Is(err, vcs.ErrMergeConflict) {
		t.Fatalf("two edits of one line = %v, want ErrMergeConflict", err)
	}
	if _, err := repo.MergeTreeAt(ctx(t), base, ours, "nowhere"); !errors.Is(err, vcs.ErrRefNotFound) {
		t.Fatalf("an unresolvable side = %v, want ErrRefNotFound", err)
	}
}

// Two histories that merged each other crosswise have two best common
// ancestors, and MergeBases names both; a plain fork has one.
func TestMergeBasesNamesEveryBestCommonAncestor(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	root := commitFile(t, dir, "r.txt", "root\n", "root")
	a := commitFile(t, dir, "a.txt", "a\n", "a")
	rawGit(t, dir, "checkout", "--quiet", "-b", "side", root)
	b := commitFile(t, dir, "b.txt", "b\n", "b")
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if bases, err := repo.MergeBases(ctx(t), a, b); err != nil || !slices.Equal(bases, []string{root}) {
		t.Fatalf("a plain fork has bases %v (%v), want only %s", bases, err, root)
	}
	// Each side merges the other's original tip, so each new tip has both
	// a and b as best common ancestors.
	rawGit(t, dir, "merge", "--quiet", "--no-edit", a)
	x := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	rawGit(t, dir, "checkout", "--quiet", "-b", "crossed", a)
	rawGit(t, dir, "merge", "--quiet", "--no-edit", b)
	y := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	bases, err := repo.MergeBases(ctx(t), x, y)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(bases)
	want := []string{a, b}
	slices.Sort(want)
	if !slices.Equal(bases, want) {
		t.Fatalf("a criss-cross has bases %v, want %v", bases, want)
	}
}

// CreateRef writes a reference that does not exist and refuses to replace
// one that does, whatever it names, leaving it as it was.
func TestCreateRefRefusesAnExistingReference(t *testing.T) {
	gitEnvironment(t)
	dir, first, second := sourceRepo(t)
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRef(ctx(t), "refs/assistant/kept", first); err != nil {
		t.Fatalf("CreateRef: %v", err)
	}
	for _, commit := range []string{second, first} {
		if err := repo.CreateRef(ctx(t), "refs/assistant/kept", commit); !errors.Is(err, vcs.ErrRefExists) {
			t.Fatalf("creating an existing reference at %s = %v, want ErrRefExists", commit, err)
		}
	}
	if got := strings.TrimSpace(rawGit(t, dir, "rev-parse", "refs/assistant/kept")); got != first {
		t.Fatalf("the refused create left the reference at %s", got)
	}
	if err := repo.CreateRef(ctx(t), "assistant/kept", first); !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Fatalf("a short name = %v, want ErrInvalidArgument", err)
	}
}

// IsShallow reads a full clone as whole and a depth-limited one as cut short.
func TestIsShallowTellsACutHistoryApart(t *testing.T) {
	gitEnvironment(t)
	dir, _, _ := sourceRepo(t)
	full, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if shallow, err := full.IsShallow(ctx(t)); err != nil || shallow {
		t.Fatalf("a full repository reads shallow %t (%v)", shallow, err)
	}
	cut := filepath.Join(t.TempDir(), "cut")
	rawGit(t, dir, "clone", "--quiet", "--depth=1", "file://"+filepath.ToSlash(dir), cut)
	repo, err := vcs.OpenWorktree(ctx(t), cut)
	if err != nil {
		t.Fatal(err)
	}
	if shallow, err := repo.IsShallow(ctx(t)); err != nil || !shallow {
		t.Fatalf("a depth-limited clone reads shallow %t (%v)", shallow, err)
	}
}

// MoveBranch records the caller's reason in the branch's reflog.
func TestMoveBranchRecordsTheReason(t *testing.T) {
	gitEnvironment(t)
	dir, first, second := sourceRepo(t)
	rawGit(t, dir, "branch", "landing", first)
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MoveBranch(ctx(t), "landing", first, second, "assistant: a reason of its own"); err != nil {
		t.Fatal(err)
	}
	if log := rawGit(t, dir, "reflog", "show", "--format=%gs", "refs/heads/landing"); !strings.Contains(log, "assistant: a reason of its own") {
		t.Fatalf("the reflog does not carry the reason:\n%s", log)
	}
}
