package vcs_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// TestADigestFromASubdirectoryHandleSeesTheWholeCopy opens one handle on the
// working copy's root and one on a subdirectory. The digest describes the
// copy, not the directory a handle was opened on, so an edit inside the
// subdirectory and one outside it each change both handles' digests, and
// the two handles agree.
func TestADigestFromASubdirectoryHandleSeesTheWholeCopy(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	writeFile(t, filepath.Join(dir, "sub", "f.txt"), "one\n")
	writeFile(t, filepath.Join(dir, "top.txt"), "top\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "base")
	root, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := vcs.OpenWorktree(ctx(t), filepath.Join(dir, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	digests := func() (string, string) {
		t.Helper()
		a, err := root.UncommittedDigest(ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		b, err := sub.UncommittedDigest(ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		return a, b
	}
	r0, s0 := digests()
	writeFile(t, filepath.Join(dir, "sub", "f.txt"), "two\n")
	r1, s1 := digests()
	writeFile(t, filepath.Join(dir, "sub", "f.txt"), "three\n")
	r2, s2 := digests()
	writeFile(t, filepath.Join(dir, "top.txt"), "edited\n")
	r3, s3 := digests()
	if s0 == s1 || s1 == s2 || s2 == s3 {
		t.Fatalf("the subdirectory handle's digest did not follow the edits: %s %s %s %s", s0, s1, s2, s3)
	}
	if r0 != s0 || r1 != s1 || r2 != s2 || r3 != s3 {
		t.Fatal("the root and subdirectory handles disagree about the same copy")
	}
}

// TestConflictReadsFromAnUnrelatedSubdirectoryHandle stops a rebase on a
// conflict in a file outside the subdirectory a second handle is opened on.
// Every conflict read, and the staging and unresolving of a path the reads
// name, answers for the whole copy from that handle as from the root's.
func TestConflictReadsFromAnUnrelatedSubdirectoryHandle(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", "-b", "main", ".")
	writeFile(t, filepath.Join(dir, "other", "keep.txt"), "keep\n")
	commitFile(t, dir, "clash.txt", "base\n", "base")
	rawGit(t, dir, "checkout", "--quiet", "-b", "topic")
	commitFile(t, dir, "clash.txt", "topic\n", "topic")
	rawGit(t, dir, "checkout", "--quiet", "main")
	main := commitFile(t, dir, "clash.txt", "main\n", "main")
	rawGit(t, dir, "checkout", "--quiet", "topic")
	root, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Rebase(ctx(t), vcs.RebaseSpec{Onto: main}); !errors.Is(err, vcs.ErrRebaseConflict) {
		t.Fatalf("rebasing onto %s: %v, want a conflict", main, err)
	}
	sub, err := vcs.OpenWorktree(ctx(t), filepath.Join(dir, "other"))
	if err != nil {
		t.Fatal(err)
	}
	if active, err := sub.RebaseStatus(ctx(t)); err != nil || !active {
		t.Fatalf("rebase in progress from the subdirectory: %v, %v; want true", active, err)
	}
	want, err := root.ConflictRecords(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := sub.ConflictRecords(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 1 || len(got) != len(want) || got["clash.txt"] != want["clash.txt"] {
		t.Fatalf("conflict records from the subdirectory %v, from the root %v", got, want)
	}
	markers, err := sub.ConflictMarkers(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(markers["clash.txt"]) == 0 {
		t.Fatalf("conflict markers from the subdirectory: %v, want clash.txt's", markers)
	}
	left, err := sub.MarkersLeft(ctx(t), markers)
	if err != nil || !slices.Equal(left, []string{"clash.txt"}) {
		t.Fatalf("markers left from the subdirectory: %v, %v; want clash.txt", left, err)
	}
	if err := sub.StagePaths(ctx(t), []string{"clash.txt"}); err != nil {
		t.Fatalf("staging clash.txt from the subdirectory: %v", err)
	}
	if unmerged := rawGit(t, dir, "ls-files", "-u"); unmerged != "" {
		t.Fatalf("after staging from the subdirectory, still unmerged:\n%s", unmerged)
	}
	if err := sub.UnresolvePaths(ctx(t), []string{"clash.txt"}); err != nil {
		t.Fatalf("unresolving clash.txt from the subdirectory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other", "clash.txt")); !os.IsNotExist(err) {
		t.Fatalf("an operation made other/clash.txt: %v", err)
	}
}
