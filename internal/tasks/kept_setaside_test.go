package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// setAsideLoss moves each working-tree path the loss shows into the
// discard's directory and writes missing tracked paths back from the
// index, so a putBack restores the copy (PRD section 6 step 1's path half).
func TestADiscardSetAsideMovesLossPathsAndWritesTrackedOnesBack(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, _ := keptWithWork(t)
	ctx := context.Background()
	tracked := filepath.Join(tk.copy, "own.txt")
	if err := os.WriteFile(tracked, []byte("edited in the copy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shown, err := s.cleaner.Loss(ctx, entry, "")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []LostKind
	for _, f := range shown.Files {
		kinds = append(kinds, f.Kind)
	}
	if !slices.Contains(kinds, LostChanged) || !slices.Contains(kinds, LostNew) {
		t.Fatalf("Loss.Files kinds = %v; want changed own.txt and new scratch.txt", kinds)
	}
	wantOwn, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	wantScratch, err := os.ReadFile(filepath.Join(tk.copy, "scratch.txt"))
	if err != nil {
		t.Fatal(err)
	}

	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, found, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || !found {
		t.Fatalf("standingDiscard = %v, %v", found, err)
	}
	if err := s.cleaner.setAsideLoss(ctx, dir, &last, tk.copy, shown); err != nil {
		t.Fatalf("setAsideLoss = %v", err)
	}

	asideOwn := filepath.Join(dir, "paths", "own.txt")
	asideScratch := filepath.Join(dir, "paths", "scratch.txt")
	if raw, err := os.ReadFile(asideOwn); err != nil || string(raw) != string(wantOwn) {
		t.Fatalf("aside own.txt = %q, %v; want the edited bytes", raw, err)
	}
	if raw, err := os.ReadFile(asideScratch); err != nil || string(raw) != string(wantScratch) {
		t.Fatalf("aside scratch.txt = %q, %v; want the untracked bytes", raw, err)
	}
	if _, err := os.Lstat(filepath.Join(tk.copy, "scratch.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scratch.txt still stands in the copy: %v", err)
	}
	// own.txt was written back from the index (HEAD's content).
	if raw, err := os.ReadFile(tracked); err != nil || string(raw) != "the task's own work\n" {
		t.Fatalf("own.txt after setAside = %q, %v; want HEAD's content written back", raw, err)
	}
	j, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || len(j.Steps) < 3 {
		t.Fatalf("journal steps = %+v, %v; want moves and a writeBack", j.Steps, err)
	}

	if err := putBack(dir, j); err != nil {
		t.Fatalf("putBack = %v", err)
	}
	if raw, err := os.ReadFile(tracked); err != nil || string(raw) != string(wantOwn) {
		t.Fatalf("after putBack, own.txt = %q, %v; want the edited bytes restored", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(tk.copy, "scratch.txt")); err != nil || string(raw) != string(wantScratch) {
		t.Fatalf("after putBack, scratch.txt = %q, %v; want the untracked bytes restored", raw, err)
	}
}

// A deletion the loss shows is restored from the index on set-aside, and
// putBack takes that write out again (PRD section 6).
func TestADiscardSetAsideWritesBackADeletedTrackedPath(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, _ := keptWithWork(t)
	ctx := context.Background()
	tracked := filepath.Join(tk.copy, "own.txt")
	if err := os.Remove(tracked); err != nil {
		t.Fatal(err)
	}
	// Drop the untracked file so the loss is the deletion alone among paths.
	if err := os.Remove(filepath.Join(tk.copy, "scratch.txt")); err != nil {
		t.Fatal(err)
	}
	shown, err := s.cleaner.Loss(ctx, entry, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(shown.Files, func(f LostPath) bool { return f.Kind == LostDeleted && f.Path == "own.txt" }) {
		t.Fatalf("Loss.Files = %+v; want own.txt deleted", shown.Files)
	}
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cleaner.setAsideLoss(ctx, dir, &last, tk.copy, shown); err != nil {
		t.Fatalf("setAsideLoss = %v", err)
	}
	if raw, err := os.ReadFile(tracked); err != nil || string(raw) != "the task's own work\n" {
		t.Fatalf("own.txt after setAside = %q, %v; want it written back from the index", raw, err)
	}
	if err := putBack(dir, last); err != nil {
		t.Fatalf("putBack = %v", err)
	}
	if _, err := os.Lstat(tracked); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after putBack, own.txt still stands: %v", err)
	}
}

// A path that changes after the loss is read refuses set-aside with
// ErrLossChanged; an earlier path already moved is left for putBack (P6).
func TestADiscardSetAsideRefusesAPathThatChangedSinceTheLoss(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, _ := keptWithWork(t)
	ctx := context.Background()
	tracked := filepath.Join(tk.copy, "own.txt")
	if err := os.WriteFile(tracked, []byte("edited in the copy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shown, err := s.cleaner.Loss(ctx, entry, "")
	if err != nil {
		t.Fatal(err)
	}
	wantOwn, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tk.copy, "scratch.txt"), []byte("changed since the loss\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = s.cleaner.setAsideLoss(ctx, dir, &last, tk.copy, shown)
	if !errors.Is(err, ErrLossChanged) {
		t.Fatalf("setAsideLoss after a change = %v; want ErrLossChanged", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "paths", "own.txt")); err != nil {
		t.Fatalf("the earlier changed path was not set aside before the refusal: %v", err)
	}
	if err := putBack(dir, last); err != nil {
		t.Fatalf("putBack after a partial setAside = %v", err)
	}
	if raw, err := os.ReadFile(tracked); err != nil || string(raw) != string(wantOwn) {
		t.Fatalf("after putBack, own.txt = %q, %v; want the edited bytes restored", raw, err)
	}
}

// A deleted directory the loss shows once is written back for each of its
// tracked children still named in the index (PRD section 6).
func TestADiscardSetAsideWritesBackChildrenOfADeletedDirectory(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, _ := keptWithWork(t)
	ctx := context.Background()
	if err := os.Remove(filepath.Join(tk.copy, "scratch.txt")); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(tk.copy, "gone")
	if err := os.MkdirAll(filepath.Join(gone, "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(gone, "a.txt"), "in gone\n")
	writeFile(t, filepath.Join(gone, "deep", "b.txt"), "deeper\n")
	rawGit(t, tk.copy, "add", "gone")
	rawGit(t, tk.copy, "commit", "--quiet", "-m", "add gone")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	shown, err := s.cleaner.Loss(ctx, entry, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(shown.Files, func(f LostPath) bool { return f.Kind == LostDeleted && f.Path == "gone/" }) {
		t.Fatalf("Loss.Files = %+v; want gone/ deleted", shown.Files)
	}
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cleaner.setAsideLoss(ctx, dir, &last, tk.copy, shown); err != nil {
		t.Fatalf("setAsideLoss = %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(gone, "a.txt")); err != nil || string(raw) != "in gone\n" {
		t.Fatalf("gone/a.txt after setAside = %q, %v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(gone, "deep", "b.txt")); err != nil || string(raw) != "deeper\n" {
		t.Fatalf("gone/deep/b.txt after setAside = %q, %v", raw, err)
	}
}

// A tracked path that goes missing without being shown as changed or
// deleted refuses write-back (PRD section 6: a deletion nobody was shown).
func TestADiscardSetAsideRefusesAnUnshownMissingTrackedPath(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, _ := keptWithWork(t)
	ctx := context.Background()
	if err := os.Remove(filepath.Join(tk.copy, "scratch.txt")); err != nil {
		t.Fatal(err)
	}
	shown, err := s.cleaner.Loss(ctx, entry, "")
	if err != nil {
		t.Fatal(err)
	}
	// Loss no longer names scratch; own.txt still matches HEAD so it is
	// not in Files. Delete own.txt after the loss so write-back would see
	// an unshown missing tracked path.
	if err := os.Remove(filepath.Join(tk.copy, "own.txt")); err != nil {
		t.Fatal(err)
	}
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	err = s.cleaner.setAsideLoss(ctx, dir, &last, tk.copy, shown)
	if !errors.Is(err, ErrLossChanged) {
		t.Fatalf("setAsideLoss with an unshown missing path = %v; want ErrLossChanged", err)
	}
}
