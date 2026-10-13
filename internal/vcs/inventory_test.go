package vcs_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// clonedCopy builds a source with sourceRepo and clones it the way a task's
// copy is cut, with one remote named upstream, returning the copy's handle,
// the source directory, and the source's two commits.
func clonedCopy(t *testing.T) (copy *vcs.Repository, src, first, second string) {
	t.Helper()
	src, first, second = sourceRepo(t)
	copy, err := vcs.Clone(ctx(t), src, filepath.Join(t.TempDir(), "copy"), "upstream")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	return copy, src, first, second
}

// inventory takes one and fails the test when it could not be taken.
func inventory(t *testing.T, repo *vcs.Repository) vcs.Inventory {
	t.Helper()
	inv, err := repo.Inventory(ctx(t))
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	return inv
}

// refNames lists the names in an inventory, for a message.
func refNames(inv vcs.Inventory) []string {
	var names []string
	for _, r := range inv.Refs {
		names = append(names, r.Name)
	}
	return names
}

// A fresh clone's inventory is its one branch, HEAD on it at the source's
// tip, and nothing dirty, untracked, or stashed.
func TestInventoryOfAFreshClone(t *testing.T) {
	gitEnvironment(t)
	copy, _, _, second := clonedCopy(t)

	inv := inventory(t, copy)
	want := vcs.Inventory{
		Refs: []vcs.Ref{{Name: "refs/heads/main", Object: second, Commit: second}},
		Head: vcs.HeadState{Commit: second, Branch: "main"},
	}
	if !reflect.DeepEqual(inv, want) {
		t.Errorf("Inventory = %+v, want %+v", inv, want)
	}
}

// Every kind of local reference a worker can make is reported with the commit
// it reaches: a branch HEAD is not on, an annotated tag by the commit it peels
// to, and a stash, which is both a reference and the Stash indicator.
func TestInventoryReportsBranchesTagsAndTheStash(t *testing.T) {
	gitEnvironment(t)
	copy, _, first, second := clonedCopy(t)
	dir := copy.Path()
	rawGit(t, dir, "branch", "side", first)
	rawGit(t, dir, "tag", "-a", "-m", "tagged", "v1", first)
	writeFile(t, filepath.Join(dir, "kept.txt"), "stashed edit\n")
	rawGit(t, dir, "stash", "push", "--quiet")
	stash := strings.TrimSpace(rawGit(t, dir, "rev-parse", "refs/stash"))
	tagObject := strings.TrimSpace(rawGit(t, dir, "rev-parse", "refs/tags/v1"))

	inv := inventory(t, copy)
	want := vcs.Inventory{
		Refs: []vcs.Ref{
			{Name: "refs/heads/main", Object: second, Commit: second},
			{Name: "refs/heads/side", Object: first, Commit: first},
			{Name: "refs/stash", Object: stash, Commit: stash},
			{Name: "refs/tags/v1", Object: tagObject, Commit: first},
		},
		Head:  vcs.HeadState{Commit: second, Branch: "main"},
		Stash: true,
	}
	if !reflect.DeepEqual(inv, want) {
		t.Errorf("Inventory = %+v, want %+v", inv, want)
	}
	if tagObject == first {
		t.Fatalf("the tag is not annotated, so peeling was not exercised")
	}
}

// A commit made at a detached HEAD is on no reference, and the inventory
// shows both halves: HEAD detached at that commit, and no entry of Refs
// reaching it.
func TestInventoryReportsADetachedCommitOnNoRef(t *testing.T) {
	gitEnvironment(t)
	copy, _, _, second := clonedCopy(t)
	dir := copy.Path()
	rawGit(t, dir, "switch", "--quiet", "--detach", second)
	writeFile(t, filepath.Join(dir, "loose.txt"), "on no branch\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "--quiet", "-m", "loose")
	loose := strings.TrimSpace(rawGit(t, dir, "rev-parse", "HEAD"))

	inv := inventory(t, copy)
	if want := (vcs.HeadState{Commit: loose, Detached: true}); inv.Head != want {
		t.Errorf("Head = %+v, want %+v", inv.Head, want)
	}
	for _, r := range inv.Refs {
		if r.Commit == loose {
			t.Errorf("%s reaches the detached commit, which should be on no reference", r.Name)
		}
	}
	if inv.Dirty || inv.Untracked || inv.Stash {
		t.Errorf("a clean detached copy reads dirty=%v untracked=%v stash=%v", inv.Dirty, inv.Untracked, inv.Stash)
	}
}

// An untracked file and an edit to a tracked file are each reported on their
// own indicator, and an ignored file is on neither, which is the stated gap.
func TestInventoryReportsUntrackedAndDirtyTrees(t *testing.T) {
	gitEnvironment(t)
	copy, _, _, _ := clonedCopy(t)
	dir := copy.Path()

	writeFile(t, filepath.Join(dir, "new.txt"), "untracked\n")
	inv := inventory(t, copy)
	if !inv.Untracked || inv.Dirty {
		t.Errorf("with an untracked file: untracked=%v dirty=%v, want true, false", inv.Untracked, inv.Dirty)
	}

	rawGit(t, dir, "add", "new.txt")
	inv = inventory(t, copy)
	if inv.Untracked || !inv.Dirty {
		t.Errorf("with the file staged: untracked=%v dirty=%v, want false, true", inv.Untracked, inv.Dirty)
	}
	rawGit(t, dir, "rm", "--quiet", "--cached", "new.txt")

	writeFile(t, filepath.Join(dir, ".gitignore"), "new.txt\n.gitignore\n")
	inv = inventory(t, copy)
	if inv.Untracked || inv.Dirty {
		t.Errorf("with only ignored files: untracked=%v dirty=%v, want neither", inv.Untracked, inv.Dirty)
	}

	writeFile(t, filepath.Join(dir, "kept.txt"), "edited in place\n")
	inv = inventory(t, copy)
	if !inv.Dirty {
		t.Errorf("with a tracked file edited: dirty=%v, want true", inv.Dirty)
	}
	if inv.Stash {
		t.Errorf("Stash reads true with nothing stashed")
	}
}

// A fetch into the copy writes remote-tracking references there, and the
// inventory does not change across it: those references are excluded, which
// is what lets a cleanup's own fetch leave the copy readable as unchanged.
// The fetch is shown to have written something first, so the equality is
// not vacuous.
func TestInventoryIsUnchangedByAFetchAndExcludesRemoteTrackingRefs(t *testing.T) {
	gitEnvironment(t)
	copy, src, _, _ := clonedCopy(t)
	rawGit(t, src, "branch", "upstream-only")
	before := inventory(t, copy)
	if tracking, err := copy.ListRefs(ctx(t), "refs/remotes/"); err != nil || len(tracking) != 0 {
		t.Fatalf("the clone carries remote-tracking references before any fetch: %v, %v", tracking, err)
	}

	if err := copy.Fetch(ctx(t), vcs.FetchSpec{Remote: "upstream", Refspecs: []string{"+refs/heads/*:refs/remotes/upstream/*"}}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	tracking, err := copy.ListRefs(ctx(t), "refs/remotes/")
	if err != nil || len(tracking) == 0 {
		t.Fatalf("the fetch wrote no remote-tracking reference (%v, %v), so the exclusion is not exercised", tracking, err)
	}

	after := inventory(t, copy)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("the inventory changed across a fetch:\nbefore %v\nafter  %v", refNames(before), refNames(after))
	}
	for _, r := range after.Refs {
		if strings.HasPrefix(r.Name, "refs/remotes/") {
			t.Errorf("Inventory reports the remote-tracking reference %s", r.Name)
		}
	}
}

// A bare repository has no tree to inventory and is refused as not a working
// copy.
func TestInventoryRefusesABareRepository(t *testing.T) {
	gitEnvironment(t)
	bare, err := vcs.InitBare(ctx(t), filepath.Join(t.TempDir(), "gate.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bare.Inventory(ctx(t)); !errors.Is(err, vcs.ErrNotARepository) {
		t.Errorf("Inventory on a bare repository: %v, want ErrNotARepository", err)
	}
}
