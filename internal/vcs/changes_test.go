package vcs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// changesOf reads one and fails the test when it could not be read.
func changesOf(t *testing.T, repo *vcs.Repository) vcs.Changes {
	t.Helper()
	c, err := repo.Changes(ctx(t))
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	return c
}

// Each kind of uncommitted work a person acts on differently is reported on
// its own indicator, read from what real git status prints: an untracked
// file, an unstaged edit, a staged edit, and one edit both staged and then
// edited again. A deletion staged with git rm is staged, a deletion made in
// the tree alone is unstaged, an ignored file is on none, and a clean tree
// is clean. The inventory's dirty indicator reads the same parse, so it is
// checked beside each state rather than trusted to agree.
func TestChangesSeparatesUntrackedUnstagedAndStaged(t *testing.T) {
	gitEnvironment(t)
	copy, _, _, _ := clonedCopy(t)
	dir := copy.Path()
	check := func(when string, want vcs.Changes) {
		t.Helper()
		got := changesOf(t, copy)
		// The indicators are compared here; the paths they were read from
		// are TestChangesListsThePathsItReadFrom's.
		if got.Untracked != want.Untracked || got.Unstaged != want.Unstaged || got.Staged != want.Staged || got.Unmerged != want.Unmerged {
			t.Errorf("%s: Changes = %+v, want %+v", when, got, want)
		}
		inv := inventory(t, copy)
		if inv.Dirty != want.Dirty() || inv.Untracked != want.Untracked {
			t.Errorf("%s: Inventory reads dirty=%v untracked=%v, and Changes %+v", when, inv.Dirty, inv.Untracked, want)
		}
		if got.Clean() != want.Clean() || got.Clean() != (len(got.Paths) == 0) {
			t.Errorf("%s: Clean = %v for %+v", when, got.Clean(), got)
		}
	}

	check("a fresh clone", vcs.Changes{})

	writeFile(t, filepath.Join(dir, "new.txt"), "untracked\n")
	check("an untracked file", vcs.Changes{Untracked: true})

	rawGit(t, dir, "add", "new.txt")
	check("the file staged", vcs.Changes{Staged: true})

	writeFile(t, filepath.Join(dir, "new.txt"), "edited after staging\n")
	check("the staged file edited again", vcs.Changes{Staged: true, Unstaged: true})

	rawGit(t, dir, "rm", "--quiet", "--cached", "-f", "new.txt")
	if err := os.Remove(filepath.Join(dir, "new.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "kept.txt"), "edited in place\n")
	check("a tracked file edited", vcs.Changes{Unstaged: true})

	rawGit(t, dir, "checkout", "--quiet", "--", "kept.txt")
	if err := os.Remove(filepath.Join(dir, "kept.txt")); err != nil {
		t.Fatal(err)
	}
	check("a tracked file deleted in the tree", vcs.Changes{Unstaged: true})

	rawGit(t, dir, "rm", "--quiet", "kept.txt")
	check("a tracked file deleted with git rm", vcs.Changes{Staged: true})

	rawGit(t, dir, "reset", "--quiet", "--hard")
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n.gitignore\n")
	writeFile(t, filepath.Join(dir, "ignored.txt"), "ignored\n")
	check("only ignored files", vcs.Changes{})
}

// An unresolved conflict is its own indicator, and dirty.
func TestChangesReportsAnUnresolvedConflict(t *testing.T) {
	gitEnvironment(t)
	copy, _, _, _ := clonedCopy(t)
	dir := copy.Path()
	rawGit(t, dir, "switch", "--quiet", "-c", "side")
	writeFile(t, filepath.Join(dir, "kept.txt"), "side\n")
	rawGit(t, dir, "commit", "--quiet", "-am", "side")
	rawGit(t, dir, "switch", "--quiet", "main")
	writeFile(t, filepath.Join(dir, "kept.txt"), "main\n")
	rawGit(t, dir, "commit", "--quiet", "-am", "main")
	if out, err := tryRawGit(dir, "merge", "--quiet", "side"); err == nil {
		t.Fatalf("the merge did not conflict: %s", out)
	}
	got := changesOf(t, copy)
	if !got.Unmerged || !got.Dirty() {
		t.Errorf("with a conflict: Changes = %+v, want unmerged and dirty", got)
	}
}

// A bare repository has no tree and is refused as not a working copy.
func TestChangesRefusesABareRepository(t *testing.T) {
	gitEnvironment(t)
	bare, err := vcs.InitBare(ctx(t), filepath.Join(t.TempDir(), "gate.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bare.Changes(ctx(t)); !errors.Is(err, vcs.ErrNotARepository) {
		t.Errorf("Changes on a bare repository: %v, want ErrNotARepository", err)
	}
}

// Changes lists the paths its indicators were read from, as git prints
// them, with whether each is untracked or gone from the tree: an untracked
// file, a staged file, an edited tracked file, a path with a space in it,
// and a tracked file deleted in the tree, each once; a clean tree lists
// none. IndexEntries names the index's content by git's own object
// identity, so staging a change moves it and nothing else in the tree does.
func TestChangesListsThePathsItReadFrom(t *testing.T) {
	gitEnvironment(t)
	copy, _, _, _ := clonedCopy(t)
	dir := copy.Path()
	ctx := context.Background()
	paths := func(when string, want []vcs.ChangedPath) {
		t.Helper()
		got := changesOf(t, copy).Paths
		if !slices.Equal(got, want) {
			t.Errorf("%s: Paths = %+v, want %+v", when, got, want)
		}
	}
	paths("a fresh clone", nil)
	index := func(when string) []vcs.IndexEntry {
		t.Helper()
		entries, err := copy.IndexEntries(ctx)
		if err != nil || len(entries) == 0 {
			t.Fatalf("%s: IndexEntries = %+v, %v", when, entries, err)
		}
		for _, e := range entries {
			if e.Mode == "" || e.Object == "" || e.Path == "" || e.Stage != 0 {
				t.Fatalf("%s: an entry reads %+v, want a mode, an object, a path and stage 0", when, e)
			}
		}
		return entries
	}
	clean := index("a clean index")

	writeFile(t, filepath.Join(dir, "new.txt"), "untracked\n")
	writeFile(t, filepath.Join(dir, "with space.txt"), "untracked too\n")
	paths("two untracked files", []vcs.ChangedPath{{Path: "new.txt", Untracked: true}, {Path: "with space.txt", Untracked: true}})
	if entries := index("untracked files"); !slices.Equal(entries, clean) {
		t.Fatalf("IndexEntries with untracked files = %+v; want the clean index's %+v", entries, clean)
	}

	rawGit(t, dir, "add", "new.txt")
	paths("one staged", []vcs.ChangedPath{{Path: "new.txt"}, {Path: "with space.txt", Untracked: true}})
	staged := index("a file staged")
	if slices.Equal(staged, clean) || len(staged) != len(clean)+1 {
		t.Fatalf("IndexEntries with a file staged = %+v; want one more entry than %+v", staged, clean)
	}

	writeFile(t, filepath.Join(dir, "kept.txt"), "edited\n")
	paths("a tracked file edited", []vcs.ChangedPath{{Path: "kept.txt"}, {Path: "new.txt"}, {Path: "with space.txt", Untracked: true}})
	if entries := index("an unstaged edit"); !slices.Equal(entries, staged) {
		t.Fatalf("IndexEntries with an unstaged edit = %+v; want the index's %+v unchanged", entries, staged)
	}

	if err := os.Remove(filepath.Join(dir, "kept.txt")); err != nil {
		t.Fatal(err)
	}
	paths("a tracked file deleted in the tree", []vcs.ChangedPath{{Path: "kept.txt", Gone: true}, {Path: "new.txt"}, {Path: "with space.txt", Untracked: true}})
}
