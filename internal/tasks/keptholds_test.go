package tasks

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// PRD principle P15 and section 8's kept work record, "what it holds that
// the default branch lacks": a delivery copy cloned from an upstream whose
// main already carries a feature branch's commits, holding one commit of the
// task's own, is kept as unproven, and its entry's Lacks is that one commit
// and none of the inherited ones, though the copy's references stand on
// them; Holds, the entry's identity, is what the references stand on.
func TestAKeptEntryListsOnlyWhatTheDefaultBranchLacks(t *testing.T) {
	principles.Cite(t, principles.P15)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()

	// The upstream's main carries a feature branch's two commits, merged as
	// a fast-forward, and the feature branch still stands there.
	seed := s.deliveryTask(t, "feature")
	inherited := []string{
		commit(t, seed.copy, "a.txt", "a\n", "the feature's first commit"),
		commit(t, seed.copy, "b.txt", "b\n", "the feature's second commit"),
	}
	s.push(t, seed.copy, "feature")
	mainTip := s.mergeIntoMain(t, "feature", false)
	if mainTip != inherited[1] {
		t.Fatalf("main moved to %s, want the feature's tip %s", mainTip, inherited[1])
	}

	// The task's copy is cut from that upstream, keeps a branch on the
	// inherited commits beside its own, and commits once.
	tk := s.deliveryTask(t, "task/kept")
	rawGit(t, tk.copy, "branch", "feature", inherited[1])
	own := commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit")

	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Recorded {
		t.Fatalf("Keep = %+v, %v; want a new entry", got, err)
	}
	if !slices.Equal(got.Entry.Lacks, []string{own}) {
		t.Fatalf("the entry lacks %v, want only the task's own commit %s; the default branch already has %v",
			got.Entry.Lacks, own, inherited)
	}
	if !got.Entry.Established() || got.Entry.Against.Or("") != mainTip || got.Entry.Unestablished.IsKnown() {
		t.Fatalf("the entry = %+v, want it computed against the upstream's main at %s", got.Entry, mainTip)
	}
	if !slices.Contains(got.Entry.Holds, inherited[1]) || !slices.Contains(got.Entry.Holds, own) {
		t.Fatalf("the entry holds %v, want the copy's reference tips %s and %s", got.Entry.Holds, inherited[1], own)
	}
	if refs := rawGit(t, tk.copy, "for-each-ref", "refs/assistant/"); refs != "" {
		t.Fatalf("the fetched default branch tip was left in the copy: %s", refs)
	}
}

// PRD principles P15 and P6: a kept copy whose default branch cannot be
// read is an entry that says its set is unestablished, with the read's
// account, listing nothing as lacking rather than every commit as lost or
// none as safe. The upstream is taken away after the copy is cut, so the
// proofs and the entry's own read both fail on it. The entry's identity is
// still what the copy's references and HEAD stand on, and no tip is
// claimed.
//
// And the identity is the copy's state, not the upstream's reachability:
// once the upstream is back, Keep of the unchanged copy writes no second
// entry but establishes the first, once, with the set and the tip; a third
// Keep returns it unchanged. Without the identity on the copy's state the
// second Keep wrote a second open entry for one copy.
func TestAKeptEntrySaysSoWhenTheDefaultBranchCannotBeReadAndIsEstablishedLater(t *testing.T) {
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P6)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/unread")
	base := rawGit(t, tk.copy, "rev-parse", "main")
	own := commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit")
	if err := os.Rename(s.upstream, s.upstream+".gone"); err != nil {
		t.Fatal(err)
	}

	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Recorded || got.NothingKept {
		t.Fatalf("Keep = %+v, %v; want a new entry", got, err)
	}
	if got.Entry.Established() || got.Entry.Against.IsKnown() || got.Entry.Lacks != nil ||
		!strings.Contains(got.Entry.Unestablished.Or(""), "the upstream could not be read") {
		t.Fatalf("the entry = %+v, want it unestablished with the failed read's account and nothing listed as lacking", got.Entry)
	}
	if !slices.Equal(got.Entry.Holds, []string{base, own}) {
		t.Fatalf("the unestablished entry holds %v, want what the copy's references and HEAD stand on, %v",
			got.Entry.Holds, []string{base, own})
	}

	if err := os.Rename(s.upstream+".gone", s.upstream); err != nil {
		t.Fatal(err)
	}
	again, err := s.cleaner.Keep(ctx, kept)
	if err != nil || again.Recorded || !again.Established || again.Entry.ID != got.Entry.ID {
		t.Fatalf("Keep with the upstream back = %+v, %v; want entry %d established and no second entry", again, err, got.Entry.ID)
	}
	if !again.Entry.Established() || again.Entry.Against.Or("") != base || !slices.Equal(again.Entry.Lacks, []string{own}) ||
		!again.Entry.Unestablished.IsKnown() {
		t.Fatalf("the established entry = %+v, want %s lacking against main at %s with the write's account kept", again.Entry, own, base)
	}
	if open, err := s.store.OpenKeptWork(ctx); err != nil || len(open) != 1 || open[0].ID != got.Entry.ID {
		t.Fatalf("the open kept work is %+v, %v; want the one entry", open, err)
	}
	if third, err := s.cleaner.Keep(ctx, kept); err != nil || third.Recorded || third.Established || third.Entry.ID != got.Entry.ID {
		t.Fatalf("a third Keep = %+v, %v; want the same entry, neither written nor established again", third, err)
	}
}

// An answered entry is sealed whole, PRD section 8's "its one revision is
// the answer": a copy kept during an outage whose entry a person answered
// before the upstream came back is, at the next Keep, that entry as it
// stands, unestablished still, with nothing written and no error. Without
// the refusal the answered row gained a basis.
func TestAnAnsweredEntryIsNotEstablishedLater(t *testing.T) {
	principles.Cite(t, principles.P15)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/answered")
	commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit")
	if err := os.Rename(s.upstream, s.upstream+".gone"); err != nil {
		t.Fatal(err)
	}
	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Recorded || got.Entry.Established() {
		t.Fatalf("Keep = %+v, %v; want a new unestablished entry", got, err)
	}
	if _, err := s.store.AnswerKeptWork(ctx, got.Entry.ID, store.KeptCarryOn, store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.upstream+".gone", s.upstream); err != nil {
		t.Fatal(err)
	}
	again, err := s.cleaner.Keep(ctx, kept)
	if err != nil || again.Recorded || again.Established || again.NothingKept || again.Entry.ID != got.Entry.ID {
		t.Fatalf("Keep after the answer = %+v, %v; want the answered entry as it stands", again, err)
	}
	if again.Entry.Established() || again.Entry.Answer.Or("") != store.KeptCarryOn {
		t.Fatalf("the answered entry reads %+v, want it unestablished with its answer", again.Entry)
	}
	if open, err := s.store.OpenKeptWork(ctx); err != nil || len(open) != 0 {
		t.Fatalf("the open kept work is %+v, %v; want none", open, err)
	}
}

// An entry already standing for the copy state is read before NothingKept
// is decided: a clean investigation copy on main, kept while the upstream
// was unavailable, has an unestablished entry; once the upstream is back the
// same copy is not NothingKept but that entry, established with nothing
// lacking. It stays open until the cleanup decision removes the copy on the
// scratch proof, which closes it as removed on that proof in the removal's
// transaction, the one program closing the record allows. Without the read
// before the decision the entry stayed open and unestablished behind a
// NothingKept answer.
func TestAnEntryForACopyHoldingNothingIsEstablishedAndClosedByTheCleanup(t *testing.T) {
	principles.Cite(t, principles.P15)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.investigationTask(t)
	mainTip := rawGit(t, tk.copy, "rev-parse", "main")
	if err := os.Rename(s.upstream, s.upstream+".gone"); err != nil {
		t.Fatal(err)
	}
	kept := Kept{Task: tk.id, Reason: KeptPromotionWindow, Detail: "the window passed"}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Recorded || got.NothingKept || got.Entry.Established() {
		t.Fatalf("Keep with the upstream away = %+v, %v; want an unestablished entry, which is the case under test", got, err)
	}
	if err := os.Rename(s.upstream+".gone", s.upstream); err != nil {
		t.Fatal(err)
	}
	again, err := s.cleaner.Keep(ctx, kept)
	if err != nil || again.NothingKept || again.Recorded || !again.Established || again.Entry.ID != got.Entry.ID {
		t.Fatalf("Keep with the upstream back = %+v, %v; want entry %d established and not NothingKept", again, err, got.Entry.ID)
	}
	if !again.Entry.Established() || again.Entry.Against.Or("") != mainTip || again.Entry.Lacks == nil || len(again.Entry.Lacks) != 0 ||
		!again.Entry.Open() {
		t.Fatalf("the entry = %+v, want it open, established against %s with nothing lacking", again.Entry, mainTip)
	}

	s.storeReport(t, tk.id)
	cleaned, err := s.cleanUp(t, tk.id)
	s.removedOn(t, tk, cleaned, err, store.CleanupProofScratch)
	closed, err := s.store.KeptWorkEntry(ctx, got.Entry.ID)
	if err != nil || closed.Open() || closed.Closure.Or("") != store.KeptRemovedOnProof || closed.ClosedProof.Or("") != store.CleanupProofScratch {
		t.Fatalf("after the removal the entry reads %+v, %v; want it closed as removed on the scratch proof", closed, err)
	}
}

// PRD principle P15 and section 6's scratch copy, as the one computation
// rather than a second rule: an investigation copy kept at the end of its
// promotion window that holds nothing the default branch lacks records no
// entry; the same copy holding a file no commit records is an entry that
// lacks no commit, computed against main; and holding a commit of its own,
// an entry lacking that commit alone.
func TestACopyHoldingNothingTheDefaultBranchLacksKeepsNothing(t *testing.T) {
	principles.Cite(t, principles.P15)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.investigationTask(t)
	mainTip := rawGit(t, tk.copy, "rev-parse", "main")
	keep := func() KeptEntry {
		t.Helper()
		got, err := s.cleaner.Keep(ctx, Kept{Task: tk.id, Reason: KeptPromotionWindow, Detail: "the window passed"})
		if err != nil {
			t.Fatalf("Keep: %v", err)
		}
		return got
	}

	if got := keep(); !got.NothingKept || got.Recorded || got.Against != mainTip || got.Entry.ID != 0 {
		t.Fatalf("Keep over an unchanged scratch copy = %+v, want nothing kept against main at %s", got, mainTip)
	}
	if open, err := s.store.OpenKeptWork(ctx); err != nil || len(open) != 0 {
		t.Fatalf("an unchanged scratch copy recorded kept work %+v, %v", open, err)
	}

	if err := os.WriteFile(filepath.Join(tk.copy, "notes.txt"), []byte("what was found\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	untracked := keep()
	if untracked.NothingKept || !untracked.Recorded || untracked.Entry.Lacks == nil || len(untracked.Entry.Lacks) != 0 ||
		untracked.Entry.Against.Or("") != mainTip {
		t.Fatalf("Keep over a copy holding an untracked file = %+v, want an entry lacking no commit against %s", untracked, mainTip)
	}

	own := commit(t, tk.copy, "notes.txt", "what was found\n", "the investigation's own commit")
	committed := keep()
	if !committed.Recorded || !slices.Equal(committed.Entry.Lacks, []string{own}) || committed.Entry.Against.Or("") != mainTip {
		t.Fatalf("Keep over a copy holding a commit of its own = %+v, want an entry lacking %s alone", committed, own)
	}
}

// NothingKept claims only what vcs.Inventory sees, and that inventory does
// not see ignored files: a copy on main whose only unique content is a file
// git is configured to ignore reads NothingKept and records no entry. This
// pins the gap doc.go and KeptEntry.NothingKept state, so a change in what
// the inventory sees is noticed here rather than widening the claim
// silently.
func TestNothingKeptDoesNotSeeAnIgnoredFile(t *testing.T) {
	s := newCleanupSubject(t, forgetest.Script{})
	tk := s.investigationTask(t)
	if err := os.WriteFile(filepath.Join(tk.copy, ".git", "info", "exclude"), []byte("notes.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tk.copy, "notes.txt"), []byte("unique notes git ignores\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := rawGit(t, tk.copy, "status", "--porcelain"); status != "" {
		t.Fatalf("the ignored file shows in git status, so this is not the case under test: %q", status)
	}
	got, err := s.cleaner.Keep(context.Background(), Kept{Task: tk.id, Reason: KeptPromotionWindow, Detail: "the window passed"})
	if err != nil || !got.NothingKept {
		t.Fatalf("Keep over a copy whose only unique content is ignored = %+v, %v; the inventory's gap is that it reads NothingKept", got, err)
	}
}

// A stash is uncommitted state, kept for as such and never listed as a
// commit the default branch lacks: a copy on main with one stashed edit is
// kept for the stash, and its entry is established with nothing lacking,
// while the stash commit is among what the copy holds, because it is part
// of the copy's state. Without the stash left out of the comparison the
// entry listed the stash's commits as work at risk.
func TestAStashIsKeptAsUncommittedStateAndNotListedAsLacking(t *testing.T) {
	principles.Cite(t, principles.P15)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk := s.deliveryTask(t, "task/stash")
	mainTip := rawGit(t, tk.copy, "rev-parse", "main")
	if err := os.WriteFile(filepath.Join(tk.copy, "f.txt"), []byte("an edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, tk.copy, "stash", "push", "-q")
	stash := rawGit(t, tk.copy, "rev-parse", "refs/stash")

	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonCopyStash))
	if !ok {
		t.Fatal("a stash refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Recorded || got.NothingKept {
		t.Fatalf("Keep = %+v, %v; want a new entry for the stash", got, err)
	}
	if !got.Entry.Established() || got.Entry.Against.Or("") != mainTip || len(got.Entry.Lacks) != 0 {
		t.Fatalf("the entry = %+v, want it established against %s with nothing lacking", got.Entry, mainTip)
	}
	if !slices.Contains(got.Entry.Holds, stash) {
		t.Fatalf("the entry holds %v, want the stash %s among the copy's state", got.Entry.Holds, stash)
	}
}

// For a task delivered under the local posture the tip the entry is
// computed against is the one the repository's working copy holds for its
// default branch, where that task's work lands, and not the upstream's: the
// working copy's main is advanced past the upstream's, and the entry names
// the working copy's tip and lacks only the task's own commit.
func TestALocalTasksEntryIsComputedAgainstTheWorkingCopysDefaultBranch(t *testing.T) {
	principles.Cite(t, principles.P15)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	tk, primary := s.localTask(t, "task/local")
	upstreamTip := rawGit(t, tk.copy, "rev-parse", "main")
	localTip := commit(t, primary, "local.txt", "landed locally\n", "a commit only the working copy's main has")
	own := commit(t, tk.copy, "own.txt", "the task's own work\n", "the task's own commit")

	_, err := s.cleanUp(t, tk.id)
	kept, ok := KeptOf(refusedWith(t, err, ReasonUnproven))
	if !ok {
		t.Fatal("an unproven refusal is not kept work")
	}
	got, err := s.cleaner.Keep(ctx, kept)
	if err != nil || !got.Recorded {
		t.Fatalf("Keep = %+v, %v; want a new entry", got, err)
	}
	if got.Entry.Against.Or("") != localTip || got.Entry.Against.Or("") == upstreamTip {
		t.Fatalf("the entry is against %v, want the working copy's main at %s and not the upstream's at %s",
			got.Entry.Against, localTip, upstreamTip)
	}
	if !slices.Equal(got.Entry.Lacks, []string{own}) {
		t.Fatalf("the entry lacks %v, want the task's own commit %s alone", got.Entry.Lacks, own)
	}
}

// PRD principle P15 for a run's copy, a linked worktree of the gate's
// repository detached at a commit the default branch already holds: kept
// for a file no commit records, its entry lacks no commit, because nothing
// it reaches is missing from main; with a commit on top, that commit alone.
// The worktree's own references are the gate's and are not read as the
// copy's.
func TestARunCopysEntryListsOnlyWhatTheDefaultBranchLacks(t *testing.T) {
	principles.Cite(t, principles.P15)
	s := newCleanupSubject(t, forgetest.Script{})
	ctx := context.Background()
	const runID = "run-kept-copy"
	if _, err := s.store.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: cleanupRepository, Branch: "topic", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	gateRepo := filepath.Join(t.TempDir(), "gate.git")
	rawGit(t, filepath.Dir(gateRepo), "clone", "-q", "--bare", s.upstream, gateRepo)
	rawGit(t, gateRepo, "branch", "elsewhere", "main")
	mainTip := rawGit(t, gateRepo, "rev-parse", "main")
	copyPath := s.home.Worktree(cleanupRepository, runID)
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, gateRepo, "worktree", "add", "-q", "--detach", copyPath, "main")
	if err := os.WriteFile(filepath.Join(copyPath, "left.txt"), []byte("left by a body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keep := func(reason CleanupReason) KeptEntry {
		t.Helper()
		got, err := s.cleaner.Keep(ctx, Kept{Run: runID, Reason: string(reason)})
		if err != nil {
			t.Fatalf("Keep: %v", err)
		}
		return got
	}

	untracked := keep(ReasonCopyUntracked)
	if !untracked.Recorded || untracked.Entry.Lacks == nil || len(untracked.Entry.Lacks) != 0 ||
		untracked.Entry.Against.Or("") != mainTip || !slices.Equal(untracked.Entry.Holds, []string{mainTip}) {
		t.Fatalf("Keep over a run copy on main holding an untracked file = %+v, want an entry lacking no commit against %s",
			untracked, mainTip)
	}
	own := commit(t, copyPath, "left.txt", "left by a body\n", "a body's commit")
	committed := keep(ReasonUnproven)
	if !committed.Recorded || !slices.Equal(committed.Entry.Lacks, []string{own}) {
		t.Fatalf("Keep over a run copy holding a commit of its own = %+v, want %s alone", committed, own)
	}
}
