package tasks

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/principles"
)

// What a discard would lose is read from the entry's basis and the copy as
// it stands, with a digest that names exactly that state, contents
// included: the same copy digests the same twice, a file no commit records
// changes it, and removing the file brings the first digest back; an edit
// to a tracked file and a second untracked file change it while every name
// and indicator stays the same, and staging the edit, whose content the
// working tree holds as shown, leaves it as it was. Discard refuses a digest that is
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
	if _, err := s.cleaner.Discard(ctx, entry, first.Digest, first.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
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
	if _, err := s.cleaner.Discard(ctx, entry, "not-the-digest", nil, "", nil); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on another digest = %v; want ErrLossChanged", err)
	}
	// The contents, not the names: a dirty copy with an untracked file is
	// shown, then the tracked file is edited again and another file is
	// added, which keeps every name and both indicators the same; the
	// digest shown no longer names the copy, and a discard on it is refused.
	// Staging the edit loses nothing more, so it leaves the digest as it was.
	tracked := filepath.Join(tk.copy, "own.txt")
	committed, err := os.Lstat(tracked)
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err := s.cleaner.Discard(ctx, entry, dirty.Digest, dirty.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
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
	if _, err := s.cleaner.Discard(ctx, entry, dirty.Digest, dirty.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on the digest shown before the edits and the file = %v; want ErrLossChanged", err)
	}
	if _, err := os.Stat(filepath.Join(tk.copy, "second.txt")); err != nil {
		t.Fatalf("the refused discard removed the new work: %v", err)
	}
	rawGit(t, tk.copy, "add", "own.txt")
	staged, err := s.cleaner.Loss(ctx, entry)
	if err != nil || staged.Digest != added.Digest {
		t.Fatalf("with the edit staged Loss = %+v, %v; want the digest %s, since the working tree holds what was staged", staged, err, added.Digest)
	}
	rawGit(t, tk.copy, "reset", "--quiet", "own.txt")
	rawGit(t, tk.copy, "checkout", "--", "own.txt")
	// The checkout writes the file under the process's own mask, and the
	// mode is part of what a discard destroys, so it is put back too.
	if err := os.Chmod(tracked, committed.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(tk.copy, "second.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stray); err != nil {
		t.Fatal(err)
	}
	if back, err := s.cleaner.Loss(ctx, entry); err != nil || back.Digest != first.Digest {
		t.Fatalf("with everything undone the copy digests %s (%v), want %s again", back.Digest, err, first.Digest)
	}
	lost, err := s.cleaner.Discard(ctx, entry, first.Digest, first.Lacks, "", nil)
	if err != nil || lost.Digest != first.Digest {
		t.Fatalf("Discard = %+v, %v; want the copy removed on its own digest", lost, err)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the discarded copy still stands: %v", err)
	}
	if _, err := s.cleaner.Loss(ctx, entry); !errors.Is(err, ErrCopyGone) {
		t.Fatalf("Loss of a gone copy = %v; want ErrCopyGone", err)
	}
	if gone, err := s.cleaner.Discard(ctx, entry, first.Digest, first.Lacks, "", nil); err != nil || gone.Digest != "" {
		t.Fatalf("Discard of a gone copy = %+v, %v; want nothing to lose and no error", gone, err)
	}
}

// A discard's preview is read from the copy as it stands, not from the
// entry's record of it when it was kept: the copy is kept holding its own
// commit, and then a commit is made on another branch and HEAD returned to
// where it was, which the entry never recorded; the preview lists that
// commit beside the first as what the default branch lacks. A default
// branch that cannot be read now refuses the preview as unestablished,
// whatever the entry recorded. Without the read the preview shows the
// entry's record and the new commit is not among what would be lost.
func TestADiscardPreviewIsReadFromTheCopyNow(t *testing.T) {
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/now")
	own := commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit")
	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !slices.Equal(got.Entry.Lacks, []string{own}) {
		t.Fatalf("Keep = %+v, %v; want the entry lacking the task's own commit alone", got, err)
	}
	entry := got.Entry

	rawGit(t, tk.copy, "checkout", "--quiet", "-b", "aside")
	aside := commit(t, tk.copy, "aside.txt", "work on another branch\n", "work on another branch")
	rawGit(t, tk.copy, "checkout", "--quiet", "task/now")
	if head := rawGit(t, tk.copy, "rev-parse", "HEAD"); head != own {
		t.Fatalf("HEAD is %s, want it back at %s", head, own)
	}
	loss, err := s.cleaner.Loss(ctx, entry)
	if err != nil || !slices.Contains(loss.Lacks, aside) || !slices.Contains(loss.Lacks, own) {
		t.Fatalf("the preview = %+v, %v; want %s and %s among what would be lost", loss, err, aside, own)
	}

	if err := os.Rename(s.upstream, s.upstream+".gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cleaner.Loss(ctx, entry); !errors.Is(err, ErrLossUnestablished) {
		t.Fatalf("the preview with the upstream unreadable = %v; want ErrLossUnestablished", err)
	}
	if err := os.Rename(s.upstream+".gone", s.upstream); err != nil {
		t.Fatal(err)
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
	if _, err := s.cleaner.Discard(ctx, entry, conflicted.Digest, conflicted.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
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
	if _, err := s.cleaner.Discard(ctx, entry, before.Digest, before.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
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

// The digest is framed, so no byte string can stand for a boundary, and it
// carries every entry's type and permission bits. The reviewer's forgery,
// inside an untracked nested repository, which is walked entry by entry: a
// link a whose target is "x", a newline, and a line naming a file b with
// the hash of some payload, digests differently from a link a to "x" beside
// a file b holding that payload, and a discard on one refuses the other. A
// name holding a newline is a field of its own. And a chmod +x inside an
// untracked nested repository moves the digest. Without the framing the
// forged pair digests the same; without the mode bits the chmod does not
// move it.
func TestADiscardDigestIsFramedAndCarriesModes(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/framed")
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
	loss := func(when string) Loss {
		t.Helper()
		l, err := s.cleaner.Loss(ctx, entry)
		if err != nil {
			t.Fatalf("%s: Loss: %v", when, err)
		}
		return l
	}
	forge := filepath.Join(tk.copy, "forge")
	if err := os.MkdirAll(forge, 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, forge, "init", "--quiet", "-b", "main", ".")
	link := filepath.Join(forge, "a")
	file := filepath.Join(forge, "b")

	// The forged state: one link whose target carries a second line.
	payload := []byte("the payload\n")
	sum, err := hashFile(writeTemp(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("x\nb "+sum, link); err != nil {
		t.Fatal(err)
	}
	forged := loss("the forged link")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	// The real state: the link to x beside a file b holding the payload.
	if err := os.Symlink("x", link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	real := loss("the link beside the file")
	if forged.Digest == real.Digest {
		t.Fatalf("a link whose target forges a second entry digests %s, the same as the link beside the file", real.Digest)
	}
	if _, err := s.cleaner.Discard(ctx, entry, forged.Digest, forged.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on the forged state's digest = %v; want ErrLossChanged", err)
	}
	if err := os.RemoveAll(forge); err != nil {
		t.Fatal(err)
	}

	// A name holding a newline is one field.
	split := loss("before the newline names")
	if err := os.WriteFile(filepath.Join(tk.copy, "c\nd"), []byte("one file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	newline := loss("a name holding a newline")
	if err := os.Remove(filepath.Join(tk.copy, "c\nd")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tk.copy, "c"), []byte("one file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plain := loss("a plain name")
	if newline.Digest == plain.Digest || newline.Digest == split.Digest {
		t.Fatalf("a name holding a newline digests %s, which another state shares", newline.Digest)
	}
	if err := os.Remove(filepath.Join(tk.copy, "c")); err != nil {
		t.Fatal(err)
	}

	// The executable bit inside an untracked nested repository.
	nested := filepath.Join(tk.copy, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, nested, "init", "--quiet", "-b", "main", ".")
	script := filepath.Join(nested, "run.sh")
	if err := os.WriteFile(script, []byte("echo hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := loss("the nested script as written")
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}
	after := loss("the nested script made executable")
	if after.Digest == before.Digest {
		t.Fatalf("chmod +x inside the nested repository left the digest at %s", before.Digest)
	}
	if _, err := s.cleaner.Discard(ctx, entry, before.Digest, before.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on the digest shown before the chmod = %v; want ErrLossChanged", err)
	}
}

// writeTemp writes content to a file of its own outside any copy and
// returns its path.
func writeTemp(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "payload")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A discard's digest seals the copy and what it was shown as lost, never
// the default branch's tip, and the removal holds what would be lost then
// to what was shown. The copy is cloned after a commit M landed upstream
// and holds its own commit beside it. With an unrelated commit landed
// upstream since the preview, the copy digests as shown. A file added to
// the copy refuses the removal. The default branch rewound past M, which
// makes the copy the only place M is, refuses it naming M, which the
// discard was not shown as lost. With the upstream back, the copy is
// removed on the digest and the commits shown. Without the subset check
// the rewound upstream's M is destroyed unshown; with the tip sealed the
// unrelated commit refuses the removal for good.
func TestADiscardSealsTheCopyAndHoldsTheLossToWhatWasShown(t *testing.T) {
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	pusher := filepath.Join(t.TempDir(), "pusher")
	rawGit(t, filepath.Dir(pusher), "clone", "-q", s.upstream, pusher)
	base := rawGit(t, pusher, "rev-parse", "HEAD")
	landed := commit(t, pusher, "m.txt", "landed upstream\n", "a commit landed upstream")
	rawGit(t, pusher, "push", "-q", "origin", "HEAD:main")

	tk := s.deliveryTask(t, "task/sealed")
	own := commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit")
	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil {
		t.Fatal(err)
	}
	entry := got.Entry
	shown, err := s.cleaner.Loss(ctx, entry)
	if err != nil || !slices.Equal(shown.Lacks, []string{own}) {
		t.Fatalf("the preview = %+v, %v; want %s alone lost", shown, err, own)
	}

	unrelated := commit(t, pusher, "u.txt", "someone else's work\n", "someone else's work")
	rawGit(t, pusher, "push", "-q", "origin", "HEAD:main")
	if again, err := s.cleaner.Loss(ctx, entry); err != nil || again.Digest != shown.Digest {
		t.Fatalf("with an unrelated commit upstream the copy digests %+v, %v; want %s as shown", again, err, shown.Digest)
	}

	stray := filepath.Join(tk.copy, "stray.txt")
	if err := os.WriteFile(stray, []byte("not committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cleaner.Discard(ctx, entry, shown.Digest, shown.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard of a copy that changed = %v; want ErrLossChanged", err)
	}
	if err := os.Remove(stray); err != nil {
		t.Fatal(err)
	}

	rawGit(t, pusher, "push", "-q", "--force", "origin", base+":refs/heads/main")
	if _, err := s.cleaner.Discard(ctx, entry, shown.Digest, shown.Lacks, "", nil); !errors.Is(err, ErrLossChanged) || !strings.Contains(err.Error(), landed) {
		t.Fatalf("a discard with the default branch rewound past %s = %v; want ErrLossChanged naming it", landed, err)
	}
	if _, err := os.Stat(tk.copy); err != nil {
		t.Fatalf("a refused discard removed the copy: %v", err)
	}

	rawGit(t, pusher, "push", "-q", "--force", "origin", unrelated+":refs/heads/main")
	lost, err := s.cleaner.Discard(ctx, entry, shown.Digest, shown.Lacks, "", nil)
	if err != nil || !slices.Equal(lost.Lacks, []string{own}) {
		t.Fatalf("the discard with the upstream back = %+v, %v; want the copy removed losing %s", lost, err, own)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the discarded copy still stands: %v", err)
	}
}

// A discard's preview reads the default branch under references of its own
// and never deletes the cleanup decision's: a reference standing under the
// cleanup's namespace for the copy, as a cleanup running at once leaves one
// between its fetch and its forget, still stands after the preview, and so
// does the preview's answer. Without a namespace of its own the preview's
// forget deletes the cleanup's reference, and the cleanup's own forget can
// delete the one the preview is about to resolve, refusing the preview as
// unestablished.
func TestADiscardPreviewLeavesTheCleanupsReferencesAlone(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/apart")
	commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit")
	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil {
		t.Fatal(err)
	}
	cleanupRef := "refs/assistant/cleanup/task/" + tk.id + "/" + keptDefaultRef
	base := rawGit(t, tk.copy, "rev-parse", "HEAD~1")
	rawGit(t, tk.copy, "update-ref", cleanupRef, base)
	if _, err := s.cleaner.Loss(ctx, got.Entry); err != nil {
		t.Fatalf("the preview = %v", err)
	}
	if at, err := rawGitErr(tk.copy, "rev-parse", "--verify", "--quiet", cleanupRef); err != nil || strings.TrimSpace(at) != base {
		t.Fatalf("after the preview the cleanup's reference reads %q, %v; want it standing at %s", at, err, base)
	}
	if left := rawGit(t, tk.copy, "for-each-ref", "refs/assistant/discard/"); left != "" {
		t.Fatalf("the preview left its references behind:\n%s", left)
	}
}

// The digest covers everything the removal of a task's clone deletes, not
// only what git reports changed: from a digest shown, each of these moves
// it and refuses the digest shown before it, with HEAD, every branch and
// every indicator the same: the stash list gaining an entry under a tip
// stored back where it was (the reviewer's sequence); a commit reset away,
// which only HEAD's and the branch's reflogs still name, and both of these
// add what they hold to the commits shown lacking, since the removal
// destroys it and nothing else holds it; a file git is told to ignore,
// created and then edited; a hook written into the repository directory;
// and a remote added to its configuration. Two reads of the copy in
// between, each of which fetches the default branch into it, digest the
// same. Without the reflogs, the ignored file or the repository directory
// in the digest, the stale digest removes the clone.
func TestADiscardDigestCoversEverythingTheRemovalDeletes(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/whole")
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
	shown := func(when string) Loss {
		t.Helper()
		l, err := s.cleaner.Loss(ctx, entry)
		if err != nil {
			t.Fatalf("%s: Loss: %v", when, err)
		}
		if again, err := s.cleaner.Loss(ctx, entry); err != nil || again.Digest != l.Digest {
			t.Fatalf("%s: read again the copy digests %s (%v), want %s", when, again.Digest, err, l.Digest)
		}
		return l
	}
	// moves makes the change and holds the loss to it: another digest, the
	// same HEAD and indicators, and the same commits lost, or, when the
	// change commits what only a stash or a reflog names (gains), more.
	moves := func(when string, before Loss, gains bool, change func()) Loss {
		t.Helper()
		change()
		after := shown(when)
		if after.Digest == before.Digest || after.Head != before.Head || after.Dirty != before.Dirty || after.Untracked != before.Untracked {
			t.Fatalf("%s: Loss = %+v after %+v; want another digest and the same HEAD and indicators", when, after, before)
		}
		grew := len(after.Lacks) > len(before.Lacks) && !slices.ContainsFunc(before.Lacks, func(c string) bool { return !slices.Contains(after.Lacks, c) })
		if gains && !grew || !gains && !slices.Equal(after.Lacks, before.Lacks) {
			t.Fatalf("%s: the commits lost read %v after %v; want them to grow: %v", when, after.Lacks, before.Lacks, gains)
		}
		if _, err := s.cleaner.Discard(ctx, entry, before.Digest, before.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
			t.Fatalf("%s: a discard on the digest shown before = %v; want ErrLossChanged", when, err)
		}
		if _, err := os.Stat(tk.copy); err != nil {
			t.Fatalf("%s: the refused discard removed the copy: %v", when, err)
		}
		return after
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tracked := filepath.Join(tk.copy, "own.txt")
	write(tracked, "stashed first\n")
	rawGit(t, tk.copy, "stash", "--quiet")
	saved := rawGit(t, tk.copy, "rev-parse", "refs/stash")
	l := shown("one stash")
	l = moves("new work stashed under the saved tip stored back", l, true, func() {
		write(tracked, "stashed after the discard was shown\n")
		rawGit(t, tk.copy, "stash", "--quiet")
		rawGit(t, tk.copy, "stash", "store", "--quiet", "-m", "stored back", saved)
	})
	moves("a commit reset away", l, true, func() {
		// The files the commit and the reset write beside the reflogs are
		// put back as they were, so the reflogs alone carry the change.
		gitDir := filepath.Join(tk.copy, ".git")
		beside := map[string][]byte{}
		for _, name := range []string{"ORIG_HEAD", "COMMIT_EDITMSG"} {
			raw, err := os.ReadFile(filepath.Join(gitDir, name))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			beside[name] = raw
		}
		commit(t, tk.copy, "away.txt", "reset away\n", "a commit only the reflogs name")
		rawGit(t, tk.copy, "reset", "--quiet", "--hard", "HEAD~1")
		for name, raw := range beside {
			if raw == nil {
				if err := os.Remove(filepath.Join(gitDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(gitDir, name), raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	})
	write(filepath.Join(tk.copy, ".git", "info", "exclude"), "secret.env\n")
	l = shown("an ignored name")
	l = moves("an ignored file created", l, false, func() { write(filepath.Join(tk.copy, "secret.env"), "TOKEN=one\n") })
	l = moves("an ignored file edited", l, false, func() { write(filepath.Join(tk.copy, "secret.env"), "TOKEN=two\n") })
	l = moves("a hook written", l, false, func() { write(filepath.Join(tk.copy, ".git", "hooks", "pre-push"), "#!/bin/sh\nexit 0\n") })
	moves("a remote added", l, false, func() { rawGit(t, tk.copy, "remote", "add", "elsewhere", s.upstream) })
}

// Loss names every path the removal of a task's clone destroys whose
// content nothing else holds, decided from the copy and not from what git
// reports: a clone kept with nothing but committed work names no path of
// its working tree, and once it holds them it names a tracked file edited
// (changed), a tracked file made executable (changed), a file no commit
// holds (new), a directory the copy's exclude list makes git ignore, once
// and whole (new, nothing under it again), content staged and then edited
// (new for the file, staged for the index), a hook written into the
// repository directory and the edited exclude list (repository), and none
// of the sample hooks git writes into every repository. The digest seals
// the list: with git's template emptied, the same copy would be shown
// naming every sample hook, and the digest shown is refused; with it back,
// the digest shown removes the clone.
func TestALossNamesEveryPathItDestroysThatNothingElseHolds(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/paths")
	commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit")
	commit(t, tk.copy, "tool.sh", "echo tool\n", "a script")
	if err := os.MkdirAll(filepath.Join(tk.copy, "gone", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	commit(t, tk.copy, "gone/a.txt", "a\n", "a directory")
	commit(t, tk.copy, "gone/deep/b.txt", "b\n", "deeper in it")
	commit(t, tk.copy, "unstaged.txt", "deleted, not staged\n", "a file")
	commit(t, tk.copy, "uncached.txt", "deleted from the index alone\n", "another")
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
	ofWorktree := func(l Loss) []LostPath {
		return slices.DeleteFunc(slices.Clone(l.Files), func(f LostPath) bool { return f.Kind == LostRepository })
	}
	clean, err := s.cleaner.Loss(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	if paths := ofWorktree(clean); len(paths) != 0 {
		t.Fatalf("a clone holding committed work alone names %+v; want no path of its working tree", paths)
	}
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(tk.copy, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("own.txt", "edited since\n")
	if err := os.Chmod(filepath.Join(tk.copy, "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	write("notes.txt", "a file no commit holds\n")
	write(".git/info/exclude", "build/\n")
	write("build/deep/a.txt", "ignored\n")
	write("build/b.txt", "ignored too\n")
	write("staged.txt", "staged first\n")
	rawGit(t, tk.copy, "add", "staged.txt")
	write("staged.txt", "edited after it was staged\n")
	write(".git/hooks/pre-push", "#!/bin/sh\nexit 0\n")
	// Deletions: a directory, a file not staged, and a file deleted from
	// the index alone, whose working-tree file still holds HEAD's content.
	if err := os.RemoveAll(filepath.Join(tk.copy, "gone")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(tk.copy, "unstaged.txt")); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "rm", "--quiet", "--cached", "uncached.txt")

	l, err := s.cleaner.Loss(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	want := []LostPath{
		{LostChanged, "own.txt"}, {LostChanged, "tool.sh"},
		{LostNew, "build/"}, {LostNew, "notes.txt"}, {LostNew, "staged.txt"},
		{LostStaged, "staged.txt"},
		{LostDeleted, "gone/"}, {LostDeleted, "uncached.txt"}, {LostDeleted, "unstaged.txt"},
	}
	if paths := ofWorktree(l); !slices.Equal(paths, want) {
		t.Fatalf("Loss names %+v; want %+v", paths, want)
	}
	for _, f := range []LostPath{{LostRepository, "hooks/pre-push"}, {LostRepository, "info/exclude"}} {
		if !slices.Contains(l.Files, f) {
			t.Errorf("Loss names %+v; want %+v among them", l.Files, f)
		}
	}
	if slices.ContainsFunc(l.Files, func(f LostPath) bool { return strings.HasSuffix(f.Path, ".sample") }) {
		t.Errorf("Loss names %+v; want none of git's sample hooks", l.Files)
	}
	// What git writes into a new repository changes under the same copy:
	// an empty template, so every sample hook the copy holds would be named.
	cfg := os.Getenv("GIT_CONFIG_GLOBAL")
	original, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	empty := filepath.ToSlash(t.TempDir())
	if err := os.WriteFile(cfg, append(slices.Clone(original), []byte("[init]\n\ttemplateDir = "+empty+"\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cleaner.Discard(ctx, entry, l.Digest, l.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("Discard once the list shown would name more = %v; want ErrLossChanged", err)
	}
	if err := os.WriteFile(cfg, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cleaner.Discard(ctx, entry, l.Digest, l.Lacks, "", nil); err != nil {
		t.Fatalf("Discard on the digest shown: %v", err)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the discarded clone still stands: %v", err)
	}
}

// Anything written into the copy after Discard's last read, the one the
// digest is checked on, refuses the discard and is left as it was written,
// the final full review of 74949eb, finding 1: a file git ignores is
// written (the reviewer's sequence), a tracked file is edited, a commit is
// made, or a file is deleted, at two moments. Between that read and the
// check of the copy's state, the check refuses with ErrLossChanged and
// nothing is removed. After the check, immediately before the removal,
// where Discard's callback runs, the removal itself refuses on reaching
// the change, which stands as written, and what it removed before that
// was each entry as it was read, and so shown; a deletion there destroys
// nothing more and goes ahead, and the loss Discard returns is the one
// shown: it names a file the loss showed as lost though another process
// deleted it, and not a clean tracked file it never showed. A cleanup keeping the copy at the moment
// finds the entry already standing, so nothing but the refusal could have
// kept the change. The positive control is the same discard with nothing
// written, which removes the clone. Before the removal was held to the
// copy as read, every one of these was destroyed with the clone and the
// discard reported done.
func TestAnythingWrittenAfterADiscardsLastReadRefusesIt(t *testing.T) {
	principles.Cite(t, principles.P6)
	type change struct {
		name  string
		write func(t *testing.T, copyPath string)
		// stands fails t unless what write did is still in the copy.
		stands func(t *testing.T, copyPath string)
	}
	holds := func(name, want string) func(t *testing.T, copyPath string) {
		return func(t *testing.T, copyPath string) {
			t.Helper()
			if raw, err := os.ReadFile(filepath.Join(copyPath, name)); err != nil || string(raw) != want {
				t.Errorf("%s reads %q, %v after the refusal; want %q", name, raw, err, want)
			}
		}
	}
	var late string
	changes := []change{
		{name: "a file git ignores", write: func(t *testing.T, copyPath string) {
			if err := os.WriteFile(filepath.Join(copyPath, "secret.txt"), []byte("written inside the window\n"), 0o600); err != nil {
				t.Error(err)
			}
		}, stands: holds("secret.txt", "written inside the window\n")},
		{name: "a tracked file edited", write: func(t *testing.T, copyPath string) {
			if err := os.WriteFile(filepath.Join(copyPath, "own.txt"), []byte("edited inside the window\n"), 0o600); err != nil {
				t.Error(err)
			}
		}, stands: holds("own.txt", "edited inside the window\n")},
		{name: "a commit", write: func(t *testing.T, copyPath string) {
			late = strings.TrimSpace(commit(t, copyPath, "late.txt", "committed inside the window\n", "a commit inside the window"))
		}, stands: func(t *testing.T, copyPath string) {
			t.Helper()
			if head := strings.TrimSpace(rawGit(t, copyPath, "rev-parse", "HEAD")); head != late {
				t.Errorf("HEAD reads %s after the refusal; want the commit made inside the window, %s", head, late)
			}
		}},
		{name: "a tracked file deleted", write: func(t *testing.T, copyPath string) {
			if err := os.Remove(filepath.Join(copyPath, "own.txt")); err != nil {
				t.Error(err)
			}
		}, stands: func(t *testing.T, copyPath string) {
			t.Helper()
			if _, err := os.Lstat(filepath.Join(copyPath, "own.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("own.txt stands after the refusal: %v; want it deleted as it was", err)
			}
		}},
		{name: "a file shown as lost deleted", write: func(t *testing.T, copyPath string) {
			if err := os.Remove(filepath.Join(copyPath, "scratch.txt")); err != nil {
				t.Error(err)
			}
		}, stands: func(t *testing.T, copyPath string) {
			t.Helper()
			if _, err := os.Lstat(filepath.Join(copyPath, "scratch.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("scratch.txt stands after the refusal: %v; want it deleted as it was", err)
			}
		}},
		{name: "nothing, the positive control"},
	}
	for _, moment := range []string{"before the check", "before the removal"} {
		for _, c := range changes {
			t.Run(moment+"/"+c.name, func(t *testing.T) {
				s := newCleanupSubject(t, forgetest.Script{})
				ctx := context.Background()
				tk := s.deliveryTask(t, "task/window")
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
				if err := os.MkdirAll(filepath.Join(tk.copy, ".git", "info"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(tk.copy, ".git", "info", "exclude"), []byte("secret.txt\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				// A file no commit records, which the loss shows as lost.
				if err := os.WriteFile(filepath.Join(tk.copy, "scratch.txt"), []byte("not committed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				shown, err := s.cleaner.Loss(ctx, got.Entry)
				if err != nil {
					t.Fatal(err)
				}
				named := func(l Loss, path string) bool {
					return slices.ContainsFunc(l.Files, func(f LostPath) bool { return f.Path == path })
				}
				if !named(shown, "scratch.txt") || named(shown, "own.txt") {
					t.Fatalf("the loss shown names %+v; want scratch.txt and not own.txt, which HEAD holds", shown.Files)
				}
				var inside KeptEntry
				var after copyState
				window := func() {
					if c.write != nil {
						c.write(t, tk.copy)
					}
					if inside, err = s.cleaner.Keep(ctx, kept); err != nil {
						t.Error(err)
					}
					if after, err = readCopyState(tk.copy); err != nil {
						t.Error(err)
					}
				}
				var callback func() error
				if moment == "before the check" {
					s.cleaner.betweenDiscardReadAndCheck = window
				} else {
					callback = func() error { window(); return nil }
				}
				lost, err := s.cleaner.Discard(ctx, got.Entry, shown.Digest, shown.Lacks, "", callback)
				deletion := c.name == "a tracked file deleted" || c.name == "a file shown as lost deleted"
				if c.write == nil || (moment == "before the removal" && deletion) {
					if err != nil {
						t.Fatalf("Discard on the digest shown, %s %s: %v", c.name, moment, err)
					}
					if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("the clone still stands: %v", err)
					}
					// The loss returned is the one shown, whatever another
					// process removed after the check: it names scratch.txt,
					// which the loss showed, deleted or not, and not own.txt,
					// which it never showed, deleted or not.
					if lost.Digest != shown.Digest || !named(lost, "scratch.txt") || named(lost, "own.txt") {
						t.Fatalf("Discard returned the loss %s naming %+v; want the one shown, %s, naming scratch.txt and not own.txt",
							lost.Digest, lost.Files, shown.Digest)
					}
					return
				}
				if !errors.Is(err, ErrLossChanged) {
					t.Fatalf("Discard on the digest shown, %s %s = %+v, %v; want ErrLossChanged", c.name, moment, lost, err)
				}
				c.stands(t, tk.copy)
				if moment == "before the check" {
					now, err := readCopyState(tk.copy)
					if err != nil {
						t.Fatal(err)
					}
					if changed := after.changedSince(now, func(string) bool { return false }); len(changed) > 0 {
						t.Errorf("the discard refused at its check changed the copy: %v", changed)
					}
				}
				if inside.Entry.ID != got.Entry.ID && c.name != "a commit" {
					t.Fatalf("a cleanup inside the window kept %+v; want entry %d, the one standing", inside.Entry, got.Entry.ID)
				}
			})
		}
	}
}

// What Discard's own read of the loss adds to the copy is not a change it
// refuses: the upstream's main moves on between the discard shown and the
// discard confirmed, so the confirmation's read fetches the commit main
// stands on into the copy by identifier, which adds objects and nothing
// else, and the discard on the digest shown removes the clone. The read
// is checked to have added objects, so the test is of that allowance.
// Without objects counted as the read's own, every discard confirmed after
// the upstream moved is refused.
func TestADiscardTakesTheObjectsItsOwnReadFetches(t *testing.T) {
	principles.Cite(t, principles.P15)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/fetched")
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
	shown, err := s.cleaner.Loss(ctx, got.Entry)
	if err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	rawGit(t, filepath.Dir(seed), "clone", "-q", s.upstream, seed)
	moved := strings.TrimSpace(commit(t, seed, "upstream.txt", "the upstream's next work\n", "the upstream moved on"))
	rawGit(t, seed, "push", "-q", "origin", "main")
	if _, err := rawGitErr(tk.copy, "cat-file", "-e", moved); err == nil {
		t.Fatalf("the copy holds %s before the discard; want its read to be what fetches it", moved)
	}
	before, err := readCopyState(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	var added []string
	s.cleaner.betweenDiscardReadAndCheck = func() {
		now, err := readCopyState(tk.copy)
		if err != nil {
			t.Error(err)
			return
		}
		added = before.changedSince(now, func(string) bool { return false })
	}
	if _, err := s.cleaner.Discard(ctx, got.Entry, shown.Digest, shown.Lacks, "", nil); err != nil {
		t.Fatalf("Discard on the digest shown, after the upstream moved: %v", err)
	}
	if len(added) == 0 {
		t.Fatal("the discard's read added nothing to the copy; want the objects of the commit it fetched")
	}
	objects := filepath.Join(tk.copy, ".git", "objects") + string(filepath.Separator)
	for _, change := range added {
		if !strings.HasPrefix(change, objects) || !strings.HasSuffix(change, " was added") {
			t.Fatalf("the discard's read changed %s; want only objects added", change)
		}
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the clone still stands: %v", err)
	}
}

// Loss reads the copy's object graph as the objects record it, never
// through a replacement reference, the final full review of c5c581c: in a
// kept clone the task commits B, which holds work nothing else does, and
// then U, which takes that work out of the tree and adds work of its own,
// so the copy is kept; a replacement reference
// or the clone's graft file grafts U onto the commit B was made on, and
// the reflogs are expired, so
// nothing but U's own parent names B. Loss lists B as lost as well as U,
// and a discard on that digest is the one that removes the clone. Read
// through the replacement, B was hidden: the loss listed U alone and a
// discard on its digest destroyed B's work unshown.
// internal/vcs's TestAHandleWithoutReplaceObjectsReadsTheGraphAsRecorded is
// the positive control, the replacement hiding B from a walk that honours it.
func TestALossReadsThroughNoReplacementReference(t *testing.T) {
	principles.Cite(t, principles.P6)
	for _, how := range []string{"a replacement reference", "a graft file"} {
		t.Run(how, func(t *testing.T) {
			s := newCleanupSubject(t, forgetest.Script{})
			ctx := context.Background()
			tk := s.deliveryTask(t, "task/replaced")
			base := strings.TrimSpace(rawGit(t, tk.copy, "rev-parse", "HEAD"))
			b := strings.TrimSpace(commit(t, tk.copy, "unique.txt", "what only B holds\n", "B, the task's unique work"))
			rawGit(t, tk.copy, "rm", "-q", "unique.txt")
			u := strings.TrimSpace(commit(t, tk.copy, "tip.txt", "U's own work\n", "U, taking B's work out and adding its own"))
			graft(t, tk.copy, how, u, base)
			rawGit(t, tk.copy, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
			if log := rawGit(t, tk.copy, "reflog", "--format=%H", "--all"); strings.Contains(log, b) {
				t.Fatalf("a reflog still names B:\n%s", log)
			}
			_, err := s.cleanUp(t, tk.id)
			kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
			if !ok {
				t.Fatal("an unproven refusal is not kept work")
			}
			got, err := s.cleaner.Keep(ctx, kept)
			if err != nil || !got.Entry.Established() {
				t.Fatalf("Keep = %+v, %v", got, err)
			}
			l, err := s.cleaner.Loss(ctx, got.Entry)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(l.Lacks, b) || !slices.Contains(l.Lacks, u) {
				t.Fatalf("Loss lacks %v; want B %s and U %s, B read through U's own parent", l.Lacks, b, u)
			}
			if _, err := s.cleaner.Discard(ctx, got.Entry, l.Digest, l.Lacks, "", nil); err != nil {
				t.Fatalf("Discard on the digest shown: %v", err)
			}
		})
	}
}

// graft sets parent as commit's only parent in the clone at copyPath, by a
// replacement reference or by the clone's graft file, as how names.
func graft(t *testing.T, copyPath, how, commit, parent string) {
	t.Helper()
	if how == "a graft file" {
		if err := os.MkdirAll(filepath.Join(copyPath, ".git", "info"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(copyPath, ".git", "info", "grafts"), []byte(commit+" "+parent+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	rawGit(t, copyPath, "replace", "--graft", commit, parent)
}

// A cleanup proof reads the copy as its objects record it too: in a clone
// the task commits work the default branch lacks, and a replacement
// reference, or the clone's graft file, grafts the default branch's tip
// onto that commit,
// so read through it the default branch would already hold the task's
// work. The cleanup refuses as unproven and keeps the copy. Read through
// the replacement, the default-branch proof found the work landed and the
// copy was removed with it.
func TestACleanupProofReadsThroughNoReplacementReference(t *testing.T) {
	principles.Cite(t, principles.P6)
	for _, how := range []string{"a replacement reference", "a graft file"} {
		t.Run(how, func(t *testing.T) {
			s := newCleanupSubject(t, forgetest.Script{})
			tk := s.deliveryTask(t, "task/replaced-main")
			main := strings.TrimSpace(rawGit(t, tk.copy, "rev-parse", "HEAD"))
			own := strings.TrimSpace(commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit"))
			graft(t, tk.copy, how, main, own)
			_, err := s.cleanUp(t, tk.id)
			refusedWith(t, err, ReasonUnproven)
			if _, err := os.Stat(filepath.Join(tk.copy, "own.txt")); err != nil {
				t.Fatalf("the copy's work is gone after the cleanup: %v", err)
			}
		})
	}
}

// Loss lists every commit the removal destroys that the default branch
// lacks, whatever names it, and every object a name points at that is not
// a commit, the reviewer's sequence on #475 and its kin: in a kept clone a
// commit is made and reset away, so only the reflogs name it; two stashes
// are made, so the older is named only by the stash's reflog; an annotated
// tag is put on HEAD; and a reference is pointed at a blob. The commits
// lost name the commit reset away and both stashes' commits, and the
// objects name the tag object and the blob, each with the names that
// point at it. The digest shown removes the clone. Without the reflogs
// among the names, the commit reset away and the older stash are destroyed
// unshown.
func TestALossListsWhatOnlyAReflogOrANameHolds(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/names")
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
	head := strings.TrimSpace(rawGit(t, tk.copy, "rev-parse", "HEAD"))
	commit(t, tk.copy, "away.txt", "reset away\n", "a commit only the reflogs name")
	away := strings.TrimSpace(rawGit(t, tk.copy, "rev-parse", "HEAD"))
	rawGit(t, tk.copy, "reset", "--quiet", "--hard", head)
	tracked := filepath.Join(tk.copy, "own.txt")
	if err := os.WriteFile(tracked, []byte("stashed first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "stash", "--quiet")
	older := strings.TrimSpace(rawGit(t, tk.copy, "rev-parse", "refs/stash"))
	if err := os.WriteFile(tracked, []byte("stashed second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "stash", "--quiet")
	rawGit(t, tk.copy, "tag", "--annotate", "--message", "a note on the work", "noted", "HEAD")
	tag := strings.TrimSpace(rawGit(t, tk.copy, "rev-parse", "refs/tags/noted"))
	blob := strings.TrimSpace(rawGit(t, tk.copy, "hash-object", "-w", "own.txt"))
	rawGit(t, tk.copy, "update-ref", "refs/aside/blob", blob)

	l, err := s.cleaner.Loss(ctx, got.Entry)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{head, away, older} {
		if !slices.Contains(l.Lacks, c) {
			t.Errorf("Loss lists the commits lost as %v; want %s among them", l.Lacks, c)
		}
	}
	want := []LostObject{{Object: tag, Type: "tag", Names: []string{"refs/tags/noted"}}, {Object: blob, Type: "blob", Names: []string{"refs/aside/blob"}}}
	for _, w := range want {
		if !slices.ContainsFunc(l.Objects, func(o LostObject) bool {
			return o.Object == w.Object && o.Type == w.Type && slices.Equal(o.Names, w.Names)
		}) {
			t.Errorf("Loss lists the objects lost as %+v; want %+v among them", l.Objects, w)
		}
	}
	if len(l.Objects) != len(want) {
		t.Errorf("Loss lists the objects lost as %+v; want exactly %+v", l.Objects, want)
	}
	if _, err := s.cleaner.Discard(ctx, got.Entry, l.Digest, l.Lacks, "", nil); err != nil {
		t.Fatalf("Discard on the digest shown: %v", err)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the discarded clone still stands: %v", err)
	}
}

// Loss lists a commit that only a reflog entry's previous value records,
// and one only a root reference names: in a kept clone a commit is made and
// reset away, then another is made and reset away, and the entries
// recording the first as their new value are deleted from the middle of
// HEAD's reflog and the branch's, so the first is recorded only as the
// previous value of the reset after it, which no ref@{n} resolves to and
// no other commit reaches; and a commit made with commit-tree, which writes no reflog,
// is named by ORIG_HEAD alone. Both are among the commits lost, and the
// digest shown removes the clone. Without git's walk of the reflogs the
// first is destroyed unshown, and without the root references the second.
func TestALossListsWhatOnlyAPreviousValueOrARootReferenceHolds(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/previous")
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
	head := strings.TrimSpace(rawGit(t, tk.copy, "rev-parse", "HEAD"))
	commit(t, tk.copy, "gap.txt", "recorded mid-log\n", "a commit only a previous value records")
	gap := strings.TrimSpace(rawGit(t, tk.copy, "rev-parse", "HEAD"))
	rawGit(t, tk.copy, "reset", "--quiet", "--hard", head)
	commit(t, tk.copy, "next.txt", "beside it\n", "a commit made after it on the task's own")
	rawGit(t, tk.copy, "reset", "--quiet", "--hard", head)
	// In HEAD's reflog and the branch's alike, @{0} is the last reset, @{1}
	// the commit beside, @{2} the first reset, whose previous value is the
	// gap's commit, and @{3} the gap's commit, which goes.
	branch := strings.TrimSpace(rawGit(t, tk.copy, "symbolic-ref", "HEAD"))
	rawGit(t, tk.copy, "reflog", "delete", "HEAD@{3}")
	rawGit(t, tk.copy, "reflog", "delete", branch+"@{3}")
	if entries := rawGit(t, tk.copy, "log", "--walk-reflogs", "--format=%H", "--all"); strings.Contains(entries, gap) {
		t.Fatalf("a reflog still names %s as an entry's new value:\n%s", gap, entries)
	}
	orig := strings.TrimSpace(rawGit(t, tk.copy, "commit-tree", "-p", head, "-m", "only ORIG_HEAD names it", "HEAD^{tree}"))
	rawGit(t, tk.copy, "update-ref", "--no-deref", "ORIG_HEAD", orig)

	l, err := s.cleaner.Loss(ctx, got.Entry)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{gap, orig} {
		if !slices.Contains(l.Lacks, c) {
			t.Errorf("Loss lists the commits lost as %v; want %s among them", l.Lacks, c)
		}
	}
	if _, err := s.cleaner.Discard(ctx, got.Entry, l.Digest, l.Lacks, "", nil); err != nil {
		t.Fatalf("Discard on the digest shown: %v", err)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the discarded clone still stands: %v", err)
	}
}

// A discard's digest seals exactly what it shows: HEAD, the commits lost,
// the objects lost with their names, each path lost by what it holds, and
// the indicators. A kept clone is shown losing two commits of its own, one
// on a branch beside, one whose tree is HEAD's, an annotated tag, a tracked
// file edited, a file and a directory no commit holds, content staged and
// then edited, a tracked file deleted and a hook. Then, from the digest
// shown, nothing it shows changes and the digest holds: a branch made at
// HEAD, the references packed, a tracked file rewritten with the same bytes
// and its mode's other bits changed with the index refreshed over it, and
// the object database repacked. Then each thing shown changes in turn and
// the digest before it is refused: HEAD moved to the commit holding its
// tree, the tag given another name, a commit shown dropped from the copy
// (refused naming it), and each path's content. The last digest removes
// the clone. Sealing the whole copy refuses the first set; sealing any less
// than what is shown lets one of the second through.
func TestADiscardSealsExactlyWhatItShows(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/exact")
	commit(t, tk.copy, "same.txt", "unchanged\n", "a file the copy keeps as committed")
	commit(t, tk.copy, "deleted.txt", "deleted later\n", "a file the copy deletes later")
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
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	at := func(rel string) string { return filepath.Join(tk.copy, filepath.FromSlash(rel)) }
	beside := strings.TrimSpace(rawGit(t, tk.copy, "commit-tree", "-p", "HEAD", "-m", "beside", "HEAD^{tree}"))
	rawGit(t, tk.copy, "update-ref", "refs/heads/beside", beside)
	twin := strings.TrimSpace(rawGit(t, tk.copy, "commit-tree", "-p", "HEAD", "-m", "HEAD's tree again", "HEAD^{tree}"))
	rawGit(t, tk.copy, "update-ref", "refs/heads/twin", twin)
	rawGit(t, tk.copy, "tag", "--annotate", "--message", "a note", "noted", "HEAD")
	tag := strings.TrimSpace(rawGit(t, tk.copy, "rev-parse", "refs/tags/noted"))
	write(at("own.txt"), "edited\n")
	write(at("note.txt"), "a new file\n")
	write(at("scratch/a.txt"), "inside a new directory\n")
	write(at("staged.txt"), "staged\n")
	rawGit(t, tk.copy, "add", "staged.txt")
	write(at("staged.txt"), "staged, then edited\n")
	if err := os.Remove(at("deleted.txt")); err != nil {
		t.Fatal(err)
	}
	write(at(".git/hooks/pre-push"), "#!/bin/sh\nexit 0\n")

	loss := func(when string) Loss {
		t.Helper()
		l, err := s.cleaner.Loss(ctx, entry)
		if err != nil {
			t.Fatalf("%s: Loss: %v", when, err)
		}
		return l
	}
	shown := loss("shown")
	for _, c := range []string{beside, twin} {
		if !slices.Contains(shown.Lacks, c) {
			t.Fatalf("the commits shown lost are %v; want %s among them", shown.Lacks, c)
		}
	}
	wantFiles := []LostPath{{LostChanged, "own.txt"}, {LostNew, "note.txt"}, {LostNew, "scratch/"}, {LostNew, "staged.txt"},
		{LostStaged, "staged.txt"}, {LostDeleted, "deleted.txt"}, {LostRepository, "hooks/pre-push"}}
	for _, w := range wantFiles {
		if !slices.Contains(shown.Files, w) {
			t.Fatalf("the paths shown lost are %+v; want %+v among them", shown.Files, w)
		}
	}
	if !slices.ContainsFunc(shown.Objects, func(o LostObject) bool { return o.Object == tag }) {
		t.Fatalf("the objects shown lost are %+v; want the tag %s", shown.Objects, tag)
	}

	// Nothing shown changes.
	rawGit(t, tk.copy, "branch", "made-at-head")
	rawGit(t, tk.copy, "pack-refs", "--all")
	write(at("same.txt"), "unchanged\n")
	if err := os.Chmod(at("same.txt"), 0o640); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "status", "--porcelain")
	rawGit(t, tk.copy, "repack", "-a", "-d", "-n", "-q")
	if still := loss("nothing shown changed"); still.Digest != shown.Digest {
		t.Fatalf("with nothing shown changed the copy digests %s, not %s as shown:\n%+v\n%+v", still.Digest, shown.Digest, still, shown)
	}

	// Each thing shown changes.
	before := shown
	refused := func(when string, change func()) {
		t.Helper()
		change()
		if _, err := s.cleaner.Discard(ctx, entry, before.Digest, before.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
			t.Fatalf("%s: a discard on the digest shown before = %v; want ErrLossChanged", when, err)
		}
		if _, err := os.Stat(tk.copy); err != nil {
			t.Fatalf("%s: the refused discard removed the copy: %v", when, err)
		}
		before = loss(when)
	}
	refused("HEAD moved to a commit holding its tree", func() {
		// The branch HEAD is on moves, so the repository's HEAD file, a
		// path shown, still names the branch, and no ORIG_HEAD is written.
		rawGit(t, tk.copy, "update-ref", "refs/heads/task/exact", twin)
		if l := loss("HEAD moved"); l.Head != twin || !slices.Equal(l.Files, before.Files) ||
			!slices.Equal(slices.Sorted(slices.Values(l.Lacks)), slices.Sorted(slices.Values(before.Lacks))) {
			t.Fatalf("with HEAD moved Loss = %+v; want HEAD on %s and the commits and paths as before", l, twin)
		}
	})
	refused("the tag given another name", func() { rawGit(t, tk.copy, "update-ref", "refs/aside/noted", tag) })
	refused("a commit shown dropped from the copy", func() {
		rawGit(t, tk.copy, "branch", "--quiet", "-D", "beside")
		if _, err := s.cleaner.Discard(ctx, entry, before.Digest, before.Lacks, "", nil); !errors.Is(err, ErrLossChanged) ||
			!strings.Contains(err.Error(), beside) {
			t.Fatalf("a discard with %s dropped = %v; want ErrLossChanged naming it", beside, err)
		}
	})
	refused("a tracked file edited again", func() { write(at("own.txt"), "edited twice\n") })
	refused("a new file edited", func() { write(at("note.txt"), "a new file, edited\n") })
	refused("a file inside a new directory edited", func() { write(at("scratch/a.txt"), "edited inside\n") })
	refused("other content staged", func() {
		write(at("staged.txt"), "staged again\n")
		rawGit(t, tk.copy, "add", "staged.txt")
		write(at("staged.txt"), "staged, then edited\n")
	})
	refused("a deletion undone", func() { write(at("deleted.txt"), "deleted later\n") })
	refused("a hook edited", func() { write(at(".git/hooks/pre-push"), "#!/bin/sh\nexit 1\n") })

	if _, err := s.cleaner.Discard(ctx, entry, before.Digest, before.Lacks, "", nil); err != nil {
		t.Fatalf("Discard on the last digest shown: %v", err)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the discarded clone still stands: %v", err)
	}
}

// The indicators a discard shows are sealed with it: a kept clone with one
// tracked file edited is shown dirty, then the file is marked
// skip-worktree, so git reports the copy clean while the path lost and what
// it holds are as shown; the digest shown before is refused. Without the
// indicators in the seal, the confirmation of a dirty copy removes one
// shown clean.
func TestADiscardSealsTheIndicatorsItShows(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/indicated")
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
	if err := os.WriteFile(filepath.Join(tk.copy, "own.txt"), []byte("edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty, err := s.cleaner.Loss(ctx, got.Entry)
	if err != nil || !dirty.Dirty {
		t.Fatalf("Loss = %+v, %v; want it dirty", dirty, err)
	}
	rawGit(t, tk.copy, "update-index", "--skip-worktree", "own.txt")
	clean, err := s.cleaner.Loss(ctx, got.Entry)
	if err != nil || clean.Dirty || !slices.Equal(clean.Files, dirty.Files) || !slices.Equal(clean.Lacks, dirty.Lacks) {
		t.Fatalf("with the file marked skip-worktree Loss = %+v, %v; want it clean with the paths and commits of %+v", clean, err, dirty)
	}
	if _, err := s.cleaner.Discard(ctx, got.Entry, dirty.Digest, dirty.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on the digest shown dirty = %v; want ErrLossChanged", err)
	}
}

// Something is lost only if it exists nowhere the copy's removal leaves
// intact, as far as Loss can see: a delivery task's clone of an upstream
// whose main carries an annotated tag v1 and a file checked out with CRLF
// line endings, and whose feature branch is a commit past main, which the
// clone holds as a remote-tracking reference. Loss lists the task's own
// commit and nothing the upstream holds: not v1, which the upstream
// advertises as that very tag object, not feature's commit, and not a.bat,
// which git would store as HEAD's blob though its bytes differ. An
// annotated tag only the copy holds, on the task's commit or on main, is
// listed, and so is an edit to a.bat. A remote-tracking reference holds
// only what the upstream still reaches. Without the upstream read as held,
// v1 and feature's commit are listed; without git's stored blob, a.bat is.
func TestALossTakesWhatTheUpstreamHoldsAsHeld(t *testing.T) {
	principles.Cite(t, principles.P15)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	seed := filepath.Join(t.TempDir(), "seed")
	rawGit(t, filepath.Dir(seed), "clone", "-q", s.upstream, seed)
	commit(t, seed, ".gitattributes", "*.bat text eol=crlf\n", "check scripts out with CRLF")
	commit(t, seed, "a.bat", "echo one\necho two\n", "a script checked out with CRLF")
	rawGit(t, seed, "tag", "--annotate", "--message", "release one", "v1")
	rawGit(t, seed, "push", "-q", "origin", "main", "v1")
	rawGit(t, seed, "switch", "-q", "-c", "feature")
	feature := strings.TrimSpace(commit(t, seed, "feature.txt", "the upstream's other work\n", "the upstream's other branch"))
	rawGit(t, seed, "push", "-q", "origin", "feature")
	v1 := strings.TrimSpace(rawGit(t, seed, "rev-parse", "v1"))

	tk := s.deliveryTask(t, "task/held")
	rawGit(t, tk.copy, "fetch", "-q", s.upstream, "+refs/heads/*:refs/remotes/upstream/*")
	own := strings.TrimSpace(commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit"))
	if raw, err := os.ReadFile(filepath.Join(tk.copy, "a.bat")); err != nil || !strings.Contains(string(raw), "\r\n") {
		t.Fatalf("the clone's a.bat reads %q, %v; want CRLF", raw, err)
	}
	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Entry.Established() {
		t.Fatalf("Keep = %+v, %v", got, err)
	}
	rawGit(t, tk.copy, "tag", "--annotate", "--message", "the copy's own", "mine", own)
	rawGit(t, tk.copy, "tag", "--annotate", "--message", "the copy's own on main", "copy-on-main", "main")
	mine := strings.TrimSpace(rawGit(t, tk.copy, "rev-parse", "mine"))
	onMain := strings.TrimSpace(rawGit(t, tk.copy, "rev-parse", "copy-on-main"))

	l, err := s.cleaner.Loss(ctx, got.Entry)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(l.Lacks, []string{own}) {
		t.Errorf("Loss lacks %v; want the task's own commit %s alone, not feature's %s", l.Lacks, own, feature)
	}
	var objects []string
	for _, o := range l.Objects {
		objects = append(objects, o.Object)
	}
	if !slices.Contains(objects, mine) || !slices.Contains(objects, onMain) || slices.Contains(objects, v1) {
		t.Errorf("Loss lists the objects %+v; want the copy's own tags %s and %s and not the upstream's v1 %s", l.Objects, mine, onMain, v1)
	}
	for _, f := range l.Files {
		if f.Kind != LostRepository {
			t.Errorf("Loss names %+v; want no path of the clean working tree", f)
		}
	}
	if err := os.WriteFile(filepath.Join(tk.copy, "a.bat"), []byte("echo one\r\necho changed\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l, err := s.cleaner.Loss(ctx, got.Entry); err != nil || !slices.Contains(l.Files, LostPath{LostChanged, "a.bat"}) {
		t.Errorf("Loss of the edited a.bat names %+v, %v; want a.bat changed", l.Files, err)
	}
	rawGit(t, tk.copy, "checkout", "--", "a.bat")

	// A new file staged as it stands is new, and its staged content is the
	// working tree's as git would store it, so it is not staged apart.
	if err := os.WriteFile(filepath.Join(tk.copy, "b.bat"), []byte("echo new\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "add", "b.bat")
	if l, err := s.cleaner.Loss(ctx, got.Entry); err != nil || !slices.Contains(l.Files, LostPath{LostNew, "b.bat"}) ||
		slices.Contains(l.Files, LostPath{LostStaged, "b.bat"}) {
		t.Errorf("Loss of a new file staged as it stands names %+v, %v; want b.bat new and not staged", l.Files, err)
	}
	rawGit(t, tk.copy, "rm", "-q", "-f", "b.bat")

	// A remote-tracking reference is the copy's record of a remote as last
	// fetched and holds nothing of itself. A commit only one names, which
	// no remote holds, is listed. One whose branch the upstream moved on
	// since is held through the commit that branch stands on now, which
	// the copy does not hold and Loss fetches. One whose branch the
	// upstream deleted, and whose objects the upstream's collection then
	// pruned, is listed: the reviewer's sequence, in which the stale
	// reference was taken as holding it, Loss omitted it and a discard
	// removed the only copy of it.
	tracked := strings.TrimSpace(rawGit(t, tk.copy, "commit-tree", "-p", own, "-m", "on no remote", own+"^{tree}"))
	rawGit(t, tk.copy, "update-ref", "refs/remotes/gate-copy/side", tracked)
	rawGit(t, seed, "switch", "-q", "-c", "moving", "main")
	moved := commit(t, seed, "moving.txt", "first\n", "a branch the upstream moves on")
	rawGit(t, seed, "switch", "-q", "-c", "doomed", "main")
	doomed := commit(t, seed, "doomed.txt", "pushed and then deleted\n", "a branch the upstream deletes")
	rawGit(t, seed, "push", "-q", "origin", "moving", "doomed")
	rawGit(t, tk.copy, "fetch", "-q", s.upstream, "+refs/heads/*:refs/remotes/upstream/*")
	rawGit(t, seed, "switch", "-q", "moving")
	movedOn := commit(t, seed, "moving.txt", "second\n", "the branch moves on")
	rawGit(t, seed, "push", "-q", "origin", "moving", ":doomed")
	rawGit(t, s.upstream, "reflog", "expire", "--expire=now", "--all")
	rawGit(t, s.upstream, "gc", "-q", "--prune=now")
	if _, err := rawGitErr(s.upstream, "cat-file", "-e", doomed); err == nil {
		t.Fatalf("the upstream still holds %s after the branch was deleted and pruned: the case would test nothing", doomed)
	}
	if _, err := rawGitErr(tk.copy, "cat-file", "-e", movedOn); err == nil {
		t.Fatalf("the copy already holds %s: the moved branch's case would test no fetch", movedOn)
	}
	if l, err := s.cleaner.Loss(ctx, got.Entry); err != nil || !slices.Contains(l.Lacks, tracked) || !slices.Contains(l.Lacks, doomed) ||
		slices.Contains(l.Lacks, moved) {
		t.Errorf("Loss lacks %v, %v; want %s, named by a remote-tracking reference alone, and %s, whose branch the upstream deleted, "+
			"and not %s, which the upstream's moved branch still reaches", l.Lacks, err, tracked, doomed, moved)
	}
	if refs := rawGit(t, tk.copy, "for-each-ref", "refs/assistant/"); refs != "" {
		t.Errorf("Loss left references behind in the copy:\n%s", refs)
	}

	// A commit shown as lost that the upstream takes up before the
	// confirmation is held then, not gone, so the confirmation removes
	// the copy.
	rawGit(t, tk.copy, "switch", "-q", "-c", "side")
	side := strings.TrimSpace(commit(t, tk.copy, "side.txt", "work on a side branch\n", "a side commit"))
	rawGit(t, tk.copy, "switch", "-q", "task/held")
	shown, err := s.cleaner.Loss(ctx, got.Entry)
	if err != nil || !slices.Contains(shown.Lacks, side) {
		t.Fatalf("Loss lacks %v, %v; want the side commit %s", shown.Lacks, err, side)
	}
	s.push(t, tk.copy, "side")
	if _, err := s.cleaner.Discard(ctx, got.Entry, shown.Digest, shown.Lacks, "", nil); err != nil {
		t.Fatalf("confirming what was shown once the upstream holds %s = %v; want the copy removed", side, err)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the copy still stands after the confirmed discard: %v", err)
	}
}

// A copy's reference is looked up on the upstream as every branch it could
// stand for: a branch as itself, a remote-tracking reference as every
// branch its name could end in, since a remote's name may hold a slash,
// and as the upstream's HEAD when it ends in HEAD. A tag and any other
// reference stand for none.
func TestUpstreamBranchNamesAreEveryBranchAReferenceCouldStandFor(t *testing.T) {
	t.Parallel()
	for ref, want := range map[string][]string{
		"refs/heads/task/kept":              {"refs/heads/task/kept"},
		"refs/remotes/origin/main":          {"refs/heads/main"},
		"refs/remotes/origin/HEAD":          {"HEAD"},
		"refs/remotes/team/gate/feature/x":  {"refs/heads/gate/feature/x", "refs/heads/feature/x", "refs/heads/x"},
		"refs/remotes/origin/":              nil,
		"refs/remotes/origin":               nil,
		"refs/tags/v1":                      nil,
		"refs/assistant/discard/task/a/b/c": nil,
	} {
		if got := upstreamBranchNames(ref); !slices.Equal(got, want) {
			t.Errorf("upstreamBranchNames(%q) = %q, want %q", ref, got, want)
		}
	}
}

// Loss runs every git it starts under the cleaner's configured deadline
// (CleanupDeps.GitDeadline, the git_deadline key), the final review of
// 3088420: an entry is kept against an upstream that answers, and its
// repository record is then pointed at a git:// listener that accepts and
// never answers, as a stalled upstream does. A cleaner built with a
// two-second deadline refuses the loss as unestablished within its
// deadline rather than waiting on the read. With the upstream back, the
// same cleaner reads the loss, so the deadline is not what refuses it.
// Without the cleaner's options on every repository Loss opens, the read
// ran under vcs's ten-minute fallback and the loss never came back.
func TestALossRunsGitUnderTheConfiguredDeadline(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/deadline")
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
	const deadline = 2 * time.Second
	bounded, err := NewCleaner(CleanupDeps{Home: s.home, Store: s.store, Forge: s.forge.Host(), GitDeadline: deadline})
	if err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var held sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	held.Add(1)
	go func() {
		defer held.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	release := func() {
		_ = listener.Close()
		mu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		held.Wait()
	}
	defer release()
	repository, err := s.store.Repository(ctx, cleanupRepository)
	if err != nil {
		t.Fatal(err)
	}
	stalled := repository
	stalled.UpstreamURL = "git://" + listener.Addr().String() + "/stalled.git"
	if _, err := s.store.UpsertRepository(ctx, stalled); err != nil {
		t.Fatal(err)
	}

	type answer struct {
		loss Loss
		err  error
	}
	answered := make(chan answer, 1)
	began := time.Now()
	go func() {
		l, err := bounded.Loss(ctx, got.Entry)
		answered <- answer{l, err}
	}()
	select {
	case a := <-answered:
		if took := time.Since(began); !errors.Is(a.err, ErrLossUnestablished) || took > 30*time.Second {
			t.Fatalf("Loss against a stalled upstream = %+v, %v after %s; want ErrLossUnestablished within the %s deadline", a.loss, a.err, took, deadline)
		}
	case <-time.After(60 * time.Second):
		release()
		a := <-answered
		t.Fatalf("Loss against a stalled upstream had not answered after 60s under a %s git deadline (it answered %v once the upstream was closed)", deadline, a.err)
	}

	if _, err := s.store.UpsertRepository(ctx, repository); err != nil {
		t.Fatal(err)
	}
	if l, err := bounded.Loss(ctx, got.Entry); err != nil || len(l.Lacks) == 0 {
		t.Fatalf("Loss with the upstream answering = %+v, %v; want the task's commit lost", l, err)
	}
}
