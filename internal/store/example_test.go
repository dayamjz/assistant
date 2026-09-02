package store_test

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"regexp"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// A caller records a run, the verdict of one of its stages, and the history of
// how that verdict was reached. The fields a run does not have yet come back as
// unknown rather than as a value nobody chose.
func Example() {
	ctx := context.Background()

	// A real caller passes the redact module PRD section 8 names. This one
	// covers the same shape internal/vcs does.
	userinfo := regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/@\s]+)@`)
	redactor := vcs.RedactorFunc(func(s string) string {
		return userinfo.ReplaceAllString(s, "${1}REDACTED@")
	})

	home, err := homeDir()
	if err != nil {
		log.Fatal(err)
	}
	s, err := store.Open(ctx, filepath.Join(home, "state.db"), store.WithRedactor(redactor))
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	repo, err := s.UpsertRepository(ctx, store.Repository{
		ID:            "repo-1",
		WorkingPath:   filepath.Join(home, "checkouts", "one"),
		UpstreamURL:   "https://alice:s3cr3t@example.test/one.git",
		DefaultBranch: "main",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("upstream:", repo.UpstreamURL)

	run, err := s.CreateRun(ctx, store.Run{
		ID:            "run-1",
		RepositoryID:  repo.ID,
		Branch:        "topic",
		SubmittedHead: "aaaa1111",
		Base:          "bbbb2222",
		Intent:        "validate the pushed branch",
		IntentSource:  "push",
		Build:         store.Build{Version: "v0.1.0", Revision: "cccc3333", Go: "go1.25.0"},
		ConfigDigest:  "sha256:dddd4444",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("status:", run.Status)
	fmt.Println("approved commit:", run.ApprovedCommit)
	fmt.Println("built from:", run.Build)

	if _, err := s.UpsertStageResult(ctx, store.StageResult{
		RunID: run.ID, Stage: "review", Status: store.StageFailed,
		FindingCount: 2, LogPath: filepath.Join(home, "logs", run.ID, "review.log"),
	}); err != nil {
		log.Fatal(err)
	}
	if _, err := s.AppendRound(ctx, store.Round{
		RunID: run.ID, Stage: "review", Number: 1,
		Findings: []byte(`[{"id":"f1"},{"id":"f2"}]`),
		Selected: []byte(`["f1"]`), SelectedBy: "pipeline",
		FixerPayload: []byte("fix f1"), Summary: "one finding sent to the fixer",
	}); err != nil {
		log.Fatal(err)
	}

	stage, err := s.StageResult(ctx, run.ID, "review")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("review:", stage.Status, stage.FindingCount, "findings, took", stage.Duration)

	rounds, err := s.Rounds(ctx, run.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("rounds recorded:", len(rounds), "-", rounds[0].Summary)

	// Output:
	// upstream: https://REDACTED@example.test/one.git
	// status: pending
	// approved commit: unknown
	// built from: v0.1.0 cccc3333 go1.25.0
	// review: failed 2 findings, took unknown
	// rounds recorded: 1 - one finding sent to the fixer
}

// A task's current state and its history are different records, and only one of
// them answers what is true now.
func ExampleStore_TaskState() {
	ctx := context.Background()
	s := openForExample()
	defer func() { _ = s.Close() }()

	task, err := s.CreateTask(ctx, store.Task{
		ID: "task-1", Shape: store.TaskInvestigation, Project: "one",
		Mode: "headless", WorktreePath: "/worktrees/task-1",
	}, "queued", "intake")
	if err != nil {
		log.Fatal(err)
	}

	// The worker appends history. None of it changes what is true now.
	for _, kind := range []string{"started", "blocked"} {
		if _, err := s.AppendTaskEvent(ctx, task.ID, kind, "worker said so"); err != nil {
			log.Fatal(err)
		}
	}
	events, err := s.TaskEvents(ctx, task.ID, 0, 0)
	if err != nil {
		log.Fatal(err)
	}
	current, err := s.TaskState(ctx, task.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("newest event:", events[len(events)-1].Kind)
	fmt.Println("actual state:", current.State, "resolved by", current.Source)

	// The owner of the state resolves it, and the revision moves.
	resolved, err := s.SetTaskState(ctx, task.ID, "running", "supervisor", store.Known("stage review is live"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("actual state:", resolved.State, "at revision", resolved.Revision)

	// Output:
	// newest event: blocked
	// actual state: queued resolved by intake
	// actual state: running at revision 2
}
