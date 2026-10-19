package store

import (
	"context"
	"errors"
	"testing"
)

// The reduced path records that validation was not performed, once, on the
// run row. A second write of the same fact is a no-op, a missing run is
// ErrNotFound, and a revision of a written value is refused.
func TestARunsValidationFlagIsWrittenOnceAndReadBack(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if run.ValidationNotPerformed.IsKnown() {
		t.Fatalf("a new run carries ValidationNotPerformed %v", run.ValidationNotPerformed)
	}
	if err := s.SetRunValidationNotPerformed(ctx, run.ID); err != nil {
		t.Fatalf("SetRunValidationNotPerformed: %v", err)
	}
	read, err := s.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if flag, ok := read.ValidationNotPerformed.Get(); !ok || !flag {
		t.Fatalf("after the write the run reads %v, %v; want known true", flag, ok)
	}
	listed, err := s.RunsForRepository(ctx, run.RepositoryID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("RunsForRepository = %v, %v", listed, err)
	}
	if listed[0].ValidationNotPerformed != Known(true) {
		t.Fatalf("the listing reads %v, want known true", listed[0].ValidationNotPerformed)
	}
	if err := s.SetRunValidationNotPerformed(ctx, run.ID); err != nil {
		t.Fatalf("a second write of the same fact: %v", err)
	}
	if err := s.SetRunValidationNotPerformed(ctx, "no-such-run"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing run: %v, want ErrNotFound", err)
	}
	for name, statement := range map[string]string{
		"clearing the flag": `UPDATE run SET validation_not_performed = NULL WHERE id = ?`,
		"writing false":     `UPDATE run SET validation_not_performed = 0 WHERE id = ?`,
	} {
		if err := rawWrite(ctx, s, statement, run.ID); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := rawWrite(ctx, s, `UPDATE run SET intent = 'still validate' WHERE id = ?`, run.ID); err != nil {
		t.Fatalf("an update that does not name the column was refused: %v", err)
	}
}

// The migration is additive: taken back to the shape without it, the shipped
// migration applied over the rows leaves every run with the flag unknown.
func TestTheRunValidationMigrationKeepsEarlierRuns(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	var added migration
	for _, m := range schema {
		if m.name == runValidationNotPerformedMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", runValidationNotPerformedMigration)
	}
	run := seedRun(t, s)
	for _, statement := range []string{
		`DROP TRIGGER run_validation_not_performed_is_written_once`,
		`ALTER TABLE run DROP COLUMN validation_not_performed`,
	} {
		if _, err := s.write.ExecContext(ctx, statement); err != nil {
			t.Fatalf("rolling the schema back: %v", err)
		}
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM schema_migration WHERE version = ?`, added.version); err != nil {
		t.Fatalf("forgetting the migration: %v", err)
	}
	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("applying the shipped migration over the old rows: %v", err)
	}
	read, err := s.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if read.ValidationNotPerformed.IsKnown() || read.Status != run.Status || read.Branch != run.Branch {
		t.Fatalf("an earlier run reads back %+v, want it as it stood with the flag unknown", read)
	}
	if err := s.SetRunValidationNotPerformed(ctx, run.ID); err != nil {
		t.Fatalf("writing after the migration: %v", err)
	}
	if err := rawWrite(ctx, s, `UPDATE run SET validation_not_performed = NULL WHERE id = ?`, run.ID); err == nil {
		t.Fatal("the reapplied trigger let the flag be cleared")
	}
}
