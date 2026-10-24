package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// An index path is replaced whole and no other is touched, and a path
// the index holds is written only where nothing stands:
// ReplaceIndexEntries puts a.txt back at the first commit's blob and
// takes b.txt out, leaving both files of the working tree and leaving
// c.txt, staged and named by neither, staged; it puts a.txt back
// conflicted, stages 1 and 3, and back at stage 0 again; it refuses an
// entry at a path not among those replaced and an object of the wrong
// length before git runs. CheckoutMissing of a.txt, deleted, writes the
// index's content, and of a.txt again, now standing and then edited,
// refuses and leaves the edit.
func TestTheIndexIsReplacedByPathAndOnlyMissingPathsAreWritten(t *testing.T) {
	repo, dir, _, first, _ := asideSubject(t)
	c := ctx(t)
	writeFile(t, filepath.Join(dir, "c.txt"), "staged\n")
	rawGit(t, dir, "add", "c.txt")
	staged := rawGit(t, dir, "ls-files", "--stage", "--", "c.txt")
	a := strings.TrimSpace(rawGit(t, dir, "rev-parse", first+":a.txt"))
	if err := repo.ReplaceIndexEntries(c, []string{"a.txt", "b.txt"}, []vcs.IndexEntry{{Mode: "100644", Object: a, Path: "a.txt"}}); err != nil {
		t.Fatalf("ReplaceIndexEntries: %v", err)
	}
	if files := strings.Fields(rawGit(t, dir, "ls-files")); !slices.Equal(files, []string{"a.txt", "c.txt"}) {
		t.Fatalf("the index lists %q; want a.txt and c.txt", files)
	}
	if got := rawGit(t, dir, "ls-files", "--stage", "--", "a.txt"); !strings.Contains(got, a+" 0\ta.txt") {
		t.Fatalf("a.txt's entry reads %q; want %s at stage 0", got, a)
	}
	if got := rawGit(t, dir, "ls-files", "--stage", "--", "c.txt"); got != staged {
		t.Fatalf("c.txt's entry reads %q; want it untouched, %q", got, staged)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s is gone from the working tree: %v", name, err)
		}
	}
	conflicted := []vcs.IndexEntry{{Mode: "100644", Object: a, Stage: 1, Path: "a.txt"}, {Mode: "100644", Object: a, Stage: 3, Path: "a.txt"}}
	if err := repo.ReplaceIndexEntries(c, []string{"a.txt"}, conflicted); err != nil {
		t.Fatalf("ReplaceIndexEntries conflicted: %v", err)
	}
	if got := strings.Count(rawGit(t, dir, "ls-files", "--stage", "--", "a.txt"), "a.txt"); got != 2 {
		t.Fatalf("a.txt has %d entries once conflicted; want stages 1 and 3", got)
	}
	if err := repo.ReplaceIndexEntries(c, []string{"a.txt"}, []vcs.IndexEntry{{Mode: "100644", Object: a, Path: "a.txt"}}); err != nil {
		t.Fatalf("ReplaceIndexEntries back to stage 0: %v", err)
	}
	if got := rawGit(t, dir, "ls-files", "--stage", "--", "a.txt"); strings.Count(got, "a.txt") != 1 || !strings.Contains(got, " 0\ta.txt") {
		t.Fatalf("a.txt's entries read %q; want one at stage 0", got)
	}
	for _, bad := range []vcs.IndexEntry{{Mode: "100644", Object: a, Path: "b.txt"}, {Mode: "100644", Object: a[:10], Path: "a.txt"}} {
		if err := repo.ReplaceIndexEntries(c, []string{"a.txt"}, []vcs.IndexEntry{bad}); !errors.Is(err, vcs.ErrInvalidArgument) {
			t.Fatalf("ReplaceIndexEntries(%+v) = %v; want an argument refusal", bad, err)
		}
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

// Configuration is read as git reads it, and the repository directory as
// git names it: ConfigBool reports a key unset as unset, reads git's
// spellings of a boolean ("yes", "off") as git does, and refuses a value
// git does not read as one; ConfigValue reports the last of several values
// set, as git uses; GitDir names a main working copy's .git and a linked
// worktree's own directory in its repository.
func TestConfigAndTheRepositoryDirectoryAreReadAsGitReadsThem(t *testing.T) {
	repo, dir, linked, _, _ := asideSubject(t)
	c := ctx(t)
	if value, set, err := repo.ConfigBool(c, "core.sparseCheckout"); err != nil || set || value {
		t.Fatalf("ConfigBool of an unset key = %v, %v, %v; want unset", value, set, err)
	}
	rawGit(t, dir, "config", "core.sparseCheckout", "yes")
	if value, set, err := repo.ConfigBool(c, "core.sparseCheckout"); err != nil || !set || !value {
		t.Fatalf("ConfigBool of yes = %v, %v, %v; want set and true", value, set, err)
	}
	rawGit(t, dir, "config", "core.sparseCheckout", "off")
	if value, set, err := repo.ConfigBool(c, "core.sparseCheckout"); err != nil || !set || value {
		t.Fatalf("ConfigBool of off = %v, %v, %v; want set and false", value, set, err)
	}
	rawGit(t, dir, "config", "core.sparseCheckout", "sometimes")
	var cmdErr *vcs.CommandError
	if _, _, err := repo.ConfigBool(c, "core.sparseCheckout"); !errors.As(err, &cmdErr) {
		t.Fatalf("ConfigBool of a value that is no boolean = %v; want a *CommandError", err)
	}
	rawGit(t, dir, "config", "--unset", "core.sparseCheckout")
	rawGit(t, dir, "config", "--add", "x.y", "first")
	rawGit(t, dir, "config", "--add", "x.y", "last")
	if value, set, err := repo.ConfigValue(c, "x.y"); err != nil || !set || value != "last" {
		t.Fatalf("ConfigValue of a key set twice = %q, %v, %v; want the last, set", value, set, err)
	}
	if _, set, err := repo.ConfigValue(c, "x.unset"); err != nil || set {
		t.Fatalf("ConfigValue of an unset key = %v, %v; want unset", set, err)
	}
	wt, err := vcs.OpenWorktree(c, linked)
	if err != nil {
		t.Fatal(err)
	}
	common, err := filepath.EvalSymlinks(filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		repo *vcs.Repository
		want string
	}{{repo, common}, {wt, filepath.Join(common, "worktrees", "linked")}} {
		got, err := w.repo.GitDir(c)
		if err != nil {
			t.Fatal(err)
		}
		if resolved, err := filepath.EvalSymlinks(got); err != nil || resolved != w.want {
			t.Fatalf("GitDir = %s (%s, %v); want %s", got, resolved, err, w.want)
		}
	}
}
