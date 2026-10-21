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
// task's own, is kept as unproven, and its entry lists that one commit and
// none of the inherited ones, though the copy's references stand on them.
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
	if !slices.Equal(got.Entry.Holds, []string{own}) {
		t.Fatalf("the entry holds %v, want only the task's own commit %s; the default branch already has %v",
			got.Entry.Holds, own, inherited)
	}
	if !got.Entry.Established() || got.Entry.Against.Or("") != mainTip || got.Entry.Unestablished.IsKnown() {
		t.Fatalf("the entry = %+v, want it computed against the upstream's main at %s", got.Entry, mainTip)
	}
	if refs := rawGit(t, tk.copy, "for-each-ref", "refs/assistant/"); refs != "" {
		t.Fatalf("the fetched default branch tip was left in the copy: %s", refs)
	}
}

// PRD principles P15 and P6: a kept copy whose default branch cannot be
// read is an entry that says its set is unestablished, with the read's
// account, rather than one listing every commit as lost or none as safe.
// The upstream is taken away after the copy is cut, so the proofs and the
// entry's own read both fail on it. What the entry lists then is what the
// copy's references and HEAD stand on, the task's own commit among them,
// and nothing claims it was computed against a tip.
func TestAKeptEntrySaysSoWhenTheDefaultBranchCannotBeRead(t *testing.T) {
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
	if got.Entry.Established() || got.Entry.Against.IsKnown() ||
		!strings.Contains(got.Entry.Unestablished.Or(""), "the upstream could not be read") {
		t.Fatalf("the entry = %+v, want it unestablished with the failed read's account", got.Entry)
	}
	if !slices.Equal(got.Entry.Holds, []string{base, own}) {
		t.Fatalf("the unestablished entry holds %v, want what the copy's references and HEAD stand on, %v",
			got.Entry.Holds, []string{base, own})
	}
}

// PRD principle P15 and section 6's scratch copy, as the one computation
// rather than a second rule: an investigation copy kept at the end of its
// promotion window that holds nothing the default branch lacks records no
// entry; the same copy holding a file no commit records is an entry that
// lists no commit, computed against main; and holding a commit of its own,
// an entry listing that commit alone.
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
	if untracked.NothingKept || !untracked.Recorded || len(untracked.Entry.Holds) != 0 ||
		untracked.Entry.Against.Or("") != mainTip {
		t.Fatalf("Keep over a copy holding an untracked file = %+v, want an entry listing no commit against %s", untracked, mainTip)
	}

	own := commit(t, tk.copy, "notes.txt", "what was found\n", "the investigation's own commit")
	committed := keep()
	if !committed.Recorded || !slices.Equal(committed.Entry.Holds, []string{own}) || committed.Entry.Against.Or("") != mainTip {
		t.Fatalf("Keep over a copy holding a commit of its own = %+v, want an entry listing %s alone", committed, own)
	}
}

// PRD principle P15 for a run's copy, a linked worktree of the gate's
// repository detached at a commit the default branch already holds: kept
// for a file no commit records, its entry lists no commit, because nothing
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
	if !untracked.Recorded || len(untracked.Entry.Holds) != 0 || untracked.Entry.Against.Or("") != mainTip {
		t.Fatalf("Keep over a run copy on main holding an untracked file = %+v, want an entry listing no commit against %s",
			untracked, mainTip)
	}
	own := commit(t, copyPath, "left.txt", "left by a body\n", "a body's commit")
	committed := keep(ReasonUnproven)
	if !committed.Recorded || !slices.Equal(committed.Entry.Holds, []string{own}) {
		t.Fatalf("Keep over a run copy holding a commit of its own = %+v, want %s alone", committed, own)
	}
}
