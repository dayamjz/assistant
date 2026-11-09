package tasks

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// A discard that set a path aside with a journalled move puts it back from
// the journal: the path stands again holding what it held, the aside is
// empty of it, and the journal's step is struck (PRD section 6, refusal
// before the point of no return).
func TestADiscardPutBackRestoresAPathSetAsideWithTheJournal(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, _, _ := keptWithWork(t)
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, found, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || !found {
		t.Fatalf("standingDiscard = %v, %v", found, err)
	}
	from := filepath.Join(tk.copy, "scratch.txt")
	to := filepath.Join(dir, "paths", "scratch.txt")
	before, err := readCopyState(from)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{
		Kind: discardStepMove, From: from, To: to,
	}); err != nil {
		t.Fatal(err)
	}
	last, _, err = s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if moved, err := moveAsRead(from, to, before, "scratch.txt"); !moved || err != nil {
		t.Fatalf("moveAsRead = %v, %v; want moved", moved, err)
	}
	if _, err := os.Lstat(from); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after the move, %s still stands: %v", from, err)
	}

	if err := putBack(dir, last); err != nil {
		t.Fatalf("putBack = %v", err)
	}
	got, err := os.ReadFile(from)
	if err != nil || string(got) != string(want) {
		t.Fatalf("after putBack, %s reads %q, %v; want %q", from, got, err, want)
	}
	if _, err := os.Lstat(to); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after putBack, the aside still holds %s: %v", to, err)
	}
	j, found, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || !found {
		t.Fatalf("after putBack, standingDiscard = %v, %v", found, err)
	}
	if len(j.Steps) != 0 {
		t.Fatalf("after putBack, journal steps = %+v; want none struck", j.Steps)
	}
}

// A path that cannot go back, because something now stands where it goes,
// stays beside the copy: putBack names both, leaves the aside, and keeps
// the step on the journal (PRD section 6).
func TestADiscardPutBackLeavesAPathBlockedAtItsOriginalName(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, _, _ := keptWithWork(t)
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(tk.copy, "scratch.txt")
	to := filepath.Join(dir, "paths", "scratch.txt")
	before, err := readCopyState(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{Kind: discardStepMove, From: from, To: to}); err != nil {
		t.Fatal(err)
	}
	last, _, err = s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := moveAsRead(from, to, before, "scratch.txt"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, from, "written meanwhile\n")

	err = putBack(dir, last)
	if !errors.Is(err, ErrAsideStands) {
		t.Fatalf("putBack over a standing path = %v; want ErrAsideStands", err)
	}
	if raw, err := os.ReadFile(from); err != nil || string(raw) != "written meanwhile\n" {
		t.Fatalf("the path written meanwhile reads %q, %v", raw, err)
	}
	if raw, err := os.ReadFile(to); err != nil || string(raw) != "not committed\n" {
		t.Fatalf("the aside reads %q, %v; want the set-aside bytes left", raw, err)
	}
	j, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || len(j.Steps) != 1 || j.Steps[0].Kind != discardStepMove {
		t.Fatalf("after a blocked putBack, journal = %+v, %v; want the move step kept", j, err)
	}
}

// A stop after the rename and before the journal strike leaves the path
// restored and the aside gone with the move step still recorded: the next
// putBack treats that as done and strikes the step (P6).
func TestADiscardPutBackResumesAfterAMoveRestoredBeforeItsStrike(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, _, _ := keptWithWork(t)
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(tk.copy, "scratch.txt")
	to := filepath.Join(dir, "paths", "scratch.txt")
	before, err := readCopyState(from)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{Kind: discardStepMove, From: from, To: to}); err != nil {
		t.Fatal(err)
	}
	last, _, err = s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := moveAsRead(from, to, before, "scratch.txt"); err != nil {
		t.Fatal(err)
	}
	// Simulate the rename half of putBack succeeding, then a stop before strike.
	if err := os.MkdirAll(filepath.Dir(from), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(to, from); err != nil {
		t.Fatal(err)
	}
	if err := putBack(dir, last); err != nil {
		t.Fatalf("putBack after a restored-but-unstruck move = %v", err)
	}
	if raw, err := os.ReadFile(from); err != nil || string(raw) != string(want) {
		t.Fatalf("after resume, %s reads %q, %v; want %q", from, raw, err, want)
	}
	j, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || len(j.Steps) != 0 {
		t.Fatalf("after resume, journal steps = %+v, %v; want none", j.Steps, err)
	}
}

// A move whose aside and original are both gone cannot be verified as
// restored: putBack refuses and keeps the step (P6).
func TestADiscardPutBackRefusesAMoveWhoseAsideAndOriginalAreBothGone(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, _, _ := keptWithWork(t)
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(tk.copy, "scratch.txt")
	to := filepath.Join(dir, "paths", "scratch.txt")
	before, err := readCopyState(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{Kind: discardStepMove, From: from, To: to}); err != nil {
		t.Fatal(err)
	}
	last, _, err = s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := moveAsRead(from, to, before, "scratch.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(to); err != nil {
		t.Fatal(err)
	}
	err = putBack(dir, last)
	if err == nil {
		t.Fatal("putBack with aside and original both gone = nil; want a refusal")
	}
	if errors.Is(err, ErrAsideStands) {
		t.Fatalf("putBack with both gone = %v; want a verify refusal, not ErrAsideStands", err)
	}
	j, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || len(j.Steps) != 1 || j.Steps[0].Kind != discardStepMove {
		t.Fatalf("after a both-gone putBack, journal = %+v, %v; want the move step kept", j, err)
	}
}

// writeBack put-back follows PRD section 6's three states: exact content is
// taken out, a missing path is struck, and anything else stays.
func TestADiscardPutBackTakesOutOrLeavesAWriteBackInThreeStates(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, _, _ := keptWithWork(t)
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	exact := filepath.Join(tk.copy, "exact.txt")
	missing := filepath.Join(tk.copy, "missing.txt")
	changed := filepath.Join(tk.copy, "changed.txt")
	writeFile(t, exact, "discard wrote\n")
	writeFile(t, changed, "written meanwhile\n")
	held := []byte("discard wrote\n")
	if err := appendDiscardStep(dir, &last, discardStep{Kind: discardStepWriteBack, Path: exact, Held: held}); err != nil {
		t.Fatal(err)
	}
	last, _, err = s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{Kind: discardStepWriteBack, Path: missing, Held: held}); err != nil {
		t.Fatal(err)
	}
	last, _, err = s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{Kind: discardStepWriteBack, Path: changed, Held: held}); err != nil {
		t.Fatal(err)
	}
	last, _, err = s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if err := putBack(dir, last); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(exact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact writeBack still stands: %v", err)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing writeBack path appeared: %v", err)
	}
	if raw, err := os.ReadFile(changed); err != nil || string(raw) != "written meanwhile\n" {
		t.Fatalf("changed writeBack reads %q, %v; want it left", raw, err)
	}
	j, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || len(j.Steps) != 0 {
		t.Fatalf("after writeBack putBack, journal steps = %+v, %v; want none", j.Steps, err)
	}
}

// A writeBack of non-UTF-8 bytes journals Held so it still matches what
// stands on disk: putBack takes the exact content out (P6).
func TestADiscardPutBackTakesOutAWriteBackOfNonUTF8Bytes(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, _, _ := keptWithWork(t)
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tk.copy, "blob.bin")
	held := []byte{0xff, 0xfe, 'a', 0x00, 'b'}
	if err := os.WriteFile(path, held, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{Kind: discardStepWriteBack, Path: path, Held: held}); err != nil {
		t.Fatal(err)
	}
	last, found, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || !found {
		t.Fatalf("standingDiscard = %v, %v", found, err)
	}
	if len(last.Steps) != 1 || !bytes.Equal(last.Steps[0].Held, held) {
		t.Fatalf("journalled Held = %v; want %v round-tripped", last.Steps[0].Held, held)
	}
	if err := putBack(dir, last); err != nil {
		t.Fatalf("putBack of non-UTF-8 writeBack = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("non-UTF-8 writeBack still stands: %v", err)
	}
	j, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || len(j.Steps) != 0 {
		t.Fatalf("after non-UTF-8 putBack, journal steps = %+v, %v; want none", j.Steps, err)
	}
}
