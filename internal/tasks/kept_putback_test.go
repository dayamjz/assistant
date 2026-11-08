package tasks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// A discard that set a path aside with a journalled move puts it back from
// the journal: the path stands again holding what it held, the aside is
// empty of it, and the journal's step is struck. Today's tree has no
// putBack and no Steps on the journal (PRD section 6, refusal before the
// point of no return).
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
