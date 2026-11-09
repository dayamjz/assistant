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
// ErrLossChanged and leaves what was journalled for putBack (P6).
func TestADiscardSetAsideRefusesAPathThatChangedSinceTheLoss(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, _, shown := keptWithWork(t)
	ctx := context.Background()
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
}
