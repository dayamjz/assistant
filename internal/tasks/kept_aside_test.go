package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// keptLanded is a delivery task whose branch topic landed on the upstream,
// so P12's remote-branch proof holds for its clone, kept by the cleanup
// decision for what change does to the clone after the landing, with its
// established entry and the loss Loss shows then.
func keptLanded(t *testing.T, change func(copyPath string)) (*cleanupSubject, task, store.KeptWork, Loss) {
	t.Helper()
	s := newCleanupSubject(t, nil)
	ctx := context.Background()
	tk := s.deliveryTask(t, "topic")
	commit(t, tk.copy, "g.txt", "on topic\n", "on topic")
	s.push(t, tk.copy, "topic")
	change(tk.copy)
	_, err := s.cleanUp(t, tk.id)
	var refusal *CleanupRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("the cleanup of the changed clone = %v; want it refused", err)
	}
	kept, ok := KeptOf(refusal)
	if !ok {
		t.Fatalf("the refusal %+v is not kept work", refusal)
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Entry.Established() {
		t.Fatalf("Keep = %+v, %v", got, err)
	}
	shown, err := s.cleaner.Loss(ctx, got.Entry)
	if err != nil {
		t.Fatal(err)
	}
	return s, tk, got.Entry, shown
}

// besideCopy is every name in the directory the clone at copyPath stands
// in, and every reference of the upstream with what it points at: what a
// discard of the clone must leave as it is.
func besideCopy(t *testing.T, s *cleanupSubject, copyPath string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(copyPath))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Name() != filepath.Base(copyPath) {
			names = append(names, e.Name())
		}
	}
	return strings.Join(names, " ") + "\n" + rawGit(t, s.upstream, "for-each-ref", "--format=%(refname) %(objectname)")
}

// A discard deletes what it showed and nothing else, then the clone, the
// positive control for every refusal TestEveryChangeADiscardDidNotShowRefusesIt
// makes: a landed task's clone is kept holding a commit on a branch no
// remote holds, a tracked file edited and a file no commit records, the
// loss shows exactly those, and the discard on its digest removes the
// clone, leaves nothing set aside, returns the loss it showed and leaves
// everything beside the clone and every reference of the upstream as they
// were. A second landed clone kept for a file no commit records that is
// deleted again before the discard holds nothing a removal loses: its
// loss shows no commit and no path of the working tree, and its discard
// removes it. Without a removal once the confirmed items are set aside,
// neither clone is removed.
func TestADiscardRemovesWhatItShowedAndThenTheCopy(t *testing.T) {
	principles.Cite(t, principles.P15)
	ctx := context.Background()
	t.Run("what was shown", func(t *testing.T) {
		var aside string
		s, tk, entry, shown := keptLanded(t, func(copyPath string) {
			rawGit(t, copyPath, "switch", "-q", "-c", "aside")
			aside = strings.TrimSpace(commit(t, copyPath, "aside.txt", "on aside\n", "on aside"))
			rawGit(t, copyPath, "switch", "-q", "topic")
			if err := os.WriteFile(filepath.Join(copyPath, "g.txt"), []byte("edited after the landing\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(copyPath, "notes.txt"), []byte("not committed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		if !slices.Equal(shown.Lacks, []string{aside}) || !slices.Contains(shown.Files, LostPath{LostChanged, "g.txt"}) ||
			!slices.Contains(shown.Files, LostPath{LostNew, "notes.txt"}) {
			t.Fatalf("Loss = %+v; want the commit %s, g.txt changed and notes.txt new", shown, aside)
		}
		beside := besideCopy(t, s, tk.copy)
		lost, err := s.cleaner.Discard(ctx, entry, shown.Digest, shown.Lacks, "", nil)
		if err != nil {
			t.Fatalf("Discard on the digest shown: %v", err)
		}
		if lost.Digest != shown.Digest || !slices.Equal(lost.Lacks, shown.Lacks) || !slices.Equal(lost.Files, shown.Files) {
			t.Fatalf("Discard returned %+v; want the loss shown, %+v", lost, shown)
		}
		if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the clone still stands: %v", err)
		}
		if _, err := os.Lstat(asideDir(tk.copy)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("what was set aside still stands: %v", err)
		}
		if now := besideCopy(t, s, tk.copy); now != beside {
			t.Fatalf("beside the clone and in the upstream the discard left\n%s\nwant\n%s", now, beside)
		}
	})
	t.Run("nothing left to lose", func(t *testing.T) {
		s, tk, entry, _ := keptLanded(t, func(copyPath string) {
			if err := os.WriteFile(filepath.Join(copyPath, "notes.txt"), []byte("not committed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		if err := os.Remove(filepath.Join(tk.copy, "notes.txt")); err != nil {
			t.Fatal(err)
		}
		shown, err := s.cleaner.Loss(ctx, entry)
		if err != nil {
			t.Fatal(err)
		}
		if len(shown.Lacks) != 0 || len(shown.Objects) != 0 || shown.Dirty || shown.Untracked || shown.Stash {
			t.Fatalf("Loss = %+v; want nothing lost", shown)
		}
		for _, f := range shown.Files {
			if f.Kind != LostRepository {
				t.Fatalf("Loss names %+v; want no path of the working tree", f)
			}
		}
		if _, err := s.cleaner.Discard(ctx, entry, shown.Digest, shown.Lacks, "", nil); err != nil {
			t.Fatalf("Discard of a clone holding nothing to lose: %v", err)
		}
		if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the clone still stands: %v", err)
		}
	})
}

// errStoppedAside is what the tests below stop a discard with once it has
// set what it showed aside, as a service that stopped there leaves it.
var errStoppedAside = errors.New("stopped once what was shown was set aside")

// keptWithWork is a delivery task's clone kept for a commit of its own and a
// file no commit records, with its entry and the loss shown.
func keptWithWork(t *testing.T) (*cleanupSubject, task, store.KeptWork, Loss) {
	t.Helper()
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/aside")
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
	if err := os.WriteFile(filepath.Join(tk.copy, "scratch.txt"), []byte("not committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shown, err := s.cleaner.Loss(ctx, got.Entry)
	if err != nil {
		t.Fatal(err)
	}
	return s, tk, got.Entry, shown
}

// A discard that stops once it has set what it showed aside loses nothing:
// what it set aside stands beside the copy, Loss refuses the copy with
// ErrAsideStands rather than show the copy without it, and the next
// discard of the copy puts all of it back before it reads anything, so a
// discard on a digest the copy does not hold refuses with the copy exactly
// as it was before the first, and one on the digest shown then removes it.
// Without the restore at the next discard, the copy read without what was
// set aside seals another digest and the work set aside is never put back.
func TestADiscardStoppedPartWayIsPutBackBeforeTheCopyIsRead(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s, tk, entry, shown := keptWithWork(t)
	before := copySnapshot(t, tk.copy)
	s.cleaner.betweenAsideAndCheck = func() error { return errStoppedAside }
	if _, err := s.cleaner.Discard(ctx, entry, shown.Digest, shown.Lacks, "", nil); !errors.Is(err, errStoppedAside) {
		t.Fatalf("the stopped discard = %v; want it stopped", err)
	}
	if _, err := os.Lstat(filepath.Join(tk.copy, "scratch.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scratch.txt stands in the copy once set aside: %v; the case would test nothing", err)
	}
	if raw, err := os.ReadFile(filepath.Join(asideDir(tk.copy), "tree", "scratch.txt")); err != nil || string(raw) != "not committed\n" {
		t.Fatalf("what was set aside holds %q, %v; want scratch.txt as it was", raw, err)
	}
	if _, err := s.cleaner.Loss(ctx, entry); !errors.Is(err, ErrAsideStands) {
		t.Fatalf("Loss with what was set aside standing = %v; want ErrAsideStands", err)
	}
	s.cleaner.betweenAsideAndCheck = nil
	if _, err := s.cleaner.Discard(ctx, entry, "not the digest", shown.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on another digest = %v; want ErrLossChanged", err)
	}
	if now := copySnapshot(t, tk.copy); now != before {
		t.Fatalf("the copy once put back reads\n%s\nwant it as it was\n%s", now, before)
	}
	if _, err := s.cleaner.Discard(ctx, entry, shown.Digest, shown.Lacks, "", nil); err != nil {
		t.Fatalf("Discard on the digest shown: %v", err)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the clone still stands: %v", err)
	}
}

// What a discard set aside is never put back over something written at its
// path meanwhile: a file written where the discard moved scratch.txt from,
// once it has, refuses the discard (the copy holds a file it did not
// show), and the restore leaves both, the new file in the copy and
// scratch.txt as it was beside it, refusing with ErrAsideStands naming
// what it could not put back, as Loss then does. Without the no-replace
// move back, the restore overwrites the new file.
func TestARestoreLeavesWhatItSetAsideWhereItsPathWasTaken(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s, tk, entry, shown := keptWithWork(t)
	s.cleaner.betweenAsideAndCheck = func() error {
		return os.WriteFile(filepath.Join(tk.copy, "scratch.txt"), []byte("written meanwhile\n"), 0o600)
	}
	_, err := s.cleaner.Discard(ctx, entry, shown.Digest, shown.Lacks, "", nil)
	if !errors.Is(err, ErrLossChanged) || !errors.Is(err, ErrAsideStands) || !strings.Contains(err.Error(), "scratch.txt") {
		t.Fatalf("the discard over a file written where one was set aside = %v; want ErrLossChanged and ErrAsideStands naming scratch.txt", err)
	}
	if raw, err := os.ReadFile(filepath.Join(tk.copy, "scratch.txt")); err != nil || string(raw) != "written meanwhile\n" {
		t.Fatalf("the copy's scratch.txt holds %q, %v; want what was written meanwhile", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(asideDir(tk.copy), "tree", "scratch.txt")); err != nil || string(raw) != "not committed\n" {
		t.Fatalf("what was set aside holds %q, %v; want scratch.txt as it was", raw, err)
	}
	if _, err := s.cleaner.Loss(ctx, entry); !errors.Is(err, ErrAsideStands) {
		t.Fatalf("Loss = %v; want ErrAsideStands", err)
	}
}

// A discard confirmed over a copy that is gone removes what a discard of
// it that stopped once it had set what it showed aside left beside it,
// every item of which that confirmation covers, and reports nothing lost:
// the copy goes after its removal and before what was set aside does, as
// a service that stopped there leaves it. Without that, what was set aside
// stays beside a copy that no longer exists, with nothing to put it back.
func TestADiscardOfACopyGoneRemovesWhatItsStoppedDiscardSetAside(t *testing.T) {
	principles.Cite(t, principles.P15)
	ctx := context.Background()
	s, tk, entry, shown := keptWithWork(t)
	s.cleaner.betweenAsideAndCheck = func() error { return errStoppedAside }
	if _, err := s.cleaner.Discard(ctx, entry, shown.Digest, shown.Lacks, "", nil); !errors.Is(err, errStoppedAside) {
		t.Fatalf("the stopped discard = %v; want it stopped", err)
	}
	s.cleaner.betweenAsideAndCheck = nil
	if err := os.RemoveAll(tk.copy); err != nil {
		t.Fatal(err)
	}
	if lost, err := s.cleaner.Discard(ctx, entry, shown.Digest, shown.Lacks, "", nil); err != nil || lost.Digest != "" {
		t.Fatalf("Discard of a copy gone = %+v, %v; want nothing lost", lost, err)
	}
	if _, err := os.Lstat(asideDir(tk.copy)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("what was set aside still stands beside the copy gone: %v", err)
	}
}

// A restore takes out what the discard wrote only while it is as written,
// and makes again every directory it emptied with the mode it had: a
// landed task's clone holds sub/x.txt, a tracked file whose directory
// holds nothing else and whose mode the umask cannot make, edited after
// the landing. A discard that stops once it has set the edit aside has
// emptied sub, removed it, and written x.txt from HEAD's tree into a sub
// it made; the next discard, on a digest the copy does not hold, puts the
// copy back exactly, sub's mode among it. Then an edit to the file the
// discard wrote, once it has, refuses the discard and is kept, with the
// edit shown left beside the copy and ErrAsideStands naming the file.
// Without the check of what was written, the restore removes that edit.
func TestARestoreTakesOutOnlyWhatItWroteAndMakesItsDirectoriesAgain(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := newCleanupSubject(t, nil)
	tk := s.deliveryTask(t, "topic")
	if err := os.MkdirAll(filepath.Join(tk.copy, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	commit(t, tk.copy, filepath.Join("sub", "x.txt"), "on topic\n", "on topic")
	s.push(t, tk.copy, "topic")
	if err := os.Chmod(filepath.Join(tk.copy, "sub"), 0o777); err != nil {
		t.Fatal(err)
	}
	x := filepath.Join(tk.copy, "sub", "x.txt")
	if err := os.WriteFile(x, []byte("edited after the landing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonCopyDirty))
	if !ok {
		t.Fatal("a dirty refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Entry.Established() {
		t.Fatalf("Keep = %+v, %v", got, err)
	}
	shown, err := s.cleaner.Loss(ctx, got.Entry)
	if err != nil || !slices.Contains(shown.Files, LostPath{LostChanged, "sub/x.txt"}) {
		t.Fatalf("Loss = %+v, %v; want sub/x.txt changed", shown, err)
	}
	before := copySnapshot(t, tk.copy)
	s.cleaner.betweenAsideAndCheck = func() error { return errStoppedAside }
	if _, err := s.cleaner.Discard(ctx, got.Entry, shown.Digest, shown.Lacks, "", nil); !errors.Is(err, errStoppedAside) {
		t.Fatalf("the stopped discard = %v; want it stopped", err)
	}
	if raw, err := os.ReadFile(x); err != nil || string(raw) != "on topic\n" {
		t.Fatalf("sub/x.txt holds %q, %v once the edit is set aside; want HEAD's, written by the discard", raw, err)
	}
	if info, err := os.Stat(filepath.Join(tk.copy, "sub")); err != nil || info.Mode().Perm() == 0o777 {
		t.Fatalf("sub reads %v, %v once set aside; want it made again by the write, so the case tests its mode", info, err)
	}
	s.cleaner.betweenAsideAndCheck = nil
	if _, err := s.cleaner.Discard(ctx, got.Entry, "not the digest", shown.Lacks, "", nil); !errors.Is(err, ErrLossChanged) {
		t.Fatalf("a discard on another digest = %v; want ErrLossChanged", err)
	}
	if now := copySnapshot(t, tk.copy); now != before {
		t.Fatalf("the copy once put back reads\n%s\nwant it as it was\n%s", now, before)
	}

	s.cleaner.betweenAsideAndCheck = func() error {
		return os.WriteFile(x, []byte("edited once the discard wrote it\n"), 0o600)
	}
	_, err = s.cleaner.Discard(ctx, got.Entry, shown.Digest, shown.Lacks, "", nil)
	if !errors.Is(err, ErrLossChanged) || !errors.Is(err, ErrAsideStands) || !strings.Contains(err.Error(), "sub/x.txt") {
		t.Fatalf("the discard over an edit to what it wrote = %v; want ErrLossChanged and ErrAsideStands naming sub/x.txt", err)
	}
	if raw, err := os.ReadFile(x); err != nil || string(raw) != "edited once the discard wrote it\n" {
		t.Fatalf("sub/x.txt holds %q, %v; want the edit made once the discard wrote it", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(asideDir(tk.copy), "tree", "sub", "x.txt")); err != nil || string(raw) != "edited after the landing\n" {
		t.Fatalf("what was set aside holds %q, %v; want the edit shown", raw, err)
	}
}

// A restore that could not finish takes up where it stopped, round 23's
// third finding: a landed clone is kept for an edit, staged, to
// sub/x.txt, a tracked file whose directory holds nothing else, and a
// file no commit records, notes.txt; the discard moves both aside, empties
// and removes sub, replaces sub/x.txt's index entry with HEAD's, and
// writes HEAD's sub/x.txt into a sub it makes. A file written where
// notes.txt was refuses the discard, and the restore puts everything back
// but notes.txt, refusing with ErrAsideStands. The person then removes
// that file and unstages sub/x.txt; the next put-back finishes without
// touching anything it already put back: sub/x.txt's edit stands, its
// index entry is the one the person left, nothing is left aside, and Loss
// reads the copy. Before, the second restore took the edit it had put
// back for a change to the file it wrote and refused for good; with the
// index entry still in the journal it would stage the edit again; and with
// the directory it made still in the journal it would refuse that
// directory, holding sub/x.txt again, as one somebody wrote into.
func TestARestoreThatStoppedTakesUpWhereItStopped(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := newCleanupSubject(t, nil)
	tk := s.deliveryTask(t, "topic")
	if err := os.MkdirAll(filepath.Join(tk.copy, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	commit(t, tk.copy, filepath.Join("sub", "x.txt"), "on topic\n", "on topic")
	s.push(t, tk.copy, "topic")
	x := filepath.Join(tk.copy, "sub", "x.txt")
	if err := os.WriteFile(x, []byte("edited after the landing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "add", filepath.Join("sub", "x.txt"))
	notes := filepath.Join(tk.copy, "notes.txt")
	if err := os.WriteFile(notes, []byte("not committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonCopyDirty))
	if !ok {
		t.Fatal("a dirty refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Entry.Established() {
		t.Fatalf("Keep = %+v, %v", got, err)
	}
	shown, err := s.cleaner.Loss(ctx, got.Entry)
	if err != nil {
		t.Fatal(err)
	}
	before := copySnapshot(t, tk.copy)
	s.cleaner.betweenAsideAndCheck = func() error {
		return os.WriteFile(notes, []byte("written meanwhile\n"), 0o600)
	}
	if _, err := s.cleaner.Discard(ctx, got.Entry, shown.Digest, shown.Lacks, "", nil); !errors.Is(err, ErrAsideStands) {
		t.Fatalf("the discard over a file written where one was set aside = %v; want ErrAsideStands", err)
	}
	s.cleaner.betweenAsideAndCheck = nil
	if notesAside := []string{"tree /notes.txt ", "aside stands"}; withoutLines(copySnapshot(t, tk.copy), notesAside) != withoutLines(before, notesAside) {
		t.Fatalf("the first restore left the copy as\n%s\nwant it as it was but notes.txt\n%s", copySnapshot(t, tk.copy), before)
	}
	if err := os.Remove(notes); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "reset", "--quiet", "--", filepath.Join("sub", "x.txt"))
	unstaged := rawGit(t, tk.copy, "ls-files", "--stage", "--", filepath.Join("sub", "x.txt"))
	if err := s.cleaner.PutBack(ctx, got.Entry, ""); err != nil {
		t.Fatalf("PutBack once the file written meanwhile is removed: %v", err)
	}
	if _, err := os.Lstat(asideDir(tk.copy)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("something is still set aside: %v", err)
	}
	if now := rawGit(t, tk.copy, "ls-files", "--stage", "--", filepath.Join("sub", "x.txt")); now != unstaged {
		t.Fatalf("sub/x.txt's index entry reads %q after the put-back; want the one the person left, %q", now, unstaged)
	}
	index := []string{"index sub/x.txt "}
	if now := copySnapshot(t, tk.copy); withoutLines(now, index) != withoutLines(before, index) {
		t.Fatalf("the copy once put back reads\n%s\nwant it as it was but sub/x.txt's index entry\n%s", now, before)
	}
	if _, err := s.cleaner.Loss(ctx, got.Entry); err != nil {
		t.Fatalf("Loss once put back: %v", err)
	}
}

// A restore puts an index path back only while it holds what the discard
// wrote there, round 23's second finding: a landed clone is kept for an
// edit to g.txt that is staged, so the discard replaces g.txt's index
// entry with HEAD's. Content staged at g.txt once it has refuses the
// discard, and the restore leaves that staging, and the file written
// over the one it wrote, where they are, refusing with ErrAsideStands
// naming g.txt, with the edit shown beside the copy. Before, the restore
// put the whole index back over the new staging.
func TestARestoreLeavesContentStagedWhereTheDiscardWroteTheIndex(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s, tk, entry, shown := keptLanded(t, func(copyPath string) {
		if err := os.WriteFile(filepath.Join(copyPath, "g.txt"), []byte("edited and staged\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rawGit(t, copyPath, "add", "g.txt")
	})
	g := filepath.Join(tk.copy, "g.txt")
	var staged string
	s.cleaner.betweenAsideAndCheck = func() error {
		if err := os.WriteFile(g, []byte("staged once the discard wrote the index\n"), 0o600); err != nil {
			return err
		}
		rawGit(t, tk.copy, "add", "g.txt")
		staged = rawGit(t, tk.copy, "ls-files", "--stage", "--", "g.txt")
		return nil
	}
	_, err := s.cleaner.Discard(ctx, entry, shown.Digest, shown.Lacks, "", nil)
	if !errors.Is(err, ErrLossChanged) || !errors.Is(err, ErrAsideStands) || !strings.Contains(err.Error(), "index entries of g.txt") {
		t.Fatalf("the discard over content staged where it wrote the index = %v; want ErrLossChanged and ErrAsideStands naming g.txt's index entries", err)
	}
	if now := rawGit(t, tk.copy, "ls-files", "--stage", "--", "g.txt"); now != staged {
		t.Fatalf("g.txt's index entry reads %q after the refusal; want what was staged once the discard wrote it, %q", now, staged)
	}
	if raw, err := os.ReadFile(filepath.Join(asideDir(tk.copy), "tree", "g.txt")); err != nil || string(raw) != "edited and staged\n" {
		t.Fatalf("what was set aside holds %q, %v; want the edit shown", raw, err)
	}
}

// A reflog goes back only once its reference does: a clone kept for a
// commit of its own has its branch deleted and the branch's reflog set
// aside, and once it has, the branch is made again, without a reflog, at a
// new commit nothing else holds. The discard refuses, the restore cannot
// write the branch back over it, and the reflog stays aside rather than
// becoming the new branch's, refusing with ErrAsideStands naming the
// branch.
func TestAReflogGoesBackOnlyWithItsReference(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s, tk, entry, shown := keptWithWork(t)
	branch := "refs/heads/task/aside"
	main := rawGit(t, tk.copy, "rev-parse", "refs/heads/main")
	s.cleaner.betweenAsideAndCheck = func() error {
		elsewhere := rawGit(t, tk.copy, "commit-tree", main+"^{tree}", "-p", main, "-m", "the branch made again")
		rawGit(t, tk.copy, "-c", "core.logAllRefUpdates=false", "update-ref", branch, elsewhere, "")
		return nil
	}
	_, err := s.cleaner.Discard(ctx, entry, shown.Digest, shown.Lacks, "", nil)
	if !errors.Is(err, ErrAsideStands) || !strings.Contains(err.Error(), branch) {
		t.Fatalf("the discard over the branch made again = %v; want ErrAsideStands naming %s", err, branch)
	}
	if _, err := os.Lstat(filepath.Join(tk.copy, ".git", "logs", "refs", "heads", "task", "aside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the branch made again has a reflog: %v; want the set-aside one kept aside", err)
	}
}
