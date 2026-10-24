package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// asideSubject is a working copy on main with two commits, an annotated tag
// v1 on the first, and a linked worktree at the first, opened through the
// package under test, with the directory of each and the two commits.
func asideSubject(t *testing.T) (repo *vcs.Repository, dir, linked, first, second string) {
	t.Helper()
	gitEnvironment(t)
	dir = t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	writeFile(t, filepath.Join(dir, "a.txt"), "first\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "first")
	first = strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	rawGit(t, dir, "tag", "--annotate", "--message", "one", "v1")
	writeFile(t, filepath.Join(dir, "a.txt"), "second\n")
	writeFile(t, filepath.Join(dir, "b.txt"), "second\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "second")
	second = strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	linked = filepath.Join(t.TempDir(), "linked")
	rawGit(t, dir, "worktree", "add", "--quiet", "--detach", linked, first)
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatalf("opening the working copy: %v", err)
	}
	return repo, dir, linked, first, second
}

// reflogLines is how many entries the reflog file at path holds, zero when
// there is none.
func reflogLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

// A reference is deleted only while it points at the object named, and
// written back only where it is gone, as it was and never peeled, with no
// reflog entry: DeleteRefAt refuses a branch that points elsewhere and
// leaves it, deletes it at its object and deletes ORIG_HEAD, a root
// reference, the same way; CreateRefAt refuses a name that stands and
// leaves it, a symbolic reference among them, which it never follows, and
// writes the deleted tag reference back naming the tag object rather than
// its commit, with no reflog for the branch it writes.
// A short name either would let git disambiguate is refused before git
// runs.
func TestAReferenceIsDeletedAndWrittenBackOnlyAsItWasRead(t *testing.T) {
	repo, dir, _, first, second := asideSubject(t)
	c := ctx(t)
	tag := strings.TrimSpace(rawGit(t, dir, "rev-parse", "refs/tags/v1"))
	rawGit(t, dir, "branch", "side", first)

	var cmdErr *vcs.CommandError
	if err := repo.DeleteRefAt(c, "refs/heads/side", second); !errors.As(err, &cmdErr) {
		t.Fatalf("DeleteRefAt at the wrong object = %v; want a *CommandError", err)
	}
	if at := strings.TrimSpace(rawGit(t, dir, "rev-parse", "refs/heads/side")); at != first {
		t.Fatalf("side reads %s after the refused delete; want %s", at, first)
	}
	if err := repo.DeleteRefAt(c, "refs/heads/side", first); err != nil {
		t.Fatalf("DeleteRefAt at its object: %v", err)
	}
	if out, err := tryRawGit(dir, "rev-parse", "--verify", "--quiet", "refs/heads/side"); err == nil {
		t.Fatalf("side still reads %s after its delete", out)
	}
	rawGit(t, dir, "update-ref", "ORIG_HEAD", first)
	if err := repo.DeleteRefAt(c, "ORIG_HEAD", first); err != nil {
		t.Fatalf("DeleteRefAt of ORIG_HEAD: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git", "ORIG_HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ORIG_HEAD stands after its delete: %v", err)
	}

	if err := repo.CreateRefAt(c, "refs/tags/v1", first); !errors.As(err, &cmdErr) {
		t.Fatalf("CreateRefAt over a standing name = %v; want a *CommandError", err)
	}
	if at := strings.TrimSpace(rawGit(t, dir, "rev-parse", "refs/tags/v1")); at != tag {
		t.Fatalf("v1 reads %s after the refused create; want the tag %s", at, tag)
	}
	if err := repo.DeleteRefAt(c, "refs/tags/v1", tag); err != nil {
		t.Fatalf("DeleteRefAt of the tag: %v", err)
	}
	if err := repo.CreateRefAt(c, "refs/tags/v1", tag); err != nil {
		t.Fatalf("CreateRefAt of the tag: %v", err)
	}
	if at := strings.TrimSpace(rawGit(t, dir, "rev-parse", "refs/tags/v1")); at != tag {
		t.Fatalf("v1 reads %s once written back; want the tag object %s", at, tag)
	}
	if err := repo.CreateRefAt(c, "refs/heads/side", first); err != nil {
		t.Fatalf("CreateRefAt of a branch: %v", err)
	}
	if n := reflogLines(t, filepath.Join(dir, ".git", "logs", "refs", "heads", "side")); n != 0 {
		t.Fatalf("the branch written back has a reflog of %d entries; want none", n)
	}

	// A symbolic reference under the name is never followed: one to a
	// branch refuses and leaves both, and one to nothing never writes what
	// it points at (git 2.36 replaces it, newer git refuses).
	rawGit(t, dir, "symbolic-ref", "refs/heads/alias", "refs/heads/main")
	if err := repo.CreateRefAt(c, "refs/heads/alias", first); !errors.As(err, &cmdErr) {
		t.Fatalf("CreateRefAt over a symbolic reference = %v; want a *CommandError", err)
	}
	if target := strings.TrimSpace(rawGit(t, dir, "symbolic-ref", "refs/heads/alias")); target != "refs/heads/main" {
		t.Fatalf("alias points at %q after the refused create; want refs/heads/main", target)
	}
	if at := strings.TrimSpace(rawGit(t, dir, "rev-parse", "refs/heads/main")); at != second {
		t.Fatalf("main reads %s after the refused create; want %s", at, second)
	}
	rawGit(t, dir, "symbolic-ref", "refs/heads/dangling", "refs/heads/missing")
	_ = repo.CreateRefAt(c, "refs/heads/dangling", first)
	if out, err := tryRawGit(dir, "rev-parse", "--verify", "--quiet", "refs/heads/missing"); err == nil {
		t.Fatalf("CreateRefAt followed a symbolic reference to nothing and wrote refs/heads/missing at %s", out)
	}

	for _, short := range []string{"side", "heads/side", "-d"} {
		if err := repo.DeleteRefAt(c, short, first); !errors.Is(err, vcs.ErrInvalidArgument) {
			t.Fatalf("DeleteRefAt(%q) = %v; want an argument refusal", short, err)
		}
		if err := repo.CreateRefAt(c, short, first); !errors.Is(err, vcs.ErrInvalidArgument) {
			t.Fatalf("CreateRefAt(%q) = %v; want an argument refusal", short, err)
		}
	}
}

// HEAD is moved, read and put back on its branch without touching a file
// or the index, and with the reflogs set aside, creating none: MoveHEAD
// refuses when HEAD does not resolve to the commit named and leaves it; it
// detaches HEAD from main onto the first commit with a tracked edit still
// in the working tree and the index as it was; HEADTarget reads it
// detached; PointHEADAt puts it back on main, refusing a name that is not
// a branch in full; HEADTarget reads main; and no reflog stands. Git
// appends to a reflog that stands, which is why a caller sets them aside.
func TestHEADMovesWithoutTouchingTheWorkingTree(t *testing.T) {
	repo, dir, _, first, second := asideSubject(t)
	c := ctx(t)
	writeFile(t, filepath.Join(dir, "a.txt"), "edited\n")
	index := rawGit(t, dir, "ls-files", "--stage")
	if err := os.Rename(filepath.Join(dir, ".git", "logs"), filepath.Join(t.TempDir(), "logs")); err != nil {
		t.Fatal(err)
	}
	if target, err := repo.HEADTarget(c); err != nil || target != "refs/heads/main" {
		t.Fatalf("HEADTarget on main = %q, %v", target, err)
	}

	var cmdErr *vcs.CommandError
	if err := repo.MoveHEAD(c, first, first); !errors.As(err, &cmdErr) {
		t.Fatalf("MoveHEAD from a commit HEAD is not on = %v; want a *CommandError", err)
	}
	if err := repo.MoveHEAD(c, first, second); err != nil {
		t.Fatalf("MoveHEAD: %v", err)
	}
	if head := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD")); head != first {
		t.Fatalf("HEAD reads %s; want %s", head, first)
	}
	if target, err := repo.HEADTarget(c); err != nil || target != "" {
		t.Fatalf("HEADTarget detached = %q, %v; want empty", target, err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(raw) != "edited\n" {
		t.Fatalf("a.txt holds %q, %v after the move; want the edit", raw, err)
	}
	if now := rawGit(t, dir, "ls-files", "--stage"); now != index {
		t.Fatalf("the index reads\n%s\nafter the move; want\n%s", now, index)
	}
	if err := repo.PointHEADAt(c, "refs/tags/v1"); !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Fatalf("PointHEADAt a tag = %v; want an argument refusal", err)
	}
	if err := repo.PointHEADAt(c, "refs/heads/main"); err != nil {
		t.Fatalf("PointHEADAt: %v", err)
	}
	if target, err := repo.HEADTarget(c); err != nil || target != "refs/heads/main" {
		t.Fatalf("HEADTarget once put back = %q, %v", target, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git", "logs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the moves created reflogs: %v; want none", err)
	}
}

// The index is reset to a commit's tree without touching the working tree,
// and a path the index holds is written only where nothing stands:
// ResetIndexTo the first commit leaves b.txt, which only the second
// holds, in the working tree and out of the index; CheckoutMissing of
// a.txt, deleted, writes the first commit's content, and of a.txt again,
// now standing and then edited, refuses and leaves the edit.
func TestTheIndexIsResetAndOnlyMissingPathsAreWritten(t *testing.T) {
	repo, dir, _, first, _ := asideSubject(t)
	c := ctx(t)
	if err := repo.ResetIndexTo(c, first); err != nil {
		t.Fatalf("ResetIndexTo: %v", err)
	}
	if files := strings.TrimSpace(rawGit(t, dir, "ls-files")); files != "a.txt" {
		t.Fatalf("the index lists %q; want the first commit's a.txt alone", files)
	}
	if _, err := os.Lstat(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("b.txt is gone from the working tree: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckoutMissing(c, []string{"a.txt"}); err != nil {
		t.Fatalf("CheckoutMissing of a deleted path: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(raw) != "first\n" {
		t.Fatalf("a.txt holds %q, %v; want the first commit's", raw, err)
	}
	writeFile(t, filepath.Join(dir, "a.txt"), "edited\n")
	var cmdErr *vcs.CommandError
	if err := repo.CheckoutMissing(c, []string{"a.txt"}); !errors.As(err, &cmdErr) {
		t.Fatalf("CheckoutMissing over a standing file = %v; want a *CommandError", err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(raw) != "edited\n" {
		t.Fatalf("a.txt holds %q, %v after the refusal; want the edit", raw, err)
	}
}

// GitPath names where git keeps what a working copy keeps of its own: in a
// linked worktree, its index and HEAD's reflog in its own directory in the
// main repository's worktrees directory, and its references in the common
// directory; in the main working copy, all under .git.
func TestGitPathNamesWhatAWorktreeKeepsOfItsOwn(t *testing.T) {
	_, dir, linked, _, _ := asideSubject(t)
	c := ctx(t)
	wt, err := vcs.OpenWorktree(c, linked)
	if err != nil {
		t.Fatal(err)
	}
	common, err := filepath.EvalSymlinks(filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"index":      filepath.Join(common, "worktrees", "linked", "index"),
		"logs/HEAD":  filepath.Join(common, "worktrees", "linked", "logs", "HEAD"),
		"refs/heads": filepath.Join(common, "refs", "heads"),
	} {
		got, err := wt.GitPath(c, name)
		if err != nil {
			t.Fatalf("GitPath(%q): %v", name, err)
		}
		if resolved, err := filepath.EvalSymlinks(filepath.Dir(got)); err == nil {
			got = filepath.Join(resolved, filepath.Base(got))
		}
		if got != want {
			t.Fatalf("GitPath(%q) in the linked worktree = %s; want %s", name, got, want)
		}
	}
}
