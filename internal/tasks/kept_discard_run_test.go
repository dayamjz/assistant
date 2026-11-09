package tasks

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// A confirmed discard of a task's clone removes the clone past its point
// of no return: the copy's path is empty, the moved clone is gone, set-aside
// paths are gone, and the discard directory with its finish journal remains
// for the outcome owner (PRD section 6 steps 3-5; P6).
func TestAConfirmedDiscardRemovesATaskClonePastItsPointOfNoReturn(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, shown := keptWithWork(t)
	ctx := context.Background()

	err := s.cleaner.Discard(ctx, entry, "", shown.Digest)
	if err != nil {
		t.Fatalf("Discard = %v; want the clone removed", err)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the copy still stands at %s after Discard: %v", tk.copy, err)
	}
	dir, err := s.home.Discarding(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	j, found, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || !found {
		t.Fatalf("standingDiscard after Discard = found %v, %v; want the discard directory left for the outcome", found, err)
	}
	if !journalHasStep(j, discardStepFinish) {
		t.Fatalf("journal steps = %+v; want a finish step", j.Steps)
	}
	if _, err := os.Lstat(filepath.Join(dir, "clone")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the moved clone still stands under the discard directory: %v", err)
	}
	if ents, err := os.ReadDir(filepath.Join(dir, "paths")); err == nil && len(ents) > 0 {
		t.Fatalf("set-aside paths still stand under the discard directory: %v", ents)
	}
}

// Discard refuses when the digest named is not what Loss seals now, and
// leaves the copy and nothing at the discard directory's name.
func TestADiscardRefusesADigestThatIsNotTheLossNow(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, shown := keptWithWork(t)
	ctx := context.Background()
	before := treeAndGitState(t, tk.copy)

	err := s.cleaner.Discard(ctx, entry, "", shown.Digest+"x")
	if !errors.Is(err, ErrConfirmMismatch) {
		t.Fatalf("Discard with a wrong digest = %v; want ErrConfirmMismatch", err)
	}
	if now := treeAndGitState(t, tk.copy); now != before {
		t.Fatalf("a refused Discard changed the copy:\n%s\nwant\n%s", now, before)
	}
	if _, found, err := s.cleaner.standingDiscard(tk.copy); err != nil || found {
		t.Fatalf("standingDiscard after a digest refusal = found %v, %v; want nothing", found, err)
	}
}

// A staged path the set-aside does not yet handle refuses Discard after
// moving earlier paths; putBack restores them so the copy holds what it
// held when the loss was read (P6).
func TestADiscardPutsBackWhenSetAsideRefusesMidWay(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, _ := keptWithWork(t)
	ctx := context.Background()
	tracked := filepath.Join(tk.copy, "own.txt")
	if err := os.WriteFile(tracked, []byte("staged content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "add", "own.txt")
	if err := os.WriteFile(tracked, []byte("working tree after staging\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shown, err := s.cleaner.Loss(ctx, entry, "")
	if err != nil {
		t.Fatal(err)
	}
	var hasStaged, hasChanged bool
	for _, f := range shown.Files {
		if f.Kind == LostStaged && f.Path == "own.txt" {
			hasStaged = true
		}
		if f.Kind == LostChanged && f.Path == "own.txt" {
			hasChanged = true
		}
	}
	if !hasStaged || !hasChanged {
		t.Fatalf("Loss.Files = %+v; want own.txt both staged and changed", shown.Files)
	}
	wantOwn, err := os.ReadFile(tracked)
	if err != nil {
		t.Fatal(err)
	}
	wantScratch, err := os.ReadFile(filepath.Join(tk.copy, "scratch.txt"))
	if err != nil {
		t.Fatal(err)
	}

	err = s.cleaner.Discard(ctx, entry, "", shown.Digest)
	if err == nil {
		t.Fatal("Discard with a staged path = nil; want a refusal and put-back")
	}
	if raw, err := os.ReadFile(tracked); err != nil || string(raw) != string(wantOwn) {
		t.Fatalf("own.txt after refused Discard = %q, %v; want the working-tree bytes put back", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(tk.copy, "scratch.txt")); err != nil || string(raw) != string(wantScratch) {
		t.Fatalf("scratch.txt after refused Discard = %q, %v; want it put back", raw, err)
	}
	if _, err := os.Lstat(tk.copy); err != nil {
		t.Fatalf("the copy was removed on a refused Discard: %v", err)
	}
}

// A discard that stopped after its point of no return finishes deletion on
// the next Discard: the moved clone and set-aside paths are removed, and
// the journal records finish (PRD: resume past PONR).
func TestADiscardResumesPastItsPointOfNoReturnAndFinishes(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, shown := keptWithWork(t)
	ctx := context.Background()

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
	to := filepath.Join(dir, "clone")
	if err := appendDiscardStep(dir, &last, discardStep{
		Kind: discardStepPONR, From: tk.copy, To: to,
	}); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(tk.copy, to); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("setup: copy should be gone from its path")
	}

	if err := s.cleaner.Discard(ctx, entry, "", shown.Digest); err != nil {
		t.Fatalf("Discard resume = %v", err)
	}
	if _, err := os.Lstat(to); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("moved clone still stands after resume: %v", err)
	}
	j, found, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || !found || !journalHasStep(j, discardStepFinish) {
		t.Fatalf("after resume, journal = %+v found %v err %v; want finish", j, found, err)
	}
}

// A journalled ponr whose rename never happened, with the copy still at
// its path, is before the point of no return: Discard puts everything
// back and carries out the removal afresh (PRD: clone at its path has
// not passed).
func TestADiscardWithAnUnreachedPONRPutsBackAndContinues(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, shown := keptWithWork(t)
	ctx := context.Background()
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cleaner.setAsideLoss(ctx, dir, &last, tk.copy, shown); err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{
		Kind: discardStepPONR, From: tk.copy, To: filepath.Join(dir, "clone"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.cleaner.Discard(ctx, entry, "", shown.Digest); err != nil {
		t.Fatalf("Discard with unreached ponr = %v; want put-back then a fresh removal", err)
	}
	if _, err := os.Lstat(tk.copy); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("after a full Discard, the copy should be gone: %v", err)
	}
}

// When ponr is journalled but the clone is neither at its path nor in the
// discard directory, Discard refuses without deleting the set-asides (P6).
func TestADiscardRefusesWhenPONRSideCannotBeRead(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, shown := keptWithWork(t)
	ctx := context.Background()
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cleaner.setAsideLoss(ctx, dir, &last, tk.copy, shown); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(dir, "clone")
	if err := appendDiscardStep(dir, &last, discardStep{
		Kind: discardStepPONR, From: tk.copy, To: to,
	}); err != nil {
		t.Fatal(err)
	}
	// Rename never happened; something else removed the copy.
	if err := os.RemoveAll(tk.copy); err != nil {
		t.Fatal(err)
	}
	aside := filepath.Join(dir, "paths", "scratch.txt")
	before, err := os.ReadFile(aside)
	if err != nil {
		t.Fatal(err)
	}

	err = s.cleaner.Discard(ctx, entry, "", shown.Digest)
	if !errors.Is(err, ErrDiscardStateUnreadable) {
		t.Fatalf("Discard = %v; want ErrDiscardStateUnreadable", err)
	}
	if raw, err := os.ReadFile(aside); err != nil || string(raw) != string(before) {
		t.Fatalf("set-aside after refusal = %q, %v; want it left untouched", raw, err)
	}
}

// pastPointOfNoReturn is decided from where the clone stands, never from
// a missing copyPath alone (review of #860): in the discard directory is
// past; at the copy path is not; both gone without finish is unreadable.
func TestPastPointOfNoReturnReadsTheCloneNotTheJournalAlone(t *testing.T) {
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
	if err := s.cleaner.setAsideLoss(ctx, dir, &last, tk.copy, shown); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(dir, "clone")
	if err := appendDiscardStep(dir, &last, discardStep{
		Kind: discardStepPONR, From: tk.copy, To: to,
	}); err != nil {
		t.Fatal(err)
	}

	// Unreached ponr, copy still at path: not past.
	past, err := pastPointOfNoReturn(tk.copy, last)
	if past || err != nil {
		t.Fatalf("unreached ponr with copy at path = %v, %v; want not past", past, err)
	}

	if err := renameNoReplace(tk.copy, to); err != nil {
		t.Fatal(err)
	}
	// Clone in the discard directory: past, even with an impostor at the path.
	if err := os.Mkdir(tk.copy, 0o700); err != nil {
		t.Fatal(err)
	}
	past, err = pastPointOfNoReturn(tk.copy, last)
	if !past || err != nil {
		t.Fatalf("clone in discard dir with impostor at path = %v, %v; want past", past, err)
	}
	if err := os.Remove(tk.copy); err != nil {
		t.Fatal(err)
	}

	// Both gone, no finish: unreadable (do not finish-delete set-asides).
	if err := os.RemoveAll(to); err != nil {
		t.Fatal(err)
	}
	aside := filepath.Join(dir, "paths", "scratch.txt")
	if _, err := os.Lstat(aside); err != nil {
		t.Fatalf("setup: set-aside must still stand: %v", err)
	}
	past, err = pastPointOfNoReturn(tk.copy, last)
	if past || !errors.Is(err, ErrDiscardStateUnreadable) {
		t.Fatalf("both gone without finish = %v, %v; want ErrDiscardStateUnreadable", past, err)
	}
	if _, err := os.Lstat(aside); err != nil {
		t.Fatalf("unreadable state must not have touched the set-aside: %v", err)
	}

	// Finish journalled, both gone: past (retry deletes).
	if err := appendDiscardStep(dir, &last, discardStep{Kind: discardStepFinish}); err != nil {
		t.Fatal(err)
	}
	past, err = pastPointOfNoReturn(tk.copy, last)
	if !past || err != nil {
		t.Fatalf("finish journalled, both gone = %v, %v; want past", past, err)
	}
	// Finish journalled, To gone, impostor at the path: still past (never put back).
	if err := os.Mkdir(tk.copy, 0o700); err != nil {
		t.Fatal(err)
	}
	past, err = pastPointOfNoReturn(tk.copy, last)
	if !past || err != nil {
		t.Fatalf("finish journalled with impostor at path = %v, %v; want past", past, err)
	}
}

// After finish is journalled, a second Discard with an impostor at the
// copy path only retries deletion: it must not put set-asides back into
// the impostor (PRD step 5; review of #860 round 2).
func TestADiscardAfterFinishDoesNotPutBackIntoAnImpostor(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, shown := keptWithWork(t)
	ctx := context.Background()
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cleaner.setAsideLoss(ctx, dir, &last, tk.copy, shown); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(dir, "clone")
	aside := filepath.Join(dir, "paths", "scratch.txt")
	asideBytes, err := os.ReadFile(aside)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{
		Kind: discardStepPONR, From: tk.copy, To: to,
	}); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(tk.copy, to); err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{Kind: discardStepFinish}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(to); err != nil {
		t.Fatal(err)
	}
	// Crash before paths/ deletion; impostor at the copy path.
	if err := os.Mkdir(tk.copy, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(tk.copy, "impostor.txt"), "not the clone\n")

	if err := s.cleaner.Discard(ctx, entry, "", shown.Digest); err != nil {
		t.Fatalf("Discard after finish with impostor = %v", err)
	}
	if _, err := os.Lstat(aside); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("paths/ should be finished-deleted, not put back; aside still stands (%v)", err)
	}
	if raw, err := os.ReadFile(filepath.Join(tk.copy, "impostor.txt")); err != nil || string(raw) != "not the clone\n" {
		t.Fatalf("impostor changed or gained put-back bytes: %q, %v (aside was %q)", raw, err, asideBytes)
	}
	if _, err := os.Lstat(filepath.Join(tk.copy, "scratch.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("set-aside was put back into the impostor")
	}
}

// An impostor at the copy's path after a successful rename does not make
// Discard put back into it or delete the moved clone via the pre-PONR arm:
// the clone in the discard directory is past, so finish removes it (P6).
func TestADiscardPastPONRFinishesEvenIfSomethingStandsAtTheCopyPath(t *testing.T) {
	principles.Cite(t, principles.P6)
	s, tk, entry, shown := keptWithWork(t)
	ctx := context.Background()
	dir, err := s.cleaner.beginDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	last, _, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cleaner.setAsideLoss(ctx, dir, &last, tk.copy, shown); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(dir, "clone")
	if err := appendDiscardStep(dir, &last, discardStep{
		Kind: discardStepPONR, From: tk.copy, To: to,
	}); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(tk.copy, to); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tk.copy, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(tk.copy, "impostor.txt"), "not the clone\n")

	if err := s.cleaner.Discard(ctx, entry, "", shown.Digest); err != nil {
		t.Fatalf("Discard with impostor at path = %v", err)
	}
	if _, err := os.Lstat(to); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("moved clone still stands: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(tk.copy, "impostor.txt")); err != nil {
		t.Fatalf("impostor should be left (not putBack target): %v", err)
	}
	j, found, err := s.cleaner.standingDiscard(tk.copy)
	if err != nil || !found || !journalHasStep(j, discardStepFinish) {
		t.Fatalf("journal after finish = %+v found %v err %v", j, found, err)
	}
}
