package service_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/vcs"
)

// PRD principle P15's "kept work is never silent", at the run-copy site,
// through the real socket: a run whose copy stands on a commit that landed
// nowhere keeps the copy, and the refusal becomes a kept work entry naming
// the run, its branch, the project, the reason and the commit the copy
// holds, which status lists the moment the run's answer is back. Without
// the entry's writer status lists nothing.
func TestARunCopyKeptForItsWorkIsAKeptWorkEntryStatusLists(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	submitted := git(t, subject, "rev-parse", "HEAD")

	withService(t, h, func(under serviceUnderTest) {
		started := startRunSkipping(t, under.client, subject, afterIntent()...)
		run := runByID(t, under.client, started.Record.ID)
		if run.Cleanup == nil || !run.Cleanup.Kept() {
			t.Fatalf("the run's copy reads %+v, want it kept, so that there is kept work to record", run.Cleanup)
		}
		status := statusOf(t, under.client, subject)
		if len(status.KeptWork) != 1 {
			t.Fatalf("status lists kept work %+v, want the run's one entry", status.KeptWork)
		}
		entry := status.KeptWork[0]
		if entry.RunID.Or("") != run.Record.ID || entry.TaskID.IsKnown() || entry.RepositoryID.Or("") != repository.ID ||
			entry.Branch.Or("") != "work" || entry.Reason != string(tasks.ReasonUnproven) ||
			len(entry.Holds) != 1 || entry.Holds[0] != submitted || entry.Answered() {
			t.Fatalf("the entry = %+v, want run %s's unanswered copy on work, unproven, holding %s", entry, run.Record.ID, submitted)
		}
		if entry.CopyPath != h.Worktree(repository.ID, run.Record.ID) {
			t.Fatalf("the entry names the copy at %s, want %s", entry.CopyPath, h.Worktree(repository.ID, run.Record.ID))
		}
	})
}

// PRD section 11's restart sentence: "a copy no run owns that holds unlanded
// work becomes a kept-work entry rather than staying on disk unrecorded". A
// finished run's copy left standing by an earlier service, here holding a
// file no commit records and with no entry about it, is found by the next
// service's recovery and recorded as kept work, without the copy or the file
// being touched; a service opened after that finds the same copy unchanged
// and writes no second entry. Without the recovery's reclaim the first
// service's status never lists the entry.
func TestARestartTurnsAFinishedRunsUnlandedCopyIntoOneKeptWorkEntry(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	const runID = "run-left-by-an-earlier-service"
	copyPath := plantFinishedRunCopy(t, h, subject, repository.ID, runID)
	left := copyPath + "/left-behind.txt"
	if err := writeFile(left, "a worker wrote this and never committed it\n"); err != nil {
		t.Fatal(err)
	}

	waitKept := func(under serviceUnderTest) store.KeptWork {
		deadline := time.Now().Add(30 * time.Second)
		for {
			status := statusOf(t, under.client, subject)
			if len(status.KeptWork) > 0 {
				if len(status.KeptWork) != 1 {
					t.Fatalf("status lists kept work %+v, want one entry", status.KeptWork)
				}
				return status.KeptWork[0]
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("recovery recorded no kept work for the copy at %s", copyPath)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	var first store.KeptWork
	withService(t, h, func(under serviceUnderTest) {
		first = waitKept(under)
	})
	if first.RunID.Or("") != runID || first.Reason != string(tasks.ReasonCopyUntracked) || first.CopyPath != copyPath || first.Answered() {
		t.Fatalf("the entry = %+v, want run %s's copy kept for an untracked file", first, runID)
	}
	if _, err := os.Stat(left); err != nil {
		t.Fatalf("the kept copy's file was touched: %v", err)
	}

	withService(t, h, func(under serviceUnderTest) {
		deadline := time.Now().Add(30 * time.Second)
		for !strings.Contains(readServiceLog(t, h), "is already kept work entry") {
			if !time.Now().Before(deadline) {
				t.Fatalf("the second recovery never asked of the copy again; the log reads:\n%s", readServiceLog(t, h))
			}
			time.Sleep(20 * time.Millisecond)
		}
		if again := waitKept(under); again.ID != first.ID {
			t.Fatalf("the second recovery wrote entry %+v, want the first, %d, alone", again, first.ID)
		}
	})
}

// plantFinishedRunCopy records a run that an earlier service finished, as
// failed, and stands its isolated copy where the home puts it, cut from the
// gate's repository at the subject's commit the way the service cuts one. It
// returns the copy's path.
func plantFinishedRunCopy(t *testing.T, h *home.Home, subject, repositoryID, runID string) string {
	t.Helper()
	ctx := t.Context()
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer func() { _ = records.Close() }()
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	head := git(t, subject, "rev-parse", "HEAD")
	if _, err := records.CreateRun(ctx, store.Run{
		ID: runID, RepositoryID: repositoryID, Branch: "work", SubmittedHead: head, Base: head,
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
	}); err != nil {
		t.Fatalf("recording the run: %v", err)
	}
	if _, err := records.TransitionRun(ctx, runID, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := records.TransitionRun(ctx, runID, []store.RunStatus{store.RunRunning}, store.RunFailed); err != nil {
		t.Fatal(err)
	}
	bare, err := vcs.OpenBare(ctx, gateRepositoryOf(t, h, subject), vcs.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the gate repository: %v", err)
	}
	if err := bare.Fetch(ctx, vcs.FetchSpec{Remote: subject, Refspecs: []string{"+refs/heads/work:refs/assistant/runs/" + runID}}); err != nil {
		t.Fatalf("bringing the run's commit into the gate repository: %v", err)
	}
	copyPath := h.Worktree(repositoryID, runID)
	if _, err := bare.AddWorktree(ctx, vcs.WorktreeSpec{Path: copyPath, Commit: "refs/assistant/runs/" + runID}); err != nil {
		t.Fatalf("cutting the run's copy: %v", err)
	}
	return copyPath
}

// readServiceLog reads what the home's service logged.
func readServiceLog(t *testing.T, h *home.Home) string {
	t.Helper()
	text, err := os.ReadFile(h.ServiceLog())
	if err != nil {
		t.Fatalf("reading the service log: %v", err)
	}
	return string(text)
}

// A kept copy whose work later lands is removed on that proof and its entry
// closes with it, as the program's closure and never as a discard: the run's
// copy is kept and recorded, the commit it stands on is then pushed to the
// upstream branch, and the next service's recovery removes the copy on the
// remote-branch proof. Status no longer lists the entry, and the entry
// records the closure and the proof with no answer. Without the closure the
// removed copy's entry stays listed.
func TestAKeptCopyWhoseWorkLandsIsRemovedAndItsEntryClosed(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	var entry store.KeptWork
	withService(t, h, func(under serviceUnderTest) {
		started := startRunSkipping(t, under.client, subject, afterIntent()...)
		runByID(t, under.client, started.Record.ID)
		status := statusOf(t, under.client, subject)
		if len(status.KeptWork) != 1 {
			t.Fatalf("status lists kept work %+v, want the run's one entry", status.KeptWork)
		}
		entry = status.KeptWork[0]
	})
	git(t, subject, "push", "--quiet", "origin", "work")

	withService(t, h, func(under serviceUnderTest) {
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(entry.CopyPath); os.IsNotExist(err) {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("recovery left the copy at %s standing after its work landed; the log reads:\n%s", entry.CopyPath, readServiceLog(t, h))
			}
			time.Sleep(20 * time.Millisecond)
		}
		if listed := statusOf(t, under.client, subject).KeptWork; len(listed) != 0 {
			t.Fatalf("status still lists kept work %+v for a copy removed on a proof", listed)
		}
	})
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = records.Close() }()
	closed, err := records.KeptWorkEntry(t.Context(), entry.ID)
	if err != nil || closed.Closure.Or("") != store.KeptRemovedOnProof ||
		closed.ClosedProof.Or("") != store.CleanupProofRemoteBranch || closed.Answered() {
		t.Fatalf("entry %d reads %+v (%v), want it closed as removed on the remote-branch proof with no answer", entry.ID, closed, err)
	}
}
