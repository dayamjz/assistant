package store

import (
	"context"
	"errors"
	"testing"
)

// begin settles whether a run has a code host once, and a listing that has
// only the record phrases a completed no-code-host run as pushed from that
// flag. The settlement is written once: a second write of the same value is
// a no-op so a begin that failed after the first write can retry, a flip is
// refused, and a run that does not exist is ErrNotFound.
func TestARunsCodeHostSettlementIsWrittenOnceAndReadBack(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if run.NoCodeHost.IsKnown() {
		t.Fatalf("a new run carries NoCodeHost %v", run.NoCodeHost)
	}
	if err := s.SetRunNoCodeHost(ctx, run.ID); err != nil {
		t.Fatalf("SetRunNoCodeHost: %v", err)
	}
	read, err := s.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if noHost, ok := read.NoCodeHost.Get(); !ok || !noHost {
		t.Fatalf("after SetRunNoCodeHost the run reads %v, %v; want known true", noHost, ok)
	}
	listed, err := s.RunsForRepository(ctx, run.RepositoryID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("RunsForRepository = %v, %v", listed, err)
	}
	if listed[0].NoCodeHost != Known(true) {
		t.Fatalf("the listing reads NoCodeHost %v, want known true", listed[0].NoCodeHost)
	}
	if err := s.SetRunNoCodeHost(ctx, run.ID); err != nil {
		t.Fatalf("a second SetRunNoCodeHost of the same settlement: %v", err)
	}
	if err := s.SetRunHasCodeHost(ctx, run.ID); err == nil {
		t.Fatal("flipping a no-code-host settlement to has-code-host was accepted")
	}
	if err := s.SetRunNoCodeHost(ctx, "no-such-run"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetRunNoCodeHost on a missing run: %v, want ErrNotFound", err)
	}

	other, err := s.CreateRun(ctx, Run{
		ID:            "run-2",
		RepositoryID:  run.RepositoryID,
		Branch:        "other",
		SubmittedHead: "bbbb",
		Base:          "bbbb",
		Intent:        "validate",
		IntentSource:  "push",
		Build:         testBuild(),
		ConfigDigest:  "cfg-2",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.SetRunHasCodeHost(ctx, other.ID); err != nil {
		t.Fatalf("SetRunHasCodeHost: %v", err)
	}
	host, err := s.Run(ctx, other.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if noHost, ok := host.NoCodeHost.Get(); !ok || noHost {
		t.Fatalf("after SetRunHasCodeHost the run reads %v, %v; want known false", noHost, ok)
	}
	if err := s.SetRunHasCodeHost(ctx, other.ID); err != nil {
		t.Fatalf("a second SetRunHasCodeHost of the same settlement: %v", err)
	}
	if err := s.SetRunNoCodeHost(ctx, other.ID); err == nil {
		t.Fatal("flipping a has-code-host settlement to no-code-host was accepted")
	}
	for name, statement := range map[string]string{
		"clearing the settlement":  `UPDATE run SET no_code_host = NULL WHERE id = ?`,
		"revising the settlement":  `UPDATE run SET no_code_host = 0 WHERE id = ?`,
		"forcing the other answer": `UPDATE run SET no_code_host = 1 WHERE id = ?`,
	} {
		if err := rawWrite(ctx, s, statement, other.ID); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := rawWrite(ctx, s, `UPDATE run SET intent = 'still validate' WHERE id = ?`, other.ID); err != nil {
		t.Fatalf("an update that does not name the column was refused: %v", err)
	}
}

// The migration is additive: taken back to the shape without it, the shipped
// migration applied over the rows leaves every run as it stood with no
// settlement, and the trigger refuses again. It is found by name.
func TestTheRunNoCodeHostMigrationKeepsEarlierRuns(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	var added migration
	for _, m := range schema {
		if m.name == runNoCodeHostMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", runNoCodeHostMigration)
	}
	run := seedRun(t, s)
	for _, statement := range []string{
		`DROP TRIGGER run_no_code_host_is_written_once`,
		`ALTER TABLE run DROP COLUMN no_code_host`,
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
	if read.NoCodeHost.IsKnown() || read.Status != run.Status || read.Branch != run.Branch {
		t.Fatalf("the earlier run reads back %+v, want it as it stood with no settlement", read)
	}
	if err := s.SetRunNoCodeHost(ctx, run.ID); err != nil {
		t.Fatalf("settling after the migration: %v", err)
	}
	if err := rawWrite(ctx, s, `UPDATE run SET no_code_host = NULL WHERE id = ?`, run.ID); err == nil {
		t.Fatal("the reapplied trigger let the settlement be cleared")
	}
}
