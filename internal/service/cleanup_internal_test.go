package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// cleanupService is a service built by hand with what the two task-copy
// cleanup sites read: the store, the home, the log and the cleaner. No
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
// calls the task copy's cleanup, which refuses today with the terminal
// unobserved, as the site passes it; a run no merge record landed calls
// nothing, and a task the run is not attributed to is not touched.
func TestAMergeRecordCallsTheLandedTasksCleanup(t *testing.T) {
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
// registers its holds, and then calls the copy's cleanup, which refuses
// today with the terminal unobserved.
func TestStoringAReportCallsTheInvestigationsCleanup(t *testing.T) {
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
	if log := serviceLog(t, h); !strings.Contains(log, "isolated copy of task lookedaaaaaa is left standing ("+string(tasks.ReasonTerminalUnobserved)+")") {
		t.Fatalf("the investigation's cleanup was not called, or answered something else: %s", log)
	}
	if _, err := s.storeTaskReport(ctx, "lookedaaaaaa", `{"summary":"again"}`); err == nil {
		t.Fatal("a second report was stored")
	}
}
