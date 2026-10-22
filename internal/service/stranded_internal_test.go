package service

import (
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// A push that terminates the branch's run and then cannot create its
// replacement leaves the branch idle, and releases the task queued behind the
// one the displaced run was attributed to, after the exclusion is given back.
//
// The service is built by hand, on the terms the branch-gate test above
// builds one, because the failure has to land between claimPush's two
// writes: the driver it is handed does the terminate, and the create fails
// in driverFor, which resolves an agent out of an empty catalog. Nothing
// here plants a fault in the store.
func TestAPushThatCannotReplaceTheRunItDisplacedReleasesTheSuccessor(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root := t.TempDir()
	h, err := home.Open(root)
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
	repository, err := records.UpsertRepository(ctx, store.Repository{
		ID: "subject", WorkingPath: filepath.Join(root, "checkout"),
		UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	blocker, err := records.CreateTask(ctx, store.Task{
		ID: "blockeraaaaa", Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/w/blocker",
		RepositoryID: store.Known(repository.ID), Branch: store.Known("work"),
	}, store.Disposition{})
	if err != nil {
		t.Fatalf("recording the blocker: %v", err)
	}
	if _, err := records.BeginTaskAttempt(ctx, blocker.ID, 0); err != nil {
		t.Fatalf("beginning the blocker's attempt: %v", err)
	}
	successor, err := records.CreateTask(ctx, store.Task{
		ID: "successoraaa", Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/w/successor",
		RepositoryID: store.Known(repository.ID), Branch: store.Known("work"),
	}, store.Disposition{Blocker: store.Known(blocker.ID), Observed: blocker.Row})
	if err != nil {
		t.Fatalf("recording the successor: %v", err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}
	if _, err := records.CreateRun(ctx, store.Run{
		ID: "run-1", RepositoryID: repository.ID, Branch: "work", SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build,
	}); err != nil {
		t.Fatalf("recording the blocker's run: %v", err)
	}
	if _, err := records.TransitionRun(ctx, "run-1", []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("starting the blocker's run: %v", err)
	}

	log, err := h.OpenLog(0)
	if err != nil {
		t.Fatalf("opening the log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	events, err := ipc.NewPublisher(ipc.PublisherConfig{})
	if err != nil {
		t.Fatalf("opening the publisher: %v", err)
	}
	s := &Service{
		home: h, store: records, log: log, events: events,
		// No agent resolves out of this catalog, so the create behind the
		// claim fails in driverFor while the terminate before it has
		// already been made with the driver handed in below.
		catalog:   agents.NewCatalog(),
		starting:  make(map[branchKey]*branchGate),
		advancing: make(map[string]*slot),
		cfg:       config.Config{GitDeadline: config.DefaultGitDeadline},
	}
	if s.queue, err = s.newQueue(); err != nil {
		t.Fatalf("building the queue: %v", err)
	}
	resolution, err := agents.Resolve(ctx, []string{agents.AutoEntry},
		agents.NewCatalog(fixedRunner{runner: standin.New(t, standin.Script{}).Runner()}))
	if err != nil {
		t.Fatalf("resolving the stand-in: %v", err)
	}
	runService, err := runs.New(runs.Options{Store: records, Agent: resolution})
	if err != nil {
		t.Fatalf("building the run service: %v", err)
	}

	reader := taskstate.NewReader(records, nil, nil)
	if got, err := reader.Task(ctx, successor.ID); err != nil || got.State != taskstate.Queued {
		t.Fatalf("before the push the successor resolves %+v (%v), want queued", got, err)
	}
	_, _, err = s.claimPush(ctx, &driver{agent: resolution, runs: runService}, repository.ID, "work", "cccc", "", noPushedIntent)
	if err == nil {
		t.Fatal("the push created its replacement run, so the stranded path was not reached")
	}
	displaced, err := records.Run(ctx, "run-1")
	if err != nil || displaced.Status != store.RunTerminated {
		t.Fatalf("the displaced run is %s (%v), want terminated", displaced.Status, err)
	}
	if all, err := records.RunsForRepository(ctx, repository.ID); err != nil || len(all) != 1 {
		t.Fatalf("the branch holds %d runs (%v), want only the displaced one", len(all), err)
	}
	got, err := reader.Task(ctx, successor.ID)
	if err != nil || got.State != taskstate.Dispatched || got.Source != taskstate.SourceEvent {
		t.Fatalf("after the failed push the successor resolves %s from %s (%v), want dispatched from its release",
			got.State, got.Source, err)
	}
	if len(s.starting) != 0 {
		t.Fatalf("%d branch gate(s) are still held after the push", len(s.starting))
	}
}
