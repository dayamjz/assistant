package store

import (
	"context"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
)

// intentOf reads a task's recorded delivery intent back through the accessor.
func intentOf(t *testing.T, s *Store, id string) Optional[string] {
	t.Helper()
	task, err := s.Task(context.Background(), id)
	if err != nil {
		t.Fatalf("Task(%s): %v", id, err)
	}
	return task.DeliveryIntent
}

// A task's delivery intent is written once, by the promotion that makes it a
// delivery task, and the schema refuses every other write of it rather than
// leaving that to the absence of a second writer. CreateTask refuses a task
// carrying one, and so does an insert written by hand, because intake records
// no promotion. A promoted task's intent cannot be revised, rewritten with
// its own text, or cleared; an investigation cannot be given one without
// being promoted in the same statement; a delivery task intake recorded
// cannot be given one; and a promotion statement naming a blank one is
// refused. The controls are that promotion's own write lands, through the
// accessor and through the same statement shape written by hand, and that an
// update which does not name the column is not refused on a task that has
// one. P14 is what the test claims: the record is the one owner of what a
// promoted task's delivery is to do, so no later write may make it say
// something else.
func TestTheDeliveryIntentIsWrittenOnceByPromotion(t *testing.T) {
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)

	if _, err := s.CreateTask(ctx, Task{
		ID: "taskaaaaaaca", Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/tasks/ca/copy",
		RepositoryID: Known("repo-1"), Branch: Known("carried"), DeliveryIntent: Known("Deliver it."),
	}, Disposition{}); err == nil {
		t.Fatal("CreateTask recorded a delivery intent at intake")
	}
	if err := rawWrite(ctx, s, `INSERT INTO task (id, shape, project, mode, worktree_path, repository_id, branch, created_at,
		delivery_intent) VALUES ('taskaaaaaacb', 'delivery', 'one', 'interactive', '/tasks/cb/copy', 'repo-1', 'b', ?, 'x')`,
		encodeTime(nowUTC())); err == nil {
		t.Fatal("an insert naming a delivery intent was accepted")
	}
	for _, id := range []string{"taskaaaaaaca", "taskaaaaaacb"} {
		if _, err := s.Task(ctx, id); err == nil {
			t.Fatalf("the refused intake of %s recorded a task", id)
		}
	}

	intake, err := s.CreateTask(ctx, Task{
		ID: "taskaaaaaacc", Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/tasks/cc/copy",
		RepositoryID: Known("repo-1"), Branch: Known("intake"),
	}, Disposition{})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if got := intentOf(t, s, intake.ID); got.IsKnown() {
		t.Fatalf("a task intake recorded carries the delivery intent %v, want none", got)
	}
	promoted := seedInvestigation(t, s, "taskaaaaaacd")
	if got := intentOf(t, s, promoted.ID); got.IsKnown() {
		t.Fatalf("an investigation carries the delivery intent %v, want none", got)
	}
	if _, _, err := s.TransitionTaskShape(ctx, promoted.ID, TaskInvestigation, TaskDelivery, "repo-1", "promoted",
		config.DeliveryGated, "Deliver it.", 2, "promoted in the test"); err != nil {
		t.Fatalf("the promotion's own write of the intent: %v", err)
	}
	if got := intentOf(t, s, promoted.ID); got != Known("Deliver it.") {
		t.Fatalf("the promoted task carries %v, want the intent it was promoted with", got)
	}
	investigation := seedInvestigation(t, s, "taskaaaaaace")

	refused := []struct {
		name, statement, task string
	}{
		{"revising an intent promotion recorded", `UPDATE task SET delivery_intent = 'other' WHERE id = ?`, promoted.ID},
		{"rewriting an intent with its own text", `UPDATE task SET delivery_intent = 'Deliver it.' WHERE id = ?`, promoted.ID},
		{"clearing an intent", `UPDATE task SET delivery_intent = NULL WHERE id = ?`, promoted.ID},
		{"giving a task intake recorded an intent", `UPDATE task SET delivery_intent = 'x' WHERE id = ?`, intake.ID},
		{"giving an investigation an intent without promoting it", `UPDATE task SET delivery_intent = 'x' WHERE id = ?`, investigation.ID},
		{"promoting with a blank intent", `UPDATE task SET shape = 'delivery', branch = 'b', delivery = 'gated',
			delivery_intent = '  ' WHERE id = ?`, investigation.ID},
	}
	for _, write := range refused {
		before := intentOf(t, s, write.task)
		if err := rawWrite(ctx, s, write.statement, write.task); err == nil {
			t.Fatalf("%s was accepted", write.name)
		}
		if after := intentOf(t, s, write.task); after != before {
			t.Fatalf("%s moved the intent from %v to %v", write.name, before, after)
		}
	}
	if task, err := s.Task(ctx, investigation.ID); err != nil || task.Shape != TaskInvestigation {
		t.Fatalf("a refused promotion statement moved the investigation: %+v, %v", task, err)
	}

	if err := rawWrite(ctx, s, `UPDATE task SET project = 'two' WHERE id = ?`, promoted.ID); err != nil {
		t.Fatalf("an update that does not name the intent was refused: %v", err)
	}
	if err := rawWrite(ctx, s, `UPDATE task SET shape = 'delivery', branch = 'b', delivery = 'gated',
		delivery_intent = 'By hand.' WHERE id = ?`, investigation.ID); err != nil {
		t.Fatalf("the promotion's statement shape written by hand was refused: %v", err)
	}
	if got := intentOf(t, s, investigation.ID); got != Known("By hand.") {
		t.Fatalf("the hand-written promotion recorded %v, want its intent", got)
	}
}

// The delivery intent migration is additive and keeps what was recorded
// before it: the database is taken back to the shape without it, by
// forgetting only that version and dropping its triggers and its column, and
// the shipped migration is applied over the rows. A task promoted before the
// column existed reads back promoted, with no intent rather than a guessed
// one, and the triggers refuse again once it is reapplied. The migration is
// found by name, so the test spells no version the shipped list may grow
// past.
func TestThePromotedIntentMigrationKeepsEarlierTasks(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	var added migration
	for _, m := range schema {
		if m.name == promotedIntentMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", promotedIntentMigration)
	}
	earlier := seedInvestigation(t, s, "taskaaaaaacf")
	seedInvestigation(t, s, "taskaaaaaacg")

	for _, statement := range []string{
		`DROP TRIGGER task_delivery_intent_is_not_inserted`,
		`DROP TRIGGER task_delivery_intent_is_written_once`,
		`ALTER TABLE task DROP COLUMN delivery_intent`,
	} {
		if _, err := s.write.ExecContext(ctx, statement); err != nil {
			t.Fatalf("rolling the schema back with %q: %v", statement, err)
		}
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM schema_migration WHERE version = ?`, added.version); err != nil {
		t.Fatalf("forgetting the migration: %v", err)
	}
	// The promotion an earlier build made: the shape, the branch and the
	// posture, and no intent, because there was nowhere to put one.
	if err := rawWrite(ctx, s, `UPDATE task SET shape = 'delivery', branch = 'earlier', delivery = 'gated' WHERE id = ?`,
		earlier.ID); err != nil {
		t.Fatalf("an earlier build's promotion: %v", err)
	}
	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("applying the shipped migration over the old rows: %v", err)
	}
	read, err := s.Task(ctx, earlier.ID)
	if err != nil || read.Shape != TaskDelivery || read.Branch.Or("") != "earlier" || read.DeliveryIntent.IsKnown() {
		t.Fatalf("the earlier promoted task reads back %+v (%v), want delivery on earlier with no intent", read, err)
	}
	if got := intentOf(t, s, "taskaaaaaacg"); got.IsKnown() {
		t.Fatalf("the earlier investigation reads back with the intent %v, want none", got)
	}
	if err := rawWrite(ctx, s, `UPDATE task SET delivery_intent = 'late' WHERE id = ?`, earlier.ID); err == nil {
		t.Fatal("the reapplied migration let an earlier promoted task be given an intent later")
	}
	if _, _, err := s.TransitionTaskShape(ctx, "taskaaaaaacg", TaskInvestigation, TaskDelivery, "repo-1", "later",
		config.DeliveryGated, "Deliver it.", 2, "promoted after the migration"); err != nil {
		t.Fatalf("a promotion after the reapplied migration: %v", err)
	}
	if got := intentOf(t, s, "taskaaaaaacg"); got != Known("Deliver it.") {
		t.Fatalf("the task promoted after the migration carries %v, want its intent", got)
	}
}
