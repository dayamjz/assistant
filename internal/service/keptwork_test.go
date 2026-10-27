package service_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/service"
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
		// The copy goes first and the removal is marked after it, which is
		// what closes the entry, so both are waited for: a copy gone with
		// its entry still listed is the state between the two, and only one
		// that outlasts the deadline is the defect.
		deadline := time.Now().Add(30 * time.Second)
		for {
			_, err := os.Stat(entry.CopyPath)
			gone := os.IsNotExist(err)
			listed := statusOf(t, under.client, subject).KeptWork
			if gone && len(listed) == 0 {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("after its work landed the copy at %s is gone: %v, and status lists kept work %+v; want it gone and nothing listed; the log reads:\n%s",
					entry.CopyPath, gone, listed, readServiceLog(t, h))
			}
			time.Sleep(20 * time.Millisecond)
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

// errStoppedBeforeMark stands in for a service that stopped between a
// copy's removal and the mark on its cleanup record.
var errStoppedBeforeMark = errors.New("the service stopped between the removal and the mark")

// keptEntryOf records one kept run copy through a real run and returns its
// entry, as the earlier tests do.
func keptEntryOf(t *testing.T, h *home.Home, subject string) store.KeptWork {
	t.Helper()
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
	return entry
}

// A removal on a proof that a stopped service never marked is completed by
// the next open from the recorded proof, and the entry closes with the mark:
// the run's kept copy is recorded, its commit lands upstream, and the second
// service's recovery removes the copy on the remote-branch proof and is
// interrupted before the mark, leaving a proof, no copy and an open entry.
// The third service's recovery finds the copy gone and the proof unmarked
// on the run's cleanup record, completes the mark, and the entry reads
// closed with that proof. Without recovery reading the record for a copy
// that is gone, the entry stays open across every later restart.
func TestARemovalStoppedBeforeItsMarkIsCompletedAtTheNextOpenAndClosesTheEntry(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	entry := keptEntryOf(t, h, subject)
	git(t, subject, "push", "--quiet", "origin", "work")

	interrupted := service.WithCleanupInterruptForTest(t, options(t, h), func() error { return errStoppedBeforeMark })
	withServiceOptions(t, interrupted, func(under serviceUnderTest) {
		deadline := time.Now().Add(30 * time.Second)
		for !strings.Contains(readServiceLog(t, h), errStoppedBeforeMark.Error()) {
			if !time.Now().Before(deadline) {
				t.Fatalf("the reclaim was never interrupted; the log reads:\n%s", readServiceLog(t, h))
			}
			time.Sleep(20 * time.Millisecond)
		}
		if _, err := os.Stat(entry.CopyPath); !os.IsNotExist(err) {
			t.Fatalf("the interrupted removal left the copy at %s: %v", entry.CopyPath, err)
		}
		if listed := statusOf(t, under.client, subject).KeptWork; len(listed) != 1 {
			t.Fatalf("after the interrupted removal status lists %+v, want the entry still open, which is the window under test", listed)
		}
	})

	withService(t, h, func(under serviceUnderTest) {
		deadline := time.Now().Add(30 * time.Second)
		for len(statusOf(t, under.client, subject).KeptWork) != 0 {
			if !time.Now().Before(deadline) {
				t.Fatalf("the next open left the entry open over a copy removed on a recorded proof; the log reads:\n%s", readServiceLog(t, h))
			}
			time.Sleep(20 * time.Millisecond)
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
		t.Fatalf("entry %d reads %+v (%v), want it closed as removed on the remote-branch proof", entry.ID, closed, err)
	}
	cleanup, err := records.RunCleanup(t.Context(), closed.RunID.Or(""))
	if err != nil || !cleanup.RemovedAt.IsKnown() {
		t.Fatalf("the run's cleanup record reads %+v (%v), want the removal marked", cleanup, err)
	}
}

// A kept copy that disappears with no proof recorded for it is reported and
// never closed: the program does not close kept work it cannot show was
// removed on a proof. The copy is removed by hand, outside any cleanup, and
// the next open names the entry in the log and still lists it.
func TestAKeptCopyGoneWithNoProofIsReportedAndLeftOpen(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	entry := keptEntryOf(t, h, subject)
	if err := os.RemoveAll(entry.CopyPath); err != nil {
		t.Fatal(err)
	}

	withService(t, h, func(under serviceUnderTest) {
		want := fmt.Sprintf("kept work entry %d names the copy at %s, which is gone with no recorded proof", entry.ID, entry.CopyPath)
		deadline := time.Now().Add(30 * time.Second)
		for !strings.Contains(readServiceLog(t, h), want) {
			if !time.Now().Before(deadline) {
				t.Fatalf("the next open did not report the entry; the log reads:\n%s", readServiceLog(t, h))
			}
			time.Sleep(20 * time.Millisecond)
		}
		if listed := statusOf(t, under.client, subject).KeptWork; len(listed) != 1 || !listed[0].Open() {
			t.Fatalf("status lists %+v, want the entry still open", listed)
		}
	})
}

// PRD principle P15's restart rule for an eject's removal reservation: one
// whose owner, the eject process, is gone is released when the next service
// opens, so a repository is not left refusing every run because an eject
// died after reserving; one whose owner still runs is left, because that
// eject may be removing the gate now. The dead owner is a real process
// identified while it ran and then ended; the live one is this test's own
// process.
func TestARemovalReservationWhoseEjectIsGoneIsReleasedAtOpen(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)

	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	dead, found, err := agents.Identify(sleeper.Process.Pid)
	if err != nil || !found {
		_ = sleeper.Process.Kill()
		_ = sleeper.Wait()
		t.Skipf("this platform cannot identify a process (%v), so an owner's death cannot be shown", err)
	}
	_ = sleeper.Process.Kill()
	_ = sleeper.Wait()
	self, found, err := agents.Identify(os.Getpid())
	if err != nil || !found {
		t.Fatalf("identifying this test's own process: %v", err)
	}

	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	if err := records.ReserveRepositoryRemoval(t.Context(), repository.ID, store.RemovalOwner{PID: dead.PID, Start: dead.Start}); err != nil {
		t.Fatal(err)
	}
	alive, err := records.UpsertRepository(t.Context(), store.Repository{ID: "other", WorkingPath: newSubject(t),
		UpstreamURL: "https://example.test/other.git", DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := records.ReserveRepositoryRemoval(t.Context(), alive.ID, store.RemovalOwner{PID: self.PID, Start: self.Start}); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()

	withService(t, h, func(under serviceUnderTest) {
		started := startRunSkipping(t, under.client, subject, afterIntent()...)
		if started.Record.ID == "" {
			t.Fatal("a run of the repository whose eject is gone did not start")
		}
	})
	records, err = store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = records.Close() }()
	reserved, err := records.RepositoryRemovals(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(reserved) != 1 || reserved[0].Repository != alive.ID {
		t.Fatalf("after the open the reservations are %+v, want only the live eject's, of %s", reserved, alive.ID)
	}
	if !strings.Contains(readServiceLog(t, h), "released the removal reservation of repository "+repository.ID) {
		t.Fatalf("the service log does not say the dead eject's reservation was released:\n%s", readServiceLog(t, h))
	}
}

// Recovery's release of a dead eject's reservation is anchored on the owner
// it read: a new eject that takes the reservation over between recovery's
// read and its release keeps it, and a run of the repository is still
// refused, so no run copy can appear under a gate that eject is removing.
// The takeover is made through the seam between the read and the release,
// by the store's own reservation for a live owner, this test's process.
func TestAReservationTakenOverAfterRecoveryReadsItStands(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P6)

	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	dead, found, err := agents.Identify(sleeper.Process.Pid)
	_ = sleeper.Process.Kill()
	_ = sleeper.Wait()
	if err != nil || !found {
		t.Skipf("this platform cannot identify a process (%v), so an owner's death cannot be shown", err)
	}
	self, found, err := agents.Identify(os.Getpid())
	if err != nil || !found {
		t.Fatalf("identifying this test's own process: %v", err)
	}
	live := store.RemovalOwner{PID: self.PID, Start: self.Start}

	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = records.Close() }()
	if err := records.ReserveRepositoryRemoval(t.Context(), repository.ID, store.RemovalOwner{PID: dead.PID, Start: dead.Start}); err != nil {
		t.Fatal(err)
	}
	var takeover error
	opts := service.WithBeforeStaleRemovalReleaseForTest(options(t, h), func() {
		takeover = records.ReserveRepositoryRemoval(context.Background(), repository.ID, live)
	})
	withServiceOptions(t, opts, func(under serviceUnderTest) {
		if takeover != nil {
			t.Fatalf("the new eject's reservation inside recovery's window: %v", takeover)
		}
		var run machine.Run
		err := under.client.Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{
			Working: machine.Working{WorkingPath: subject}, Intent: "started while an eject holds the repository",
		}, &run)
		if err == nil || !strings.Contains(err.Error(), "being removed") {
			t.Fatalf("a run while the new eject holds the repository = %v, want it refused", err)
		}
	})
	reserved, err := records.RepositoryRemovals(t.Context())
	if err != nil || len(reserved) != 1 || reserved[0].Owner != live {
		t.Fatalf("after recovery the reservations are %+v (%v), want the new eject's alone", reserved, err)
	}
}

// Status lists kept work whose owner names no repository in every working
// copy's answer, because such an entry belongs to no working copy and would
// otherwise reach no status at all, and keeps out an entry of another
// repository. The repository-less entry is an investigation task's, the
// shape the real refusal writes for an investigation started with no
// repository; both are written through the store's own accessor.
func TestStatusListsKeptWorkWhoseOwnerNamesNoRepository(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P15)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	ctx := t.Context()
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	const task = "keptnorepoxx"
	copyPath, err := h.TaskCopy(task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateTask(ctx, store.Task{ID: task, Shape: store.TaskInvestigation, Project: "one",
		Mode: "headless", WorktreePath: copyPath}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	unowned, _, err := records.RecordKeptWork(ctx, store.KeptCopy{TaskID: task, CopyPath: copyPath, Holds: []string{"aaaa"}, Reason: "unproven", Against: "dddd"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.UpsertRepository(ctx, store.Repository{ID: "other", WorkingPath: newSubject(t),
		UpstreamURL: "https://example.test/other.git", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateRun(ctx, store.Run{ID: "run-of-other", RepositoryID: "other", Branch: "topic", SubmittedHead: "bbbb",
		Base: "bbbb", Intent: "validate", IntentSource: "push", Build: build, ConfigDigest: "cfg"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.RecordKeptWork(ctx, store.KeptCopy{RunID: "run-of-other", CopyPath: "/w/run-of-other", Holds: []string{"bbbb"}, Reason: "unproven", Against: "dddd"}); err != nil {
		t.Fatal(err)
	}
	_ = records.Close()

	withService(t, h, func(under serviceUnderTest) {
		listed := statusOf(t, under.client, subject).KeptWork
		if len(listed) != 1 || listed[0].ID != unowned.ID {
			t.Fatalf("status lists kept work %+v, want the repository-less entry %d alone", listed, unowned.ID)
		}
	})
}
