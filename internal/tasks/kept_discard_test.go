package tasks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// What a discard would lose is read from the entry's basis and the copy as
// it stands, with a digest that names exactly that state, contents
// included: the same copy digests the same twice, a file no commit records
// changes it, and removing the file brings the first digest back; an edit
// to a tracked file and a second untracked file change it while every name
// and indicator stays the same, and staging the edit changes it again. Discard refuses a digest that is
// not the copy's and removes nothing, removes the clone on the copy's own
// digest, and treats a copy already gone as nothing to lose; Loss refuses
// an entry whose basis is not established and a copy that is gone. Without
// the digest comparison a changed copy is removed on a stale confirmation.
func TestADiscardRemovesTheCopyOnlyWhileItHoldsExactlyWhatWasShown(t *testing.T) {
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/discard")
	own := commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit")
	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Recorded || !got.Entry.Established() {
		t.Fatalf("Keep = %+v, %v; want a new established entry", got, err)
	}
	entry := got.Entry

	first, err := s.cleaner.Loss(ctx, entry)
	if err != nil || first.Digest == "" || len(first.Lacks) != 1 || first.Lacks[0] != own || first.Head != own ||
		first.Dirty || first.Untracked || first.Stash {
		t.Fatalf("Loss = %+v, %v; want the task's own commit lacking, HEAD on it, nothing uncommitted, and a digest", first, err)
	}
	if again, err := s.cleaner.Loss(ctx, entry); err != nil || again.Digest != first.Digest {
		t.Fatalf("the same copy digests %s then %s (%v)", first.Digest, again.Digest, err)
	}
	stray := filepath.Join(tk.copy, "stray.txt")
	if err := os.WriteFile(stray, []byte("not committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := s.cleaner.Loss(ctx, entry)
	if err != nil || changed.Digest == first.Digest || !changed.Untracked {
		t.Fatalf("with a file added Loss = %+v, %v; want the untracked indicator and another digest", changed, err)
	}
	if _, err := s.cleaner.Discard(ctx, entry, first.Digest, ""); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on the digest shown before the file = %v; want ErrLossChanged", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatalf("the refused discard removed the copy: %v", err)
	}
	if err := os.Remove(stray); err != nil {
		t.Fatal(err)
	}
	if back, err := s.cleaner.Loss(ctx, entry); err != nil || back.Digest != first.Digest {
		t.Fatalf("with the file removed the copy digests %s (%v), want %s again", back.Digest, err, first.Digest)
	}
	if _, err := s.cleaner.Discard(ctx, entry, "not-the-digest", ""); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on another digest = %v; want ErrLossChanged", err)
	}
	// The contents, not the names: a dirty copy with an untracked file is
	// shown, then the tracked file is edited again and another file is
	// added, which keeps every name and both indicators the same; the
	// digest shown no longer names the copy, and a discard on it is refused.
	// Staging the edit changes it again.
	tracked := filepath.Join(tk.copy, "own.txt")
	if err := os.WriteFile(tracked, []byte("edited once\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stray, []byte("the first untracked file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty, err := s.cleaner.Loss(ctx, entry)
	if err != nil || !dirty.Dirty || !dirty.Untracked || dirty.Digest == first.Digest {
		t.Fatalf("a dirty copy with an untracked file = %+v, %v; want both indicators and another digest", dirty, err)
	}
	// The contents alone first: the same names, the same indicators, other
	// bytes in the tracked file and in the untracked one.
	if err := os.WriteFile(tracked, []byte("edited twice, which the names do not show\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stray, []byte("the first untracked file, rewritten\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	edited, err := s.cleaner.Loss(ctx, entry)
	if names := changedPaths(t, tk.copy); !slices.Equal(names, []string{"own.txt", "stray.txt"}) {
		t.Fatalf("after the edits git reports %v changed, want the same two names as before them", names)
	}
	if err != nil || edited.Digest == dirty.Digest || edited.Dirty != dirty.Dirty || edited.Untracked != dirty.Untracked {
		t.Fatalf("after the edits Loss = %+v, %v; want the same indicators and another digest than %s", edited, err, dirty.Digest)
	}
	if _, err := s.cleaner.Discard(ctx, entry, dirty.Digest, ""); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on the digest shown before the edits = %v; want ErrLossChanged", err)
	}
	// Then another file beside them.
	if err := os.WriteFile(filepath.Join(tk.copy, "second.txt"), []byte("more new work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := s.cleaner.Loss(ctx, entry)
	if err != nil || added.Digest == edited.Digest {
		t.Fatalf("with a second file Loss = %+v, %v; want another digest than %s", added, err, edited.Digest)
	}
	if _, err := s.cleaner.Discard(ctx, entry, dirty.Digest, ""); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on the digest shown before the edits and the file = %v; want ErrLossChanged", err)
	}
	if _, err := os.Stat(filepath.Join(tk.copy, "second.txt")); err != nil {
		t.Fatalf("the refused discard removed the new work: %v", err)
	}
	rawGit(t, tk.copy, "add", "own.txt")
	staged, err := s.cleaner.Loss(ctx, entry)
	if err != nil || staged.Digest == added.Digest {
		t.Fatalf("with the edit staged Loss = %+v, %v; want another digest than %s", staged, err, added.Digest)
	}
	rawGit(t, tk.copy, "reset", "--quiet", "own.txt")
	rawGit(t, tk.copy, "checkout", "--", "own.txt")
	if err := os.Remove(filepath.Join(tk.copy, "second.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stray); err != nil {
		t.Fatal(err)
	}
	if back, err := s.cleaner.Loss(ctx, entry); err != nil || back.Digest != first.Digest {
		t.Fatalf("with everything undone the copy digests %s (%v), want %s again", back.Digest, err, first.Digest)
	}
	lost, err := s.cleaner.Discard(ctx, entry, first.Digest, "")
	if err != nil || lost.Digest != first.Digest {
		t.Fatalf("Discard = %+v, %v; want the copy removed on its own digest", lost, err)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the discarded copy still stands: %v", err)
	}
	if _, err := s.cleaner.Loss(ctx, entry); !errors.Is(err, ErrCopyGone) {
		t.Fatalf("Loss of a gone copy = %v; want ErrCopyGone", err)
	}
	if gone, err := s.cleaner.Discard(ctx, entry, first.Digest, ""); err != nil || gone.Digest != "" {
		t.Fatalf("Discard of a gone copy = %+v, %v; want nothing to lose and no error", gone, err)
	}

	unestablished := entry
	unestablished.Against = store.Unknown[string]()
	unestablished.Unestablished = store.Known("the upstream could not be read")
	if _, err := s.cleaner.Loss(ctx, unestablished); !errors.Is(err, ErrLossUnestablished) {
		t.Fatalf("Loss of an unestablished entry = %v; want ErrLossUnestablished", err)
	}
}

// changedPaths is what git reports changed or untracked in the copy, by
// name, sorted.
func changedPaths(t *testing.T, copyPath string) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(rawGit(t, copyPath, "status", "--porcelain", "--untracked-files=all")), "\n") {
		if fields := strings.Fields(line); len(fields) > 1 {
			out = append(out, fields[len(fields)-1])
		}
	}
	slices.Sort(out)
	return out
}

// The digest covers the index at every stage and an untracked directory
// down to its contents: a conflicted copy whose staged side is edited and
// whose working version is then restored keeps every indicator and name and
// digests differently, and so does an edit inside an untracked directory,
// a nested repository among them, which git reports as one path. Without
// the index's entries the conflicted copy's staged work is removed on the
// stale digest; without the walk the nested edit is.
func TestADiscardDigestCoversTheIndexAndUntrackedDirectories(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/conflict")
	commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit")
	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Entry.Established() {
		t.Fatalf("Keep = %+v, %v", got, err)
	}
	entry := got.Entry

	// A conflict on own.txt: a branch edits it one way, the task's branch
	// another, and a merge leaves it unmerged with three stages.
	base := rawGit(t, tk.copy, "rev-parse", "HEAD~1")
	rawGit(t, tk.copy, "checkout", "--quiet", "-b", "other", base)
	commit(t, tk.copy, "own.txt", "the other side\n", "the other side's commit")
	rawGit(t, tk.copy, "checkout", "--quiet", "task/conflict")
	if _, err := rawGitErr(tk.copy, "merge", "--quiet", "--no-commit", "other"); err == nil {
		t.Fatal("the merge did not conflict")
	}
	conflicted, err := s.cleaner.Loss(ctx, entry)
	if err != nil || !conflicted.Dirty {
		t.Fatalf("Loss of a conflicted copy = %+v, %v; want it dirty", conflicted, err)
	}
	working, err := os.ReadFile(filepath.Join(tk.copy, "own.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// Stage a resolution, then restore the conflicted working version: the
	// names, the indicators and the tree are as shown, and the index is not.
	if err := os.WriteFile(filepath.Join(tk.copy, "own.txt"), []byte("resolved one way\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "add", "own.txt")
	if err := os.WriteFile(filepath.Join(tk.copy, "own.txt"), working, 0o600); err != nil {
		t.Fatal(err)
	}
	staged, err := s.cleaner.Loss(ctx, entry)
	if err != nil || staged.Digest == conflicted.Digest {
		t.Fatalf("with a resolution staged and the working version restored Loss = %+v, %v; want another digest than %s", staged, err, conflicted.Digest)
	}
	if _, err := s.cleaner.Discard(ctx, entry, conflicted.Digest, ""); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on the digest shown before the resolution was staged = %v; want ErrLossChanged", err)
	}
	_, _ = rawGitErr(tk.copy, "merge", "--abort")
	rawGit(t, tk.copy, "reset", "--quiet", "--hard", "HEAD")
	if names := changedPaths(t, tk.copy); len(names) != 0 {
		t.Fatalf("after the merge was undone git reports %v changed, want nothing", names)
	}

	// An untracked directory holding a nested repository: git reports the
	// directory as one path, and an edit inside it is a change to what
	// would be lost.
	nested := filepath.Join(tk.copy, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, nested, "init", "--quiet", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(nested, "inner.txt"), []byte("inner work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := s.cleaner.Loss(ctx, entry)
	if err != nil || !before.Untracked {
		t.Fatalf("Loss with a nested repository = %+v, %v; want it untracked", before, err)
	}
	if names := changedPaths(t, tk.copy); !slices.Equal(names, []string{"nested/"}) {
		t.Fatalf("git reports %v, want the nested repository as the one directory nested/", names)
	}
	if err := os.WriteFile(filepath.Join(nested, "inner.txt"), []byte("inner work, edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := s.cleaner.Loss(ctx, entry)
	if err != nil || after.Digest == before.Digest {
		t.Fatalf("with a file inside the nested repository edited Loss = %+v, %v; want another digest than %s", after, err, before.Digest)
	}
	if _, err := s.cleaner.Discard(ctx, entry, before.Digest, ""); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on the digest shown before the nested edit = %v; want ErrLossChanged", err)
	}
	rawGit(t, nested, "add", "inner.txt")
	committed, err := s.cleaner.Loss(ctx, entry)
	if err != nil || committed.Digest == after.Digest {
		t.Fatalf("with the nested repository's own index moved Loss = %+v, %v; want another digest than %s", committed, err, after.Digest)
	}
}

// rawGitErr runs git directly in dir and returns what it printed and the
// error, for a command the test expects to fail.
func rawGitErr(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
