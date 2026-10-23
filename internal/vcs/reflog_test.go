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

// The reflogs read the same whatever date format the operator's git
// configuration asks git log for: with log.date set to relative in the
// global configuration file the package reads, and to iso in the copy's
// own, AllReflogs and Reflogs read the same positions and objects as with
// neither, the stash list newest first. A name carrying a selector of its
// own, which would ask for a reflog as of a date, is refused before git
// runs, as a full reference name never carries one.
func TestReflogsReadPositionsWhateverTheOperatorsLogDate(t *testing.T) {
	cfg := gitEnvironment(t)
	copy, _, _, _ := clonedCopy(t)
	dir := copy.Path()
	c := ctx(t)
	for _, edit := range []string{"the first stashed edit\n", "the second stashed edit\n"} {
		writeFile(t, filepath.Join(dir, "kept.txt"), edit)
		rawGit(t, dir, "stash", "--quiet")
	}
	plain, err := copy.AllReflogs(c)
	if err != nil {
		t.Fatal(err)
	}
	stash, err := copy.Reflogs(c, "refs/stash")
	if err != nil || len(stash) != 2 || stash[0].Index != 0 || stash[1].Index != 1 {
		t.Fatalf("Reflogs(refs/stash) = %+v, %v; want the two stashes at positions 0 and 1", stash, err)
	}

	appendConfig(t, cfg, "[log]\n\tdate = relative\n")
	rawGit(t, dir, "config", "log.date", "iso")
	if got, err := copy.AllReflogs(c); err != nil || !slices.Equal(got, plain) {
		t.Fatalf("AllReflogs under log.date = %+v, %v; want %+v", got, err, plain)
	}
	if got, err := copy.Reflogs(c, "refs/stash"); err != nil || !slices.Equal(got, stash) {
		t.Fatalf("Reflogs(refs/stash) under log.date = %+v, %v; want %+v", got, err, stash)
	}
	if _, err := copy.Reflogs(c, "refs/stash@{now}"); !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Errorf("Reflogs of a name carrying a selector = %v; want ErrInvalidArgument", err)
	}
}

// ReflogValuesOf reads the named logs alone, by both values of every entry:
// a worktree of a bare repository whose HEAD log keeps, after deletions,
// only entries whose previous values are no entry's new value, the oldest
// entry's and one left by a gap in the middle. Both are read, and neither
// the null identifier a creation moved from nor a value only a log the
// worktree shares with the repository records (a branch's, made with a log)
// is, unless that log is named. A line of another shape is refused, not
// skipped, a name that is not a reference is refused before git runs, a
// bare repository is refused, and so is a repository whose references are
// not files, where git can make one.
func TestReflogValuesOfReadsTheNamedLogsByBothValues(t *testing.T) {
	gitEnvironment(t)
	base := t.TempDir()
	seed := filepath.Join(base, "seed")
	rawGit(t, base, "init", "--quiet", "-b", "main", seed)
	rawGit(t, seed, "commit", "--quiet", "--allow-empty", "-m", "a")
	gate := filepath.Join(base, "gate.git")
	rawGit(t, base, "clone", "--quiet", "--bare", seed, gate)
	wt := filepath.Join(base, "wt")
	rawGit(t, gate, "worktree", "add", "--quiet", "--detach", wt, "main")
	commit := func(message string) string {
		t.Helper()
		rawGit(t, wt, "commit", "--quiet", "--allow-empty", "-m", message)
		return strings.TrimSpace(rawGit(t, wt, "rev-parse", "HEAD"))
	}
	start := strings.TrimSpace(rawGit(t, wt, "rev-parse", "HEAD"))
	b := commit("b")
	c := commit("c")
	d := commit("d")
	e := commit("e")
	// HEAD's log, newest first: d->e, c->d, b->c, start->b. Deleting c->d
	// and start->b leaves d->e and b->c: b and d are previous values no
	// remaining entry moved to.
	rawGit(t, wt, "reflog", "delete", "HEAD@{3}")
	rawGit(t, wt, "reflog", "delete", "HEAD@{1}")
	if moved := rawGit(t, wt, "log", "--walk-reflogs", "--format=%H", "HEAD"); strings.Contains(moved, b) || strings.Contains(moved, d) {
		t.Fatalf("HEAD's log still moves to %s or %s:\n%s", b, d, moved)
	}
	shared := commit("shared")
	rawGit(t, wt, "reset", "--quiet", "--hard", e)
	rawGit(t, wt, "reflog", "delete", "HEAD@{0}")
	rawGit(t, wt, "reflog", "delete", "HEAD@{0}")
	rawGit(t, gate, "update-ref", "--create-reflog", "refs/heads/recorded", shared)
	repo, err := vcs.OpenWorktree(ctx(t), wt)
	if err != nil {
		t.Fatal(err)
	}

	got, err := repo.ReflogValuesOf(ctx(t), "HEAD")
	if err != nil || !slices.Contains(got, b) || !slices.Contains(got, c) || !slices.Contains(got, d) || !slices.Contains(got, e) ||
		slices.Contains(got, shared) || slices.Contains(got, start) {
		t.Fatalf("HEAD's values = %v, %v; want %s, %s, %s and %s, and not the shared log's %s or the deleted entry's %s",
			got, err, b, c, d, e, shared, start)
	}
	// The shared branch's log was made by its creation, so its one entry
	// moved from the null identifier, which is no value.
	if created := rawGit(t, gate, "reflog", "show", "--format=%H", "refs/heads/recorded"); strings.TrimSpace(created) != shared {
		t.Fatalf("the shared branch's log reads %q; want its one creation entry", created)
	}
	named, err := repo.ReflogValuesOf(ctx(t), "HEAD", "refs/heads/recorded", "refs/heads/nolog")
	if err != nil || !slices.Contains(named, shared) {
		t.Fatalf("with the shared log named = %v, %v; want %s", named, err, shared)
	}
	for _, v := range named {
		if strings.Trim(v, "0") == "" {
			t.Fatalf("the values hold the null identifier: %v", named)
		}
	}
	if _, err := repo.ReflogValuesOf(ctx(t), "main"); !errors.Is(err, vcs.ErrInvalidArgument) {
		t.Fatalf("a short name = %v; want ErrInvalidArgument", err)
	}
	bare, err := vcs.OpenBare(ctx(t), gate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bare.ReflogValuesOf(ctx(t), "HEAD"); !errors.Is(err, vcs.ErrNotARepository) {
		t.Fatalf("a bare repository = %v; want ErrNotARepository", err)
	}

	logPath := strings.TrimSpace(rawGit(t, wt, "rev-parse", "--path-format=absolute", "--git-path", "logs/HEAD"))
	writeFile(t, logPath, b+" "+c+"\n")
	if _, err := repo.ReflogValuesOf(ctx(t), "HEAD"); !errors.Is(err, vcs.ErrMalformedOutput) {
		t.Fatalf("a line that is not an entry = %v; want ErrMalformedOutput", err)
	}

	tables := filepath.Join(base, "tables")
	if _, err := tryRawGit(base, "init", "--quiet", "--ref-format=reftable", tables); err != nil {
		t.Logf("this git makes no reftable repository, so the refusal of one is not exercised: %v", err)
		return
	}
	rawGit(t, tables, "commit", "--quiet", "--allow-empty", "-m", "a")
	reftable, err := vcs.OpenWorktree(ctx(t), tables)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reftable.ReflogValuesOf(ctx(t), "HEAD"); !errors.Is(err, vcs.ErrRefStorageUnsupported) {
		t.Fatalf("a reftable repository = %v; want ErrRefStorageUnsupported", err)
	}
}
