package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// What a discard would lose is read from the entry's basis and the copy as
// it stands, with a digest that names exactly that state: the same copy
// digests the same twice, a file no commit records changes it, and removing
// the file brings the first digest back. Discard refuses a digest that is
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
