package vcs_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// The reflogs are read entry by entry, so the stash list a saved tip was
// stored back onto reads differently from the list before it, though
// refs/stash names the same commit both times: two stashes are made, the
// first one's commit stored back on top, and refs/stash's log reads the
// three entries newest first, the same through AllReflogs and through
// Reflogs. HEAD's log is among AllReflogs, a reference with no log reads
// nothing, a reference that does not exist is refused, a name that is not
// a full reference is refused before git runs, and a bare repository is
// refused.
func TestReflogsReadEveryEntryOfTheStashList(t *testing.T) {
	gitEnvironment(t)
	copy, _, _, _ := clonedCopy(t)
	dir := copy.Path()
	c := ctx(t)
	stashOf := func(entries []vcs.ReflogEntry) []string {
		var out []string
		for i, e := range entries {
			if e.Ref == "refs/stash" {
				if len(out) != e.Index {
					t.Fatalf("entry %d of the stash list is at position %d, want %d", i, e.Index, len(out))
				}
				out = append(out, e.Object)
			}
		}
		return out
	}

	writeFile(t, filepath.Join(dir, "kept.txt"), "the first stashed edit\n")
	rawGit(t, dir, "stash", "--quiet")
	saved := strings.TrimSpace(rawGit(t, dir, "rev-parse", "refs/stash"))
	before, err := copy.AllReflogs(c)
	if err != nil || !slices.Equal(stashOf(before), []string{saved}) {
		t.Fatalf("AllReflogs with one stash = %+v, %v; want the stash list to be %s alone", before, err, saved)
	}
	writeFile(t, filepath.Join(dir, "kept.txt"), "the second stashed edit\n")
	rawGit(t, dir, "stash", "--quiet")
	second := strings.TrimSpace(rawGit(t, dir, "rev-parse", "refs/stash"))
	rawGit(t, dir, "stash", "store", "--quiet", "-m", "stored back", saved)
	if tip := strings.TrimSpace(rawGit(t, dir, "rev-parse", "refs/stash")); tip != saved {
		t.Fatalf("refs/stash is %s, want it back at %s", tip, saved)
	}

	all, err := copy.AllReflogs(c)
	if err != nil || !slices.Equal(stashOf(all), []string{saved, second, saved}) {
		t.Fatalf("AllReflogs = %+v, %v; want the stash list %s, %s, %s", all, err, saved, second, saved)
	}
	if !slices.ContainsFunc(all, func(e vcs.ReflogEntry) bool { return e.Ref == "HEAD" }) {
		t.Errorf("AllReflogs = %+v; want HEAD's log among them", all)
	}
	named, err := copy.Reflogs(c, "refs/stash")
	if err != nil || !slices.Equal(named, slices.DeleteFunc(slices.Clone(all), func(e vcs.ReflogEntry) bool { return e.Ref != "refs/stash" })) {
		t.Fatalf("Reflogs(refs/stash) = %+v, %v; want the stash list AllReflogs read", named, err)
	}
	again, err := copy.AllReflogs(c)
	if err != nil || !slices.Equal(again, all) {
		t.Fatalf("AllReflogs read again = %+v, %v; want %+v", again, err, all)
	}

	rawGit(t, dir, "tag", "unlogged")
	if got, err := copy.Reflogs(c, "refs/tags/unlogged"); err != nil || len(got) != 0 {
		t.Errorf("Reflogs of a tag with no log = %+v, %v; want nothing", got, err)
	}
	var command *vcs.CommandError
	if _, err := copy.Reflogs(c, "refs/heads/absent"); !errors.As(err, &command) {
		t.Errorf("Reflogs of a reference that does not exist = %v; want git's refusal", err)
	}
	if _, err := copy.Reflogs(c, "stash"); !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Errorf("Reflogs of a short name = %v; want ErrInvalidArgument", err)
	}
	bare, err := vcs.InitBare(c, filepath.Join(t.TempDir(), "gate.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bare.AllReflogs(c); !errors.Is(err, vcs.ErrNotARepository) {
		t.Errorf("AllReflogs on a bare repository = %v; want ErrNotARepository", err)
	}
}
