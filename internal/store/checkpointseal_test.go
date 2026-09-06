package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// insertCheckpointRow attempts the row the retired one-row checkpoint record
// used to hold: a run this store has, an empty state, a position, no open
// decision, the first revision, and a well-formed timestamp.
//
// The values are what the shipped WriteCheckpoint wrote before it was removed,
// so every column, type, and reference the table declares is satisfied and the
// only thing left that can refuse the row is migration 6's trigger.
func insertCheckpointRow(ctx context.Context, db *sql.DB, runID string) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO checkpoint (run_id, state, position, open_decision, revision, written_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		runID, []byte{}, "review", Unknown[string](), int64(1), encodeTime(nowUTC()))
	return err
}

// A run's position has one record, and the table that used to be the second one
// can no longer hold a row. The Go surface that read and wrote it is gone, so
// what is checked here is the database's own refusal, which is what a caller
// that added new SQL for this table would run into.
func TestTheOneRowCheckpointTableTakesNoRows(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	err := insertCheckpointRow(ctx, s.write, run.ID)
	if err == nil {
		t.Fatal("the checkpoint table accepted a row, so a run can still be given a second position record")
	}
	if !strings.Contains(err.Error(), "the checkpoint table is not a record") {
		t.Fatalf("the row was refused for some other reason than the seal: %v", err)
	}
}

// The refusal above is worth nothing unless the row would otherwise have landed:
// a malformed row, a missing reference, or a type the table will not take would
// produce a failure that looks the same to a test that only checked for one.
//
// So the same statement is run again with the trigger dropped, on a store of its
// own. It succeeds, which says the row satisfies everything else the table
// declares and migration 6's trigger is the whole of what refused it.
func TestTheCheckpointSealIsWhatRefusesTheRow(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	if _, err := s.write.ExecContext(ctx, `DROP TRIGGER checkpoint_is_not_a_record`); err != nil {
		t.Fatalf("dropping the seal: %v", err)
	}
	if err := insertCheckpointRow(ctx, s.write, run.ID); err != nil {
		t.Fatalf("with the seal gone the row still would not land, so the refusal above"+
			" did not demonstrate the seal: %v", err)
	}
}

// An INSERT-only trigger says nothing about rows that are already there, so
// migration 6 empties the table before it seals it. A database migrated forward
// from a build that wrote this record arrives at 6 holding one, and this drives
// that case with the shipped migration rather than a copy of it: the seal is
// removed, the row is written, migration 6's record is forgotten, and migrate
// replays it against exactly the state such a database is in.
//
// migrate returning no error is also what shows the DELETE keeps the migration
// additive, since applyMigration runs verifyAdditive inside the same
// transaction as the statements.
func TestTheSealEmptiesACheckpointTableThatAlreadyHasARow(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	if _, err := s.write.ExecContext(ctx, `DROP TRIGGER checkpoint_is_not_a_record`); err != nil {
		t.Fatalf("dropping the seal: %v", err)
	}
	if err := insertCheckpointRow(ctx, s.write, run.ID); err != nil {
		t.Fatalf("writing the row a forward-migrated database would carry: %v", err)
	}
	if _, err := s.write.ExecContext(ctx,
		`DELETE FROM schema_migration WHERE version = ?`, lastVersion(schema)); err != nil {
		t.Fatalf("forgetting the seal migration: %v", err)
	}

	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("replaying the seal over a table that already had a row: %v", err)
	}

	var rows int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM checkpoint`).Scan(&rows); err != nil {
		t.Fatalf("counting checkpoint rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("the seal left %d row(s) in the checkpoint table, so a run migrated"+
			" forward still carries a second position record", rows)
	}
	if err := insertCheckpointRow(ctx, s.write, run.ID); err == nil {
		t.Fatal("the replayed migration emptied the table but did not seal it")
	}
}
