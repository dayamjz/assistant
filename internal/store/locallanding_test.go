package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
)

// seedLocalTask records a delivery task under the local posture.
func seedLocalTask(t *testing.T, s *Store, id string) Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), Task{
		ID: id, Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/" + id,
		Delivery: Known(config.DeliveryLocal),
	}, Disposition{})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// The posture is a field of its own beside the launch notation: written at
// creation, read back as written, and unknown for a task that stated none,
// which is what a task recorded before the column existed reads as too. A
// posture outside the set and a posture on an investigation task are
// refused before anything is written.
func TestTheDeliveryPostureIsRecordedBesideTheLaunchNotation(t *testing.T) {
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)
	local := seedLocalTask(t, s, "taskaaaaaaac")
	if got, ok := local.Delivery.Get(); !ok || got != config.DeliveryLocal || local.Mode != "interactive" {
		t.Fatalf("the task came back with posture %v and launch notation %q", local.Delivery, local.Mode)
	}
	plain := seedTask(t, s, "taskaaaaaaad")
	if plain.Delivery.IsKnown() {
		t.Fatalf("a task that stated no posture reads %v, want unknown", plain.Delivery)
	}
	snap, err := s.TaskSnapshot(ctx, local.ID)
	if err != nil || snap.Task.Delivery != local.Delivery {
		t.Fatalf("the snapshot read the posture as %v, %v", snap.Task.Delivery, err)
	}

	for _, refused := range []Task{
		{ID: "taskaaaaaaae", Shape: TaskDelivery, Project: "one", WorktreePath: "/w/e", Delivery: Known(config.Delivery("direct"))},
		{ID: "taskaaaaaaaf", Shape: TaskInvestigation, Project: "one", WorktreePath: "/w/f", Delivery: Known(config.DeliveryGated)},
	} {
		if _, err := s.CreateTask(ctx, refused, Disposition{}); err == nil {
			t.Fatalf("a task with posture %v and shape %s was recorded", refused.Delivery, refused.Shape)
		}
		if _, err := s.Task(ctx, refused.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a refused task left a row: %v", err)
		}
	}
	if _, err := s.CreateTask(ctx, Task{ID: "taskaaaaaaae", Shape: TaskDelivery, Project: "one", WorktreePath: "/w/e",
		Delivery: Known(config.Delivery("direct"))}, Disposition{}); !errors.Is(err, ErrUnknownDelivery) {
		t.Fatalf("a posture outside the set = %v, want ErrUnknownDelivery", err)
	}

	// A word outside the set in the column was not written by this package,
	// and reading it back is refused rather than taken as some posture.
	if err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE task SET delivery = 'direct' WHERE id = ?`, plain.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Task(ctx, plain.ID); !errors.Is(err, ErrUnknownDelivery) {
		t.Fatalf("a stored posture outside the set read back as %v, want ErrUnknownDelivery", err)
	}
}

// The local landing record is written once, with the approval hold closed in
// the same transaction, and read back as written by the accessor and by the
// snapshot; a second record, a record on a task not recorded under the local
// posture, and an incomplete record are refused and leave nothing; and the
// table refuses to update or delete a row. The first write is the control.
func TestALocalLandingRecordIsWrittenOnceWithTheApproval(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := openStore(t)
	task := seedLocalTask(t, s, "taskaaaaaaag")
	gated := seedTask(t, s, "taskaaaaaaah")
	hold, err := s.RegisterHold(ctx, Hold{Key: "task/" + task.ID + "/local-approval/x", TaskID: Known(task.ID), Subject: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	landing := LocalLanding{Task: task.ID, Commit: "c2", Branch: "refs/heads/main", Before: "c1", After: "c2"}
	closing := HoldClosure{Hold: hold, Resolution: "approve", By: ResolvedByMachineInterface()}

	for name, refused := range map[string]LocalLanding{
		"no commit":           {Task: task.ID, Branch: "refs/heads/main", Before: "c1", After: "c2"},
		"after is not commit": {Task: task.ID, Commit: "c3", Branch: "refs/heads/main", Before: "c1", After: "c2"},
		"no before":           {Task: task.ID, Commit: "c2", Branch: "refs/heads/main", After: "c2"},
	} {
		if _, err := s.RecordLocalLanding(ctx, refused, closing); err == nil {
			t.Fatalf("a record with %s was written", name)
		}
	}
	other := landing
	other.Task = gated.ID
	if _, err := s.RecordLocalLanding(ctx, other, HoldClosure{}); !errors.Is(err, ErrNotLocal) {
		t.Fatalf("a record on a task with no local posture = %v, want ErrNotLocal", err)
	}
	if _, found, err := s.LocalLanding(ctx, task.ID); found || err != nil {
		t.Fatalf("a refused write left a record: %v, %v", found, err)
	}
	if h, _ := s.Hold(ctx, hold.Key); !h.Open() {
		t.Fatal("a refused write closed the approval hold")
	}

	written, err := s.RecordLocalLanding(ctx, landing, closing)
	if err != nil {
		t.Fatalf("RecordLocalLanding: %v", err)
	}
	if written.Commit != "c2" || written.Before != "c1" || written.After != "c2" || written.RecordedAt.IsZero() {
		t.Fatalf("the record came back as %+v", written)
	}
	if h, _ := s.Hold(ctx, hold.Key); h.Open() || h.Resolution.Or("") != "approve" {
		t.Fatalf("the approval hold is %+v after the record was written", h)
	}
	snap, err := s.TaskSnapshot(ctx, task.ID)
	if got, ok := snap.LocalLanding.Get(); err != nil || !ok || got != written {
		t.Fatalf("the snapshot carries %v, %v; want %+v", snap.LocalLanding, err, written)
	}
	if _, err := s.RecordLocalLanding(ctx, landing, HoldClosure{}); !errors.Is(err, ErrLocalLandingRecorded) {
		t.Fatalf("a second record = %v, want ErrLocalLandingRecorded", err)
	}
	for _, verb := range []string{
		`UPDATE local_landing SET commit_id = 'c9' WHERE task = ?`,
		`DELETE FROM local_landing WHERE task = ?`,
	} {
		if err := s.inTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, verb, task.ID)
			return err
		}); err == nil {
			t.Fatalf("%q was accepted on a local landing record", verb)
		}
	}
	if read, found, err := s.LocalLanding(ctx, task.ID); err != nil || !found || read != written {
		t.Fatalf("the record after the refused writes is %+v, %v, %v", read, found, err)
	}
}

// The posture's migration is additive: the database is taken back to the
// shape without it, a task is written there, and the shipped migration is
// applied over it. The task reads back unchanged with its posture unknown,
// never a fabricated gated, and with no landing record. The migration is
// found by name, so the test spells no version the shipped list may grow
// past.
func TestTheLocalLandingMigrationKeepsEarlierTasks(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	var added migration
	for _, m := range schema {
		if m.name == localLandingMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", localLandingMigration)
	}
	for _, statement := range []string{
		`DROP TABLE local_landing`,
		`ALTER TABLE task DROP COLUMN delivery`,
	} {
		if _, err := s.write.ExecContext(ctx, statement); err != nil {
			t.Fatalf("rolling the schema back with %q: %v", statement, err)
		}
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM schema_migration WHERE version = ?`, added.version); err != nil {
		t.Fatalf("forgetting the migration: %v", err)
	}
	if _, err := s.write.ExecContext(ctx, `INSERT INTO task (id, shape, project, mode, worktree_path, created_at)
		VALUES ('taskaaaaaaai', 'delivery', 'one', 'interactive', '/w/i', ?)`, encodeTime(nowUTC())); err != nil {
		t.Fatalf("writing a task in the earlier shape: %v", err)
	}
	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("applying the shipped migration over the old row: %v", err)
	}
	task, err := s.Task(ctx, "taskaaaaaaai")
	if err != nil || task.Delivery.IsKnown() || task.Mode != "interactive" {
		t.Fatalf("the earlier task reads back as %+v, %v; want its posture unknown", task, err)
	}
	if _, found, err := s.LocalLanding(ctx, task.ID); found || err != nil {
		t.Fatalf("an earlier task has a landing record: %v, %v", found, err)
	}
}
