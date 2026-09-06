package store

import (
	"context"
	"database/sql"
	"path/filepath"
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

// sealMigrationVersion is the version of the migration that empties and seals
// the checkpoint table, found by name in the shipped list.
//
// It is looked up rather than assumed to be the last entry, because the day the
// schema grows a migration past it, a test anchored to the end of the list would
// forget and replay that one instead: the seal would stay recorded, never re-run,
// and the failure would name the seal for a defect somewhere else entirely.
func sealMigrationVersion(t *testing.T) int {
	t.Helper()
	for _, m := range schema {
		if m.name == sealCheckpointTableMigration {
			return m.version
		}
	}
	t.Fatalf("the shipped schema has no migration named %q, so the seal cannot be replayed",
		sealCheckpointTableMigration)
	return 0
}

// An INSERT-only trigger says nothing about rows that are already there, so the
// seal migration empties the table before it seals it. A database migrated
// forward from a build that wrote this record arrives at the seal holding one,
// and this drives that case with the shipped migration rather than a copy of it:
// the seal is removed, the row is written, the seal migration's record is
// forgotten, and migrate replays it against exactly the state such a database
// is in.
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
		`DELETE FROM schema_migration WHERE version = ?`, sealMigrationVersion(t)); err != nil {
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

// A database written by a build that still had the one-row record is what an
// existing installation has on disk, and opening it is how that installation
// meets this change. The three tests above drive the seal inside one open
// store; this one drives the upgrade itself, through the same Open a caller
// uses, on a file that is closed and reopened in between.
//
// The old state is built rather than described: a store is opened, given a run
// and a checkpoint history, and then rolled back to what the build before the
// seal left behind, which is the trigger gone and migration 6 unrecorded. The
// row that build could write is written while the table will still take it.
//
// What the reopen has to show is all four halves of the claim: it opens at all,
// rather than failing the additive check or the recorded-migration check; the
// retired row is gone; the table will not take another; and the records that
// were never this migration's business are all still there.
func TestOpeningADatabaseWrittenBeforeTheSealRetiresItsRow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	s := openStoreAt(t, path)
	run := seedRun(t, s)
	if _, err := s.AppendGraphCheckpoint(ctx, run.ID, "", 0, []byte(`{"position":"review"}`)); err != nil {
		t.Fatalf("appending the history entry the run's position lives in: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("closing the store: %v", err)
	}

	unseal(ctx, t, path, run.ID)

	reopened := openStoreAt(t, path)
	t.Logf("reopened %s with this build", filepath.Base(path))

	var rows int
	if err := reopened.read.QueryRowContext(ctx, `SELECT count(*) FROM checkpoint`).Scan(&rows); err != nil {
		t.Fatalf("counting checkpoint rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("the upgrade left %d row(s) in the checkpoint table, so the run still"+
			" carries a second position record", rows)
	}
	t.Logf("checkpoint table after the upgrade: %d rows", rows)

	err := insertCheckpointRow(ctx, reopened.write, run.ID)
	if err == nil {
		t.Fatal("the upgraded database still accepts a checkpoint row")
	}
	if !strings.Contains(err.Error(), "the checkpoint table is not a record") {
		t.Fatalf("the row was refused for some other reason than the seal: %v", err)
	}
	t.Logf("writing one again: %v", err)

	if _, err := reopened.Run(ctx, run.ID); err != nil {
		t.Fatalf("the upgrade lost the run the retired row hung off: %v", err)
	}
	history, err := reopened.GraphCheckpointHistory(ctx, run.ID)
	if err != nil {
		t.Fatalf("reading the checkpoint history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("the upgrade left %d history entries, want 1: the record that owns a"+
			" run's position must survive the retirement of the one that did not", len(history))
	}
	t.Logf("run %s and its %d-entry checkpoint history survived", run.ID, len(history))
}

// unseal rolls a database back to what the build before the seal left on disk:
// migration 6 unrecorded and its trigger gone, holding the one-row record that
// build wrote. It works on the file directly, because the accessor that wrote
// that row is what this change removed and no Store can be opened on the file
// without migrating it forward again.
func unseal(ctx context.Context, t *testing.T, path, runID string) {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolving %q: %v", path, err)
	}
	db, err := sql.Open(driverName, poolDSN(abs, true))
	if err != nil {
		t.Fatalf("opening %s directly: %v", abs, err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx, `DROP TRIGGER checkpoint_is_not_a_record`); err != nil {
		t.Fatalf("dropping the seal: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM schema_migration WHERE version = ?`, sealMigrationVersion(t)); err != nil {
		t.Fatalf("forgetting the seal migration: %v", err)
	}
	if err := insertCheckpointRow(ctx, db, runID); err != nil {
		t.Fatalf("writing the row the build before the seal wrote: %v", err)
	}
}
