package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// cleanupService is a service built by hand with what the two task-copy
// cleanup sites read: the store, the home, the log and the cleaner, and no
// terminal seam, so its reader resolves every terminal Unobserved. No
// socket is bound and no run is driven, because the sites are reached from
// a run's settling and from a report's storing, and what is established
// here is the call and what it says, not the walk that reaches it.
func cleanupService(t *testing.T) (*Service, *store.Store, *home.Home) {
	t.Helper()
	ctx := t.Context()
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	seedDatabase(t, h.Database())
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = records.Close() })
	log, err := h.OpenLog(0)
	if err != nil {
		t.Fatalf("opening the log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	s := &Service{home: h, store: records, log: log}
	// A code host whose command line cannot be run: no site here reaches a
	// proof, because every task copy refuses on its terminal first.
	s.forge = forge.NewGitHubHost(redact.New(), forge.WithBinary(filepath.Join(t.TempDir(), "absent-gh")))
	if s.cleaner, err = tasks.NewCleaner(tasks.CleanupDeps{Home: h, Store: records, Forge: s.forge}); err != nil {
		t.Fatalf("building the cleaner: %v", err)
	}
	return s, records, h
}

// serviceLog reads what the service logged.
func serviceLog(t *testing.T, h *home.Home) string {
	t.Helper()
	text, err := os.ReadFile(h.ServiceLog())
	if err != nil {
		t.Fatalf("reading the service log: %v", err)
	}
	return string(text)
}

// A merge record written for a run attributed to a delivery task is what
// calls the task copy's cleanup, with the terminal as the service's reader
// resolved it, which for a service with no terminal seam is Unobserved and
// refuses as such; a run no merge record landed calls nothing, and a task
// the run is not attributed to is not touched.
func TestAMergeRecordCallsTheLandedTasksCleanup(t *testing.T) {
	t.Parallel()
	s, records, h := cleanupService(t)
	ctx := context.Background()
	repository, err := records.UpsertRepository(ctx, store.Repository{
		ID: "subject", WorkingPath: filepath.Join(h.Root(), "checkout"), UpstreamURL: "https://github.com/owner/name.git", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	copyPath, _ := h.TaskCopy("landedaaaaaa")
	if _, err := records.CreateTask(ctx, store.Task{
		ID: "landedaaaaaa", Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: copyPath,
		RepositoryID: store.Known(repository.ID), Branch: store.Known("work"),
	}, store.Disposition{}); err != nil {
		t.Fatalf("recording the task: %v", err)
	}
	if _, err := records.BeginTaskAttempt(ctx, "landedaaaaaa", 0); err != nil {
		t.Fatalf("beginning the attempt: %v", err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	newRun := func(id string) store.Run {
		if _, err := records.CreateRun(ctx, store.Run{
			ID: id, RepositoryID: repository.ID, Branch: "work", SubmittedHead: "aaaa", Base: "bbbb",
			Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
		}); err != nil {
			t.Fatalf("recording run %s: %v", id, err)
		}
		run, err := records.TransitionRun(ctx, id, []store.RunStatus{store.RunPending}, store.RunRunning)
		if err != nil {
			t.Fatalf("starting run %s: %v", id, err)
		}
		return run
	}
	unlanded := newRun("run-1")
	if unlanded, err = records.TransitionRun(ctx, unlanded.ID, []store.RunStatus{store.RunRunning}, store.RunPassed); err != nil {
		t.Fatal(err)
	}
	s.cleanUpLanded(ctx, unlanded)
	if log := serviceLog(t, h); strings.Contains(log, "isolated copy of task") {
		t.Fatalf("a run with no merge record called a cleanup: %s", log)
	}

	landed := newRun("run-2")
	if landed, err = records.TransitionRunOnPullRequest(ctx, landed.ID, []store.RunStatus{store.RunRunning}, store.RunPassed,
		store.PullRequestRead{PullRequest: 7, Head: "aaaa", End: store.PullRequestMerged}, store.HoldClosure{}); err != nil {
		t.Fatal(err)
	}
	s.cleanUpLanded(ctx, landed)
	log := serviceLog(t, h)
	if !strings.Contains(log, "isolated copy of task landedaaaaaa is left standing ("+string(tasks.ReasonTerminalUnobserved)+")") {
		t.Fatalf("the landed task's cleanup was not called, or answered something else: %s", log)
	}
}

// Storing an investigation task's report goes through internal/tasks, which
// registers its holds, and asks no cleanup of the task's copy, which is kept
// for a promotion on landed.go's terms for a reported investigation.
func TestStoringAReportKeepsTheInvestigationsCopy(t *testing.T) {
	t.Parallel()
	s, records, h := cleanupService(t)
	ctx := context.Background()
	copyPath, _ := h.TaskCopy("lookedaaaaaa")
	if _, err := records.CreateTask(ctx, store.Task{
		ID: "lookedaaaaaa", Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: copyPath,
	}, store.Disposition{}); err != nil {
		t.Fatalf("recording the task: %v", err)
	}
	stored, err := s.storeTaskReport(ctx, "lookedaaaaaa",
		`{"summary":"looked","findings":[{"action":"ask","description":"should this be kept?"}]}`)
	if err != nil {
		t.Fatalf("storeTaskReport: %v", err)
	}
	if len(stored.Holds) != 1 || !stored.Holds[0].Open() {
		t.Fatalf("the report registered %+v, want one open hold", stored.Holds)
	}
	if _, err := records.TaskReport(ctx, "lookedaaaaaa"); err != nil {
		t.Fatalf("the report was not stored: %v", err)
	}
	if log := serviceLog(t, h); strings.Contains(log, "isolated copy of task lookedaaaaaa") {
		t.Fatalf("storing the report asked the investigation's cleanup: %s", log)
	}
	if _, err := s.storeTaskReport(ctx, "lookedaaaaaa", `{"summary":"again"}`); err == nil {
		t.Fatal("a second report was stored")
	}
}

// PRD principle P12 at the task-copy site, with the terminal read through
// the product's own seam. The decision asked over a landed delivery task
// whose worker is still running keeps its copy with the live refusal,
// because the service's reader probed the launched session Live through the
// provider that holds it, and nothing here stated a liveness. The landing
// site, cleanUpLanded reached as settle reaches it with the merge record
// written for a run attributed to the task, then ends that worker through
// the session the service holds, reads the terminal Dead through the same
// reader, examines the copy on the proof and removes it, and the task
// resolves Complete from the cleanup record.
func TestATaskCopyIsKeptWhileItsWorkerRunsAndTheLandingEndsItAndRemovesTheCopy(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P12)
	ctx := t.Context()
	sub := newLaunchSubject(t, workerStandIn(t))
	s := sub.service
	s.terminals = heldTerminals{s}
	s.forge = forge.NewGitHubHost(redact.New(), forge.WithBinary(filepath.Join(t.TempDir(), "absent-gh")))
	var err error
	if s.cleaner, err = tasks.NewCleaner(tasks.CleanupDeps{Home: s.home, Store: s.store, Forge: s.forge}); err != nil {
		t.Fatalf("building the cleaner: %v", err)
	}
	d, err := s.dispatchTask(ctx, sub.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	launched, err := s.launchTask(ctx, d)
	if err != nil {
		t.Fatalf("launching: %v", err)
	}
	provider, _, _ := s.sessionOf(sub.task)
	waitCaptured(t, provider, launched, "worker ready")

	// A run attributed to the task, landed by its merge record.
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sub.records.CreateRun(ctx, store.Run{
		ID: "run-landed", RepositoryID: "subject", Branch: "task/one", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
	}); err != nil {
		t.Fatalf("recording the run: %v", err)
	}
	if _, err := sub.records.TransitionRun(ctx, "run-landed", []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatal(err)
	}
	landed, err := sub.records.TransitionRunOnPullRequest(ctx, "run-landed", []store.RunStatus{store.RunRunning}, store.RunPassed,
		store.PullRequestRead{PullRequest: 7, Head: "aaaa", End: store.PullRequestMerged}, store.HoldClosure{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.tasks().Task(ctx, sub.task); err != nil || got.State != taskstate.Landed || got.Basis.Process != taskstate.Live {
		t.Fatalf("the task resolves %+v (%v), want Landed with its terminal live", got, err)
	}

	// The worker runs: the decision, asked without ending it, keeps the
	// copy with the live refusal.
	s.cleanUpLandedTask(ctx, sub.task)
	if log := serviceLog(t, s.home); !strings.Contains(log, "isolated copy of task "+sub.task+" is left standing ("+string(tasks.ReasonTerminalLive)+")") {
		t.Fatalf("the cleanup over a live worker did not refuse as live: %s", log)
	}
	if _, err := os.Stat(sub.copy); err != nil {
		t.Fatalf("the copy of a task whose worker runs was touched: %v", err)
	}
	if _, err := sub.records.TaskCleanup(ctx, sub.task); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a cleanup record was written over a live worker: %v", err)
	}

	// The landing site ends the worker through the session the service
	// holds; the same seam then reads Dead, and the copy is examined and
	// removed on the proof.
	s.cleanUpLanded(ctx, landed)
	if obs := provider.Probe(ctx, launched.Reference().String()); obs.Liveness != taskstate.Dead {
		t.Fatalf("after the landing site the worker probes %+v, want dead", obs)
	}
	if _, _, held := s.sessionOf(sub.task); held {
		t.Fatal("the service still holds the landed task's session")
	}
	if log := serviceLog(t, s.home); !strings.Contains(log, "isolated copy of task "+sub.task+": "+string(tasks.CleanupRemoved)+" on ") {
		t.Fatalf("the cleanup over a dead terminal did not remove the copy: %s", log)
	}
	if _, err := os.Stat(sub.copy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the copy still stands after its removal was logged: %v", err)
	}
	if got, err := s.tasks().Task(ctx, sub.task); err != nil || got.State != taskstate.Complete {
		t.Fatalf("the task resolves %+v (%v), want Complete from the cleanup record", got, err)
	}
}
