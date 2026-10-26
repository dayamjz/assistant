package tasks

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/principles"
)

// Unheld is what of an entry's holds the copy no longer holds, by what the
// copy names now (the final full review of feddd14 and the delta review of
// 9ef7722): a copy kept standing on its own commit C and on a blob a tag
// names holds both as kept, the tag annotated so that the blob is what the
// tag peels to and not what it points at; with C amended to D it holds C
// through HEAD's reflog alone, an amend writing no root reference; with
// HEAD reset to C's parent and the reflogs expired it holds C through
// ORIG_HEAD, the root reference the reset wrote; with ORIG_HEAD deleted
// too, C is unheld while the blob the tag names is still held; with the
// tag deleted the blob is unheld as well; and with the same bytes then
// committed as a file, the blob is held through HEAD's tree, as any object
// a commit the copy names records is (the carry-on audit of b448115). The
// copy at a path where nothing stands is an error, not an answer.
func TestUnheldIsWhatNothingTheCopyNamesReaches(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/unheld")
	own := commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit")
	if err := os.WriteFile(filepath.Join(tk.copy, "data.bin"), []byte("bytes the tag names\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	blob := rawGit(t, tk.copy, "hash-object", "-w", "data.bin")
	if err := os.Remove(filepath.Join(tk.copy, "data.bin")); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "tag", "-a", "-m", "the data the task kept", "data", blob)
	if peeled := rawGit(t, tk.copy, "rev-parse", "data^{}"); peeled != blob || rawGit(t, tk.copy, "rev-parse", "data") == blob {
		t.Fatalf("the tag names %s and peels to %s, want an annotated tag peeling to the blob %s", rawGit(t, tk.copy, "rev-parse", "data"), peeled, blob)
	}
	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Recorded {
		t.Fatalf("Keep = %+v, %v; want a new entry", got, err)
	}
	entry := got.Entry
	if !slices.Contains(entry.Holds, own) || !slices.Contains(entry.Holds, blob) {
		t.Fatalf("the entry holds %v; want the commit %s and the blob %s among them", entry.Holds, own, blob)
	}
	unheld := func(want ...string) {
		t.Helper()
		got, err := s.cleaner.Unheld(ctx, entry)
		if err != nil {
			t.Fatalf("Unheld: %v", err)
		}
		if len(got) != len(want) || (len(want) > 0 && !slices.Equal(got, want)) {
			t.Fatalf("Unheld = %v, want %v", got, want)
		}
	}

	unheld()
	if err := os.WriteFile(filepath.Join(tk.copy, "own.txt"), []byte("the task's own work, reworded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "add", "own.txt")
	rawGit(t, tk.copy, "commit", "-q", "--amend", "-m", "the task's own commit, amended")
	if head := rawGit(t, tk.copy, "rev-parse", "HEAD"); head == own {
		t.Fatal("the amend left HEAD where it was")
	}
	if _, err := os.Lstat(filepath.Join(tk.copy, ".git", "ORIG_HEAD")); err == nil {
		t.Fatal("the amend wrote ORIG_HEAD, so this step would not read the reflog alone")
	}
	unheld()
	rawGit(t, tk.copy, "reset", "-q", "--hard", own)
	rawGit(t, tk.copy, "reset", "-q", "--hard", "HEAD~1")
	rawGit(t, tk.copy, "reflog", "expire", "--expire=now", "--expire-unreachable=now", "--all")
	if orig := rawGit(t, tk.copy, "rev-parse", "ORIG_HEAD"); orig != own {
		t.Fatalf("ORIG_HEAD is %s, want %s", orig, own)
	}
	unheld()
	rawGit(t, tk.copy, "update-ref", "-d", "ORIG_HEAD")
	unheld(own)
	rawGit(t, tk.copy, "tag", "-d", "data")
	unheld(own, blob)
	if err := os.WriteFile(filepath.Join(tk.copy, "data.bin"), []byte("bytes the tag names\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "add", "data.bin")
	rawGit(t, tk.copy, "commit", "-q", "-m", "the bytes as a file")
	if got := rawGit(t, tk.copy, "rev-parse", "HEAD:data.bin"); got != blob {
		t.Fatalf("HEAD:data.bin is %s, want the blob %s", got, blob)
	}
	unheld(own)

	gone := entry
	gone.CopyPath = tk.copy + "-gone"
	if _, err := s.cleaner.Unheld(ctx, gone); err == nil {
		t.Fatal("Unheld answered for a copy that is gone")
	}
}
