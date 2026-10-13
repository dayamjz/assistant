package store

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"slices"
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

// seedBranchTask creates an independent task on repo-1's topic branch,
// anchored to the newest task already on it, and begins its first attempt,
// since a task with no attempt is attributed no run by its branch.
func seedBranchTask(t *testing.T, s *Store, id string) Task {
	t.Helper()
	task := createBranchTask(t, s, id, Disposition{Observed: newestOnBranch(t, s, "repo-1", "topic")})
	beginAttempt(t, s, id, 0)
	return task
}

// createBranchTask creates a delivery task on repo-1's topic branch with d.
func createBranchTask(t *testing.T, s *Store, id string, d Disposition) Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), Task{
		ID: id, Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/" + id,
		RepositoryID: Known("repo-1"), Branch: Known("topic"),
	}, d)
	if err != nil {
		t.Fatalf("CreateTask %s: %v", id, err)
	}
	return task
}

// newestOnBranch is the highest Row among the tasks of one repository and
// branch, read the way an intake reads it: off the task listing.
func newestOnBranch(t *testing.T, s *Store, repo, branch string) int64 {
	t.Helper()
	tasks, err := s.Tasks(context.Background())
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	var newest int64
	for _, task := range tasks {
		if task.RepositoryID.Or("") == repo && task.Branch.Or("") == branch && task.Row > newest {
			newest = task.Row
		}
	}
	return newest
}

// beginAttempt records the next attempt of a task, anchored to expected.
func beginAttempt(t *testing.T, s *Store, id string, expected int64) TaskAttempt {
	t.Helper()
	a, err := s.BeginTaskAttempt(context.Background(), id, expected)
	if err != nil {
		t.Fatalf("BeginTaskAttempt %s: %v", id, err)
	}
	return a
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
// being no older than the task's latest attempt. A run of another branch, of
// another repository, or from before the attempt is not the task's. The
// branch-less task is the positive control for the NULL comparison: it matches
// no run by branch, with or without an attempt.
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
	if snap.Task.ID != task.ID || len(snap.Events) != 2 || len(snap.Attempts) != 1 {
		t.Fatalf("the snapshot's task, history and attempts are wrong: %+v", snap)
	}

	none, err := s.TaskSnapshot(ctx, investigation.ID)
	if err != nil || len(none.Runs) != 0 {
		t.Fatalf("a task with no branch was attributed %v (%v)", runIDs(none.Runs), err)
	}
	beginAttempt(t, s, investigation.ID, 0)
	none, err = s.TaskSnapshot(ctx, investigation.ID)
	if err != nil || len(none.Runs) != 0 {
		t.Fatalf("a task with no branch and an attempt was attributed %v (%v)", runIDs(none.Runs), err)
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

// A task attributed to a repository by its branch resolves from that
// repository's runs, so forgetting the repository is refused while one names it
// even though the task names no run. The control is that the same removal
// succeeds once nothing names the repository.
func TestForgettingARepositoryIsRefusedWhileATaskWorksOnOne(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunPending}, RunTerminated); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}
	seedBranchTask(t, s, "task-1")

	if _, err := s.ForgetRepository(ctx, run.RepositoryID); !errors.Is(err, ErrRepositoryInUse) {
		t.Fatalf("ForgetRepository = %v, want ErrRepositoryInUse", err)
	}
	if _, err := s.Run(ctx, run.ID); err != nil {
		t.Fatalf("the refused removal took the run anyway: %v", err)
	}

	control := openStore(t)
	cr := seedRun(t, control)
	if _, err := control.TransitionRun(ctx, cr.ID, []RunStatus{RunPending}, RunTerminated); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}
	if _, err := control.ForgetRepository(ctx, cr.RepositoryID); err != nil {
		t.Fatalf("with no task naming the repository the removal was refused: %v", err)
	}
}

// The attribution cutoff is the attempt boundary and not the task's creation.
// A task queued behind a live task on the same branch has no attempt, so the
// blocker's running run is not attributed to it; once its own attempt begins,
// a run newer than that is its and a run older than its latest attempt is
// not. The control is the blocker, whose attribution does not move.
func TestAttributionIsCutOffAtTheLatestAttempt(t *testing.T) {
	principles.Cite(t, principles.P8)
	ctx := context.Background()
	s := openStore(t)
	seedRun(t, s) // creates repo-1 and an older run on topic
	blocker := seedBranchTask(t, s, "blocker")
	time.Sleep(2 * time.Millisecond)
	live := extraRun(t, s, "run-live", "repo-1", "topic")
	if _, err := s.TransitionRun(ctx, live.ID, []RunStatus{RunPending}, RunRunning); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}
	queued := createBranchTask(t, s, "queued", Disposition{Blocker: Known(blocker.ID), Observed: blocker.Row})
	if got := queued.BlockerTask.Or(""); got != blocker.ID {
		t.Fatalf("the queued task records blocker %q, want %s", got, blocker.ID)
	}

	snap, err := s.TaskSnapshot(ctx, queued.ID)
	if err != nil {
		t.Fatalf("TaskSnapshot: %v", err)
	}
	if len(snap.Runs) != 0 || len(snap.Attempts) != 0 {
		t.Fatalf("a task with no attempt was attributed %v by its branch", runIDs(snap.Runs))
	}
	if events := snap.Events; len(events) != 1 || events[0].Kind != OpeningEventKind {
		t.Fatalf("a queued task's history is %+v, want the opening event alone", events)
	}

	time.Sleep(2 * time.Millisecond)
	first := beginAttempt(t, s, queued.ID, 0)
	time.Sleep(2 * time.Millisecond)
	mine := extraRun(t, s, "run-mine", "repo-1", "topic")
	snap, err = s.TaskSnapshot(ctx, queued.ID)
	if err != nil || len(snap.Runs) != 1 || snap.Runs[0].ID != mine.ID {
		t.Fatalf("after its attempt began the task was attributed %v (%v), want only %s", runIDs(snap.Runs), err, mine.ID)
	}

	// A relaunch moves the cutoff: the run from the first attempt is no
	// longer the task's, and one made after the relaunch is.
	time.Sleep(2 * time.Millisecond)
	beginAttempt(t, s, queued.ID, first.Sequence)
	snap, err = s.TaskSnapshot(ctx, queued.ID)
	if err != nil || len(snap.Runs) != 0 {
		t.Fatalf("after a relaunch the task is still attributed %v (%v)", runIDs(snap.Runs), err)
	}
	time.Sleep(2 * time.Millisecond)
	again := extraRun(t, s, "run-again", "repo-1", "topic")
	snap, err = s.TaskSnapshot(ctx, queued.ID)
	if err != nil || len(snap.Runs) != 1 || snap.Runs[0].ID != again.ID || len(snap.Attempts) != 2 {
		t.Fatalf("after a relaunch the task was attributed %v (%v), want only %s", runIDs(snap.Runs), err, again.ID)
	}

	// The control: the blocker's own attribution is unmoved by any of it,
	// since its attempt is where it was.
	control, err := s.TaskSnapshot(ctx, blocker.ID)
	if err != nil {
		t.Fatalf("TaskSnapshot: %v", err)
	}
	if want := []string{again.ID, mine.ID, live.ID}; !slices.Equal(runIDs(control.Runs), want) {
		t.Fatalf("the blocker is attributed %v, want %v", runIDs(control.Runs), want)
	}
}

// The snapshot carries the cleanup record once one is written and reports it
// unknown before, and the resolver reads nothing from it in this build.
func TestASnapshotCarriesTheCleanupRecord(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "task-1")
	before, err := s.TaskSnapshot(ctx, task.ID)
	if err != nil || before.Cleanup.IsKnown() {
		t.Fatalf("a task cleanup has not reached carries a cleanup record: %+v (%v)", before.Cleanup, err)
	}
	written, err := s.RecordTaskCleanup(ctx, TaskCleanup{
		TaskID: task.ID, Proof: CleanupProofRemoteBranch, Examined: []string{"abc", "def"}, RemovedAt: Known(time.Now()),
	})
	if err != nil {
		t.Fatalf("RecordTaskCleanup: %v", err)
	}
	after, err := s.TaskSnapshot(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskSnapshot: %v", err)
	}
	got, ok := after.Cleanup.Get()
	if !ok || !reflect.DeepEqual(got, written) {
		t.Fatalf("the snapshot carries cleanup %+v (known=%v), want %+v", got, ok, written)
	}
}
