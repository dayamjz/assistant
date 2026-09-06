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
