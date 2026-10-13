package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
)

func insertTaskStateRow(ctx context.Context, db *sql.DB, taskID string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO task_state (task_id, state, source, detail, revision, resolved_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		taskID, "running", "supervisor", Unknown[string](), int64(1), encodeTime(nowUTC()))
	return err
}

func sealTaskStateVersion(t *testing.T) int {
	t.Helper()
	for _, m := range schema {
		if m.name == sealTaskStateTableMigration {
			return m.version
		}
	}
	t.Fatalf("the shipped schema has no migration named %q", sealTaskStateTableMigration)
	return 0
}

// P14: a task's state has no stored record. The table that used to be one takes
// no row, and the refusal is the seal's: with the trigger dropped the same row
// lands, so the first half is not a malformed statement refused for some other
// reason.
func TestTheTaskStateTableTakesNoRows(t *testing.T) {
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "task-1")

	err := insertTaskStateRow(ctx, s.write, task.ID)
	if err == nil {
		t.Fatal("task_state accepted a row, so a task can be given a stored second answer")
	}
	if !strings.Contains(err.Error(), "the task_state table is not a record") {
		t.Fatalf("the row was refused for some other reason than the seal: %v", err)
	}

	if _, err := s.write.ExecContext(ctx, `DROP TRIGGER task_state_is_not_a_record`); err != nil {
		t.Fatalf("dropping the seal: %v", err)
	}
	if err := insertTaskStateRow(ctx, s.write, task.ID); err != nil {
		t.Fatalf("with the seal gone the row still would not land, so the refusal did not demonstrate it: %v", err)
	}
}

// A database migrated forward from a build that wrote task_state arrives at the
// seal holding rows. The shipped migration is replayed against that state.
func TestTheTaskStateSealEmptiesARowAnEarlierBuildWrote(t *testing.T) {
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "task-1")

	if _, err := s.write.ExecContext(ctx, `DROP TRIGGER task_state_is_not_a_record`); err != nil {
		t.Fatalf("dropping the seal: %v", err)
	}
	if err := insertTaskStateRow(ctx, s.write, task.ID); err != nil {
		t.Fatalf("writing the row an earlier build carried: %v", err)
	}
	if _, err := s.write.ExecContext(ctx,
		`DELETE FROM schema_migration WHERE version = ?`, sealTaskStateVersion(t)); err != nil {
		t.Fatalf("forgetting the seal migration: %v", err)
	}
	// The columns and index are already there from the first application, so
	// migrate would refuse to add them twice; the statements of the shipped
	// migration that empty and seal the table are replayed instead.
	var seal migration
	for _, m := range schema {
		if m.name == sealTaskStateTableMigration {
			seal = m
		}
	}
	for _, stmt := range seal.statements {
		if strings.HasPrefix(stmt, "ALTER") || strings.HasPrefix(stmt, "CREATE INDEX") {
			continue
		}
		if _, err := s.write.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("replaying %q: %v", stmt, err)
		}
	}

	var rows int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM task_state`).Scan(&rows); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("the seal left %d row(s) in task_state", rows)
	}
	if err := insertTaskStateRow(ctx, s.write, task.ID); err == nil {
		t.Fatal("the replayed seal emptied the table but did not seal it")
	}
}

func seedBranchTask(t *testing.T, s *Store, id string) Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), Task{
		ID: id, Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/" + id,
		RepositoryID: Known("repo-1"), Branch: Known("topic"),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func extraRun(t *testing.T, s *Store, id, repo, branch string) Run {
	t.Helper()
	ctx := context.Background()
	if _, err := s.UpsertRepository(ctx, Repository{
		ID: repo, WorkingPath: "/checkouts/" + repo, UpstreamURL: "https://example.test/" + repo + ".git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	run, err := s.CreateRun(ctx, Run{
		ID: id, RepositoryID: repo, Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", Build: testBuild(), ConfigDigest: "cfg-1",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run
}

// Attribution: a run belongs to a task by its repository and branch and by
// being no older than the task. A run of another branch, of another
// repository, or from before the task is not the task's. The branch-less task
// is the positive control for the NULL comparison: it matches no run by branch.
func TestASnapshotAttributesRunsByBranchAndAge(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	before := seedRun(t, s) // repo-1, topic, run-1: older than the task below
	time.Sleep(2 * time.Millisecond)
	task := seedBranchTask(t, s, "task-1")
	time.Sleep(2 * time.Millisecond)
	mine := extraRun(t, s, "run-mine", "repo-1", "topic")
	extraRun(t, s, "run-other-branch", "repo-1", "elsewhere")
	extraRun(t, s, "run-other-repo", "repo-2", "topic")
	investigation := seedTask(t, s, "task-2")

	snap, err := s.TaskSnapshot(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskSnapshot: %v", err)
	}
	if len(snap.Runs) != 1 || snap.Runs[0].ID != mine.ID {
		t.Fatalf("the task was attributed %v, want only %s (and not the older %s)", runIDs(snap.Runs), mine.ID, before.ID)
	}
	if snap.Task.ID != task.ID || len(snap.Events) != 1 {
		t.Fatalf("the snapshot's task and history are wrong: %+v", snap)
	}

	none, err := s.TaskSnapshot(ctx, investigation.ID)
	if err != nil || len(none.Runs) != 0 {
		t.Fatalf("a task with no branch was attributed %v (%v)", runIDs(none.Runs), err)
	}

	// A run the task names directly is its own whatever branch it ran.
	if err := s.SetTaskRun(ctx, investigation.ID, "run-other-branch"); err != nil {
		t.Fatalf("SetTaskRun: %v", err)
	}
	named, err := s.TaskSnapshot(ctx, investigation.ID)
	if err != nil || len(named.Runs) != 1 || named.Runs[0].ID != "run-other-branch" {
		t.Fatalf("the named run was not attributed: %v (%v)", runIDs(named.Runs), err)
	}
}

func runIDs(runs []Run) []string {
	var out []string
	for _, r := range runs {
		out = append(out, r.ID)
	}
	return out
}

// Holds come with the snapshot when they name the task or one of its runs, open
// or resolved, and a hold naming something else does not.
func TestASnapshotCarriesTheHoldsThatNameTheTaskOrItsRuns(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedBranchTask(t, s, "task-1")
	time.Sleep(2 * time.Millisecond)
	mine := extraRun(t, s, "run-mine", "repo-1", "topic")
	other := extraRun(t, s, "run-other", "repo-1", "elsewhere")

	for _, h := range []Hold{
		{Key: "on-task", Subject: "task", TaskID: Known(task.ID)},
		{Key: "on-run", Subject: "run", RunID: Known(mine.ID)},
		{Key: "elsewhere", Subject: "other", RunID: Known(other.ID)},
	} {
		if _, err := s.RegisterHold(ctx, h); err != nil {
			t.Fatalf("RegisterHold %s: %v", h.Key, err)
		}
	}
	if _, err := s.ResolveHold(ctx, "on-run", "yes", ResolvedByProgram()); err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}

	snap, err := s.TaskSnapshot(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskSnapshot: %v", err)
	}
	keys := map[string]bool{}
	for _, h := range snap.Holds {
		keys[h.Key] = h.Open()
	}
	if len(keys) != 2 || !keys["on-task"] || keys["on-run"] {
		t.Fatalf("the snapshot's holds are %v, want on-task (open) and on-run (resolved) only", keys)
	}
}

func TestTaskSnapshotsListNewestFirstAndRefuseUnknownTasks(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.TaskSnapshot(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TaskSnapshot of a missing task = %v, want ErrNotFound", err)
	}
	if all, err := s.TaskSnapshots(ctx); err != nil || len(all) != 0 {
		t.Fatalf("a fresh store lists (%d, %v)", len(all), err)
	}
	seedTask(t, s, "task-1")
	time.Sleep(2 * time.Millisecond)
	seedTask(t, s, "task-2")
	all, err := s.TaskSnapshots(ctx)
	if err != nil || len(all) != 2 || all[0].Task.ID != "task-2" || all[1].Task.ID != "task-1" {
		t.Fatalf("TaskSnapshots = %d snapshots, %v", len(all), err)
	}
}
