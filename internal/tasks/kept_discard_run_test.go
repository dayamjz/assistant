package tasks

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
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

// A confirmed discard of a run's worktree removes the worktree past its
// point of no return: the path is gone, the gate no longer lists it, and
// the discard directory with its finish journal remains for the outcome
// owner (PRD section 6; P6).
func TestAConfirmedDiscardRemovesARunsWorktreePastItsPointOfNoReturn(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	const runID = "run-discard-wt"
	if _, err := s.store.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: cleanupRepository, Branch: "topic", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	gateRepo := filepath.Join(t.TempDir(), "gate.git")
	rawGit(t, filepath.Dir(gateRepo), "clone", "-q", "--bare", s.upstream, gateRepo)
	copyPath := s.home.Worktree(cleanupRepository, runID)
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, gateRepo, "worktree", "add", "-q", "--detach", copyPath, "main")
	commit(t, copyPath, "left.txt", "left by a body\n", "a body's commit")
	kept, err := s.cleaner.Keep(ctx, Kept{Run: runID, Reason: string(ReasonCopyChanged)})
	if err != nil || !kept.Recorded {
		t.Fatalf("Keep = %+v, %v; want the run's copy kept", kept, err)
	}
	shown, err := s.cleaner.Loss(ctx, kept.Entry, gateRepo)
	if err != nil {
		t.Fatalf("Loss: %v", err)
	}

	err = s.cleaner.Discard(ctx, kept.Entry, gateRepo, shown.Digest)
	if err != nil {
		t.Fatalf("Discard = %v; want the worktree removed", err)
	}
	if _, err := os.Lstat(copyPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the worktree still stands at %s after Discard: %v", copyPath, err)
	}
	dir, err := s.home.Discarding(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	j, found, err := s.cleaner.standingDiscard(copyPath)
	if err != nil || !found {
		t.Fatalf("standingDiscard after Discard = found %v, %v; want the discard directory left for the outcome", found, err)
	}
	if !journalHasStep(j, discardStepFinish) {
		t.Fatalf("journal steps = %+v; want a finish step", j.Steps)
	}
	if ents, err := os.ReadDir(filepath.Join(dir, "paths")); err == nil && len(ents) > 0 {
		t.Fatalf("set-aside paths still stand under the discard directory: %v", ents)
	}
	listed := rawGit(t, gateRepo, "worktree", "list", "--porcelain")
	if strings.Contains(listed, copyPath) {
		t.Fatalf("gate still lists the removed worktree:\n%s", listed)
	}
}

// worktreeRemovalBegun is false while the path, gate record, and step-3
// tree still stand; true once any of them is gone; and an unreadable
// re-read is an error (PRD step 4; P6).
func TestWorktreeRemovalBegunReadsPathGateRecordAndTree(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	const runID = "run-begun-wt"
	if _, err := s.store.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: cleanupRepository, Branch: "topic", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	gateRepo := filepath.Join(t.TempDir(), "gate.git")
	rawGit(t, filepath.Dir(gateRepo), "clone", "-q", "--bare", s.upstream, gateRepo)
	copyPath := s.home.Worktree(cleanupRepository, runID)
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, gateRepo, "worktree", "add", "-q", "--detach", copyPath, "main")
	commit(t, copyPath, "left.txt", "left by a body\n", "a body's commit")
	kept, err := s.cleaner.Keep(ctx, Kept{Run: runID, Reason: string(ReasonCopyChanged)})
	if err != nil || !kept.Recorded {
		t.Fatalf("Keep = %+v, %v", kept, err)
	}

	begun, err := s.cleaner.worktreeRemovalBegun(ctx, gateRepo, copyPath)
	if err != nil || begun {
		t.Fatalf("intact worktree begun = %v, %v; want false, nil", begun, err)
	}

	// Dirty tree with path and record still standing: removal had begun.
	writeFile(t, filepath.Join(copyPath, "dirt.txt"), "not as step 3 left it\n")
	begun, err = s.cleaner.worktreeRemovalBegun(ctx, gateRepo, copyPath)
	if err != nil || !begun {
		t.Fatalf("dirty worktree begun = %v, %v; want true, nil", begun, err)
	}
	if err := os.Remove(filepath.Join(copyPath, "dirt.txt")); err != nil {
		t.Fatal(err)
	}
	begun, err = s.cleaner.worktreeRemovalBegun(ctx, gateRepo, copyPath)
	if err != nil || begun {
		t.Fatalf("after cleaning dirt begun = %v, %v; want false, nil", begun, err)
	}

	// Unreadable .git: claim stays owed (read failure, not begun).
	dotGit := filepath.Join(copyPath, ".git")
	if err := os.Chmod(dotGit, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dotGit, 0o644) })
	begun, err = s.cleaner.worktreeRemovalBegun(ctx, gateRepo, copyPath)
	if err == nil || begun {
		t.Fatalf("unreadable .git begun = %v, %v; want false, read error", begun, err)
	}
	if err := os.Chmod(dotGit, 0o644); err != nil {
		t.Fatal(err)
	}

	bare, err := openBareForTest(t, gateRepo)
	if err != nil {
		t.Fatal(err)
	}
	records, err := bare.WorktreeRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var record string
	for _, r := range records {
		if r.Worktree == copyPath || filepath.Base(r.Worktree) == filepath.Base(copyPath) {
			record = r.Record
			break
		}
	}
	if record == "" {
		// WorktreeRecords may EvalSymlinks the path; match by walking.
		parent, err := filepath.EvalSymlinks(filepath.Dir(copyPath))
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(parent, filepath.Base(copyPath))
		for _, r := range records {
			if r.Worktree == want {
				record = r.Record
				break
			}
		}
	}
	if record == "" {
		t.Fatalf("no gate record for %s among %+v", copyPath, records)
	}
	if err := os.RemoveAll(record); err != nil {
		t.Fatal(err)
	}
	begun, err = s.cleaner.worktreeRemovalBegun(ctx, gateRepo, copyPath)
	if err != nil || !begun {
		t.Fatalf("after gate record removed begun = %v, %v; want true, nil", begun, err)
	}

	// Path gone (record already gone): still begun.
	if err := os.RemoveAll(copyPath); err != nil {
		t.Fatal(err)
	}
	begun, err = s.cleaner.worktreeRemovalBegun(ctx, gateRepo, copyPath)
	if err != nil || !begun {
		t.Fatalf("after path removed begun = %v, %v; want true, nil", begun, err)
	}
}

// A standing worktree discard whose path still stands but whose gate
// record is gone is past PONR: finish removes the path by force rather
// than putting set-aside back (PRD step 4; P6).
func TestAStandingWorktreeDiscardFinishesWhenTheGateRecordIsGone(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	const runID = "run-standing-wt"
	if _, err := s.store.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: cleanupRepository, Branch: "topic", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	gateRepo := filepath.Join(t.TempDir(), "gate.git")
	rawGit(t, filepath.Dir(gateRepo), "clone", "-q", "--bare", s.upstream, gateRepo)
	copyPath := s.home.Worktree(cleanupRepository, runID)
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, gateRepo, "worktree", "add", "-q", "--detach", copyPath, "main")
	commit(t, copyPath, "left.txt", "left by a body\n", "a body's commit")
	kept, err := s.cleaner.Keep(ctx, Kept{Run: runID, Reason: string(ReasonCopyChanged)})
	if err != nil || !kept.Recorded {
		t.Fatalf("Keep = %+v, %v", kept, err)
	}
	shown, err := s.cleaner.Loss(ctx, kept.Entry, gateRepo)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := s.cleaner.beginDiscard(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	last, found, err := s.cleaner.standingDiscard(copyPath)
	if err != nil || !found {
		t.Fatalf("standingDiscard after begin = found %v, %v", found, err)
	}
	if err := s.cleaner.setAsideLoss(ctx, dir, &last, copyPath, shown); err != nil {
		t.Fatal(err)
	}
	if err := s.cleaner.discardClean(ctx, kept.Entry, gateRepo, shown); err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{Kind: discardStepPONR, From: copyPath}); err != nil {
		t.Fatal(err)
	}
	bare, err := openBareForTest(t, gateRepo)
	if err != nil {
		t.Fatal(err)
	}
	records, err := bare.WorktreeRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(copyPath))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(parent, filepath.Base(copyPath))
	var record string
	for _, r := range records {
		if r.Worktree == want {
			record = r.Record
			break
		}
	}
	if record == "" {
		t.Fatalf("no gate record for %s among %+v", want, records)
	}
	if err := os.RemoveAll(record); err != nil {
		t.Fatal(err)
	}

	if err := s.cleaner.Discard(ctx, kept.Entry, gateRepo, shown.Digest); err != nil {
		t.Fatalf("Discard with path standing and record gone = %v; want finish", err)
	}
	if _, err := os.Lstat(copyPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("worktree still stands after finish: %v", err)
	}
	j, found, err := s.cleaner.standingDiscard(copyPath)
	if err != nil || !found || !journalHasStep(j, discardStepFinish) {
		t.Fatalf("journal after finish = %+v found %v err %v", j, found, err)
	}
}

// When git refuses the non-force worktree remove while the path, gate
// record, and step-3 tree still stand, Discard strikes ponr, puts
// set-aside back, and leaves the copy (PRD step 4; P6). A locked worktree
// is the refusal that does not dirty the tree, so begun stays false.
func TestAWorktreeDiscardPutsBackWhenGitRefusesRemovalWhileIntact(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	const runID = "run-putback-wt"
	if _, err := s.store.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: cleanupRepository, Branch: "topic", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	gateRepo := filepath.Join(t.TempDir(), "gate.git")
	rawGit(t, filepath.Dir(gateRepo), "clone", "-q", "--bare", s.upstream, gateRepo)
	copyPath := s.home.Worktree(cleanupRepository, runID)
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, gateRepo, "worktree", "add", "-q", "--detach", copyPath, "main")
	commit(t, copyPath, "left.txt", "left by a body\n", "a body's commit")
	kept, err := s.cleaner.Keep(ctx, Kept{Run: runID, Reason: string(ReasonCopyChanged)})
	if err != nil || !kept.Recorded {
		t.Fatalf("Keep = %+v, %v", kept, err)
	}
	// Lock before Loss: the admin record's locked file is sealed into the
	// digest, and locking afterward would look like a confirm mismatch.
	rawGit(t, gateRepo, "worktree", "lock", copyPath)
	shown, err := s.cleaner.Loss(ctx, kept.Entry, gateRepo)
	if err != nil {
		t.Fatal(err)
	}

	err = s.cleaner.Discard(ctx, kept.Entry, gateRepo, shown.Digest)
	if err == nil {
		t.Fatal("Discard of a locked worktree = nil; want the remove refused and put-back")
	}
	if !strings.Contains(err.Error(), "removing the worktree") {
		t.Fatalf("Discard error = %v; want it name the refused remove", err)
	}
	got, readErr := os.ReadFile(filepath.Join(copyPath, "left.txt"))
	if readErr != nil || string(got) != "left by a body\n" {
		t.Fatalf("after put-back left.txt = %q, %v; want the body's content restored", got, readErr)
	}
	if _, found, standErr := s.cleaner.standingDiscard(copyPath); standErr != nil || found {
		t.Fatalf("standingDiscard after refuse = found %v, %v; want the discard directory cleared", found, standErr)
	}
	listed := rawGit(t, gateRepo, "worktree", "list", "--porcelain")
	if !strings.Contains(listed, copyPath) && !strings.Contains(listed, filepath.Base(copyPath)) {
		t.Fatalf("gate no longer lists the intact worktree after refuse:\n%s", listed)
	}
}

// finishDiscard must not RemoveAll a standing worktree while its gate
// record still stands: a locked worktree makes force-remove refuse, and
// the path and record must both remain (PRD step 5; P6).
func TestFinishDiscardRefusesRemoveAllWhileTheGateRecordStands(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	const runID = "run-finish-locked-wt"
	if _, err := s.store.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: cleanupRepository, Branch: "topic", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	gateRepo := filepath.Join(t.TempDir(), "gate.git")
	rawGit(t, filepath.Dir(gateRepo), "clone", "-q", "--bare", s.upstream, gateRepo)
	copyPath := s.home.Worktree(cleanupRepository, runID)
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, gateRepo, "worktree", "add", "-q", "--detach", copyPath, "main")
	commit(t, copyPath, "left.txt", "left by a body\n", "a body's commit")
	kept, err := s.cleaner.Keep(ctx, Kept{Run: runID, Reason: string(ReasonCopyChanged)})
	if err != nil || !kept.Recorded {
		t.Fatalf("Keep = %+v, %v", kept, err)
	}
	shown, err := s.cleaner.Loss(ctx, kept.Entry, gateRepo)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := s.cleaner.beginDiscard(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	last, found, err := s.cleaner.standingDiscard(copyPath)
	if err != nil || !found {
		t.Fatalf("standingDiscard after begin = found %v, %v", found, err)
	}
	if err := s.cleaner.setAsideLoss(ctx, dir, &last, copyPath, shown); err != nil {
		t.Fatal(err)
	}
	if err := s.cleaner.discardClean(ctx, kept.Entry, gateRepo, shown); err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{Kind: discardStepPONR, From: copyPath}); err != nil {
		t.Fatal(err)
	}
	rawGit(t, gateRepo, "worktree", "lock", copyPath)

	err = s.cleaner.finishDiscard(ctx, gateRepo, dir, &last)
	if err == nil {
		t.Fatal("finishDiscard of a locked worktree = nil; want refusal while the gate record stands")
	}
	if !strings.Contains(err.Error(), "gate record still stands") {
		t.Fatalf("finishDiscard error = %v; want it name the standing gate record", err)
	}
	if _, err := os.Lstat(copyPath); err != nil {
		t.Fatalf("worktree path gone after refused finish: %v", err)
	}
	record := worktreeRecordPath(t, gateRepo, copyPath)
	if _, err := os.Lstat(record); err != nil {
		t.Fatalf("gate record gone after refused finish: %v", err)
	}
}

// When the worktree path is already gone but the gate record still stands,
// finishDiscard prunes that record rather than reporting success with an
// orphan left behind (PRD step 5; P6).
func TestFinishDiscardPrunesAnOrphanGateRecordWhenThePathIsGone(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	const runID = "run-finish-orphan-wt"
	if _, err := s.store.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: cleanupRepository, Branch: "topic", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	gateRepo := filepath.Join(t.TempDir(), "gate.git")
	rawGit(t, filepath.Dir(gateRepo), "clone", "-q", "--bare", s.upstream, gateRepo)
	copyPath := s.home.Worktree(cleanupRepository, runID)
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, gateRepo, "worktree", "add", "-q", "--detach", copyPath, "main")
	commit(t, copyPath, "left.txt", "left by a body\n", "a body's commit")
	kept, err := s.cleaner.Keep(ctx, Kept{Run: runID, Reason: string(ReasonCopyChanged)})
	if err != nil || !kept.Recorded {
		t.Fatalf("Keep = %+v, %v", kept, err)
	}
	shown, err := s.cleaner.Loss(ctx, kept.Entry, gateRepo)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := s.cleaner.beginDiscard(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	last, found, err := s.cleaner.standingDiscard(copyPath)
	if err != nil || !found {
		t.Fatalf("standingDiscard after begin = found %v, %v", found, err)
	}
	if err := s.cleaner.setAsideLoss(ctx, dir, &last, copyPath, shown); err != nil {
		t.Fatal(err)
	}
	if err := s.cleaner.discardClean(ctx, kept.Entry, gateRepo, shown); err != nil {
		t.Fatal(err)
	}
	if err := appendDiscardStep(dir, &last, discardStep{Kind: discardStepPONR, From: copyPath}); err != nil {
		t.Fatal(err)
	}
	record := worktreeRecordPath(t, gateRepo, copyPath)
	if err := os.RemoveAll(copyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(record); err != nil {
		t.Fatalf("gate record should still stand after path removal: %v", err)
	}

	if err := s.cleaner.finishDiscard(ctx, gateRepo, dir, &last); err != nil {
		t.Fatalf("finishDiscard with path gone and record standing = %v; want prune", err)
	}
	if _, err := os.Lstat(record); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("gate record still stands after finish: %v", err)
	}
	listed := rawGit(t, gateRepo, "worktree", "list", "--porcelain")
	if strings.Contains(listed, copyPath) {
		t.Fatalf("gate still lists the removed worktree:\n%s", listed)
	}
	if !journalHasStep(last, discardStepFinish) {
		t.Fatalf("journal steps = %+v; want a finish step", last.Steps)
	}
}

// worktreeRecordPath returns the gate admin record directory for copyPath.
func worktreeRecordPath(t *testing.T, gateRepo, copyPath string) string {
	t.Helper()
	bare, err := openBareForTest(t, gateRepo)
	if err != nil {
		t.Fatal(err)
	}
	records, err := bare.WorktreeRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(copyPath))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(parent, filepath.Base(copyPath))
	for _, r := range records {
		if r.Worktree == want {
			return r.Record
		}
	}
	t.Fatalf("no gate record for %s among %+v", want, records)
	return ""
}

// openBareForTest opens gatePath as a bare repository for test helpers.
func openBareForTest(t *testing.T, gatePath string) (*vcs.Repository, error) {
	t.Helper()
	return vcs.OpenBare(context.Background(), gatePath)
}
