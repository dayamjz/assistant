package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
)

// rawWrite runs one statement on the writer connection in a transaction, the
// way an accessor of this package would, so the schema's own refusal is what
// is under test rather than an accessor's check before it.
func rawWrite(ctx context.Context, s *Store, statement string, args ...any) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, statement, args...)
		return err
	})
}

// postureOf reads a task's recorded posture back through the accessor.
func postureOf(t *testing.T, s *Store, id string) Optional[config.Delivery] {
	t.Helper()
	task, err := s.Task(context.Background(), id)
	if err != nil {
		t.Fatalf("Task(%s): %v", id, err)
	}
	return task.Delivery
}

// A task's delivery posture is written once, at intake or by the promotion
// that makes it a delivery task, and the schema refuses every other write of
// it rather than leaving that to the absence of a second writer: a posture
// intake recorded, a posture promotion recorded, a delivery task recorded
// with none, and an investigation task are each refused a write of the
// column, and so is clearing one. The controls are that promotion's own write
// lands on an investigation task with no posture, through the accessor and
// through the same statement shape written by hand, and that an update which
// does not name the column is not refused on a task that has one. P14 is what
// the test claims: the record is the one owner of the posture a task is
// followed under, so no later write may make it say something else.
func TestTheDeliveryPostureIsWrittenOnceAtIntakeOrPromotion(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)

	intake, err := s.CreateTask(ctx, Task{
		ID: "taskaaaaaaba", Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/tasks/ba/copy",
		RepositoryID: Known("repo-1"), Branch: Known("intake"), Delivery: Known(config.DeliveryLocal),
	}, Disposition{})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	promoted := seedInvestigation(t, s, "taskaaaaaabb")
	if _, _, err := s.TransitionTaskShape(ctx, promoted.ID, TaskInvestigation, TaskDelivery, "repo-1", "promoted",
		config.DeliveryLocal, "Deliver it.", 2, "promoted in the test", ResolvedByMachineInterface()); err != nil {
		t.Fatalf("the promotion's own write of the posture: %v", err)
	}
	if got := postureOf(t, s, promoted.ID); got != Known(config.DeliveryLocal) {
		t.Fatalf("the promoted task carries %v, want local", got)
	}
	if err := rawWrite(ctx, s, `INSERT INTO task (id, shape, project, mode, worktree_path, repository_id, branch, created_at)
		VALUES ('taskaaaaaabc', 'delivery', 'one', 'interactive', '/tasks/bc/copy', 'repo-1', 'unrecorded', ?)`,
		encodeTime(nowUTC())); err != nil {
		t.Fatalf("writing a delivery task with no posture, as one recorded before the column: %v", err)
	}
	investigation := seedInvestigation(t, s, "taskaaaaaabd")

	refused := []struct {
		name, statement, task string
	}{
		{"revising a posture intake recorded", `UPDATE task SET delivery = 'gated' WHERE id = ?`, intake.ID},
		{"revising a posture promotion recorded", `UPDATE task SET delivery = 'gated' WHERE id = ?`, promoted.ID},
		{"rewriting a posture with its own value", `UPDATE task SET delivery = 'local' WHERE id = ?`, promoted.ID},
		{"clearing a posture", `UPDATE task SET delivery = NULL WHERE id = ?`, intake.ID},
		{"giving a delivery task recorded without one a posture", `UPDATE task SET delivery = 'gated' WHERE id = ?`, "taskaaaaaabc"},
		{"giving an investigation a posture without promoting it", `UPDATE task SET delivery = 'gated' WHERE id = ?`, investigation.ID},
		{"promoting with no posture named", `UPDATE task SET shape = 'delivery', branch = 'b', delivery = NULL WHERE id = ?`, investigation.ID},
	}
	for _, write := range refused {
		before := postureOf(t, s, write.task)
		if err := rawWrite(ctx, s, write.statement, write.task); err == nil {
			t.Fatalf("%s was accepted", write.name)
		}
		if after := postureOf(t, s, write.task); after != before {
			t.Fatalf("%s moved the posture from %v to %v", write.name, before, after)
		}
	}
	if task, err := s.Task(ctx, investigation.ID); err != nil || task.Shape != TaskInvestigation {
		t.Fatalf("a refused promotion statement moved the investigation: %+v, %v", task, err)
	}

	if err := rawWrite(ctx, s, `UPDATE task SET project = 'two' WHERE id = ?`, intake.ID); err != nil {
		t.Fatalf("an update that does not name the posture was refused: %v", err)
	}
	if err := rawWrite(ctx, s, `UPDATE task SET shape = 'delivery', branch = 'b', delivery = 'gated' WHERE id = ?`,
		investigation.ID); err != nil {
		t.Fatalf("the promotion's statement shape written by hand was refused: %v", err)
	}
	if got := postureOf(t, s, investigation.ID); got != Known(config.DeliveryGated) {
		t.Fatalf("the hand-written promotion recorded %v, want gated", got)
	}
}

// The written-once migration is additive and keeps what was recorded before
// it: the database is taken back to the shape without it, by forgetting only
// that version and dropping its trigger, a posture is revised there to show
// the trigger is what refused it, and the shipped migration is applied over
// the rows. Every task reads back as it stood and the trigger refuses again.
// The migration is found by name, so the test spells no version the shipped
// list may grow past.
func TestThePromotedPostureMigrationKeepsEarlierTasks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	var added migration
	for _, m := range schema {
		if m.name == promotedPostureMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", promotedPostureMigration)
	}
	if _, err := s.CreateTask(ctx, Task{
		ID: "taskaaaaaabe", Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/tasks/be/copy",
		RepositoryID: Known("repo-1"), Branch: Known("earlier"), Delivery: Known(config.DeliveryLocal),
	}, Disposition{}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	seedInvestigation(t, s, "taskaaaaaabf")

	if _, err := s.write.ExecContext(ctx, `DROP TRIGGER task_delivery_is_written_once`); err != nil {
		t.Fatalf("rolling the schema back: %v", err)
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM schema_migration WHERE version = ?`, added.version); err != nil {
		t.Fatalf("forgetting the migration: %v", err)
	}
	// Without the trigger nothing in the schema refuses a revision, which
	// is the control that the trigger is what refuses one.
	if err := rawWrite(ctx, s, `UPDATE task SET delivery = 'gated' WHERE id = 'taskaaaaaabe'`); err != nil {
		t.Fatalf("a revision without the trigger: %v", err)
	}
	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("applying the shipped migration over the old rows: %v", err)
	}
	if got := postureOf(t, s, "taskaaaaaabe"); got != Known(config.DeliveryGated) {
		t.Fatalf("the earlier delivery task reads back under %v, want the gated it stood under", got)
	}
	if got := postureOf(t, s, "taskaaaaaabf"); got.IsKnown() {
		t.Fatalf("the earlier investigation reads back under %v, want no posture", got)
	}
	if err := rawWrite(ctx, s, `UPDATE task SET delivery = 'local' WHERE id = 'taskaaaaaabe'`); err == nil {
		t.Fatal("the reapplied migration did not refuse a revision")
	}
}
