package vcs_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/procgroup"
	"github.com/dayamjz/assistant/internal/vcs"
)

// TreeEntries reads exactly the paths asked for, literally: a file's blob and
// mode, a directory as its tree, nothing for an absent path, and nothing for
// a path whose name a glob would have matched. TreeWithEntries builds the tree
// a commit by hand records from those entries, with a mode change and a
// removal, without touching the working tree, and refuses a file where a
// directory stands. TreePathsDiffer names both ends of a move.
func TestTreeEntriesBuildTheTreeAHandCommitRecords(t *testing.T) {
	gitEnvironment(t)
	dir := t.TempDir()
	rawGit(t, dir, "init", "--quiet", ".")
	commitFile(t, dir, "a.txt", "one\n", "a")
	commitFile(t, dir, "d/b.txt", "two\n", "b")
	base := commitFile(t, dir, "gone.txt", "three\n", "gone")
	if err := procgroup.WriteProgram(filepath.Join(dir, "a.txt"), []byte("one, changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	rawGit(t, dir, "rm", "--quiet", "gone.txt")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "by hand")
	byHand := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))
	handTree := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD^{tree}"))
	repo, err := vcs.OpenWorktree(ctx(t), dir)
	if err != nil {
		t.Fatal(err)
	}

	entries, err := repo.TreeEntries(ctx(t), byHand, []string{"a.txt", "d", "gone.txt", "*.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if e := entries["a.txt"]; e.Mode != "100755" || e.Type != "blob" || e.Object == "" {
		t.Fatalf("a.txt = %+v, want an executable blob", e)
	}
	if e := entries["d"]; e.Type != "tree" {
		t.Fatalf("d = %+v, want a tree", e)
	}
	if _, ok := entries["gone.txt"]; ok {
		t.Fatal("an absent path came back with an entry")
	}
	if _, ok := entries["*.txt"]; ok || len(entries) != 2 {
		t.Fatalf("a glob was matched as a pattern: %v", entries)
	}

	tree, built, err := repo.TreeWithEntries(ctx(t), base,
		map[string]vcs.TreeEntry{"a.txt": entries["a.txt"]}, []string{"gone.txt"})
	if err != nil || !built || tree != handTree {
		t.Fatalf("TreeWithEntries = %s, %v, %v, want the hand commit's tree %s", tree, built, err, handTree)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(got) != "one, changed\n" {
		t.Fatalf("the working tree changed: %q", got)
	}
	if _, built, err := repo.TreeWithEntries(ctx(t), base,
		map[string]vcs.TreeEntry{"d": entries["a.txt"]}, nil); err != nil || built {
		t.Fatalf("a file where a directory stands = %v, %v, want not built", built, err)
	}
	if paths, err := repo.TreePathsDiffer(ctx(t), base, byHand); err != nil ||
		!slices.Equal(paths, []string{"a.txt", "gone.txt"}) {
		t.Fatalf("TreePathsDiffer = %v, %v, want [a.txt gone.txt]", paths, err)
	}
}
