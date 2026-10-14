package store

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// roundLimitPark is a park record of the shape internal/service writes for a
// stage's round limit.
var roundLimitPark = RunPark{Bound: ParkRoundLimit, Stage: "review", Node: "fix:review", Limit: 1, Checkpoint: 7}

// A park is recorded with the move that ends the run, and read back on the
// record by every accessor that returns one, so a listing and a single read
// say the same. A run a cancellation already ended keeps reading as that
// ending: the move finds it terminated, writes nothing, and records no park.
// P8 is what this claims: the record is the current state a surface reads the
// park from, written once at the ending rather than reconstructed later from a
// history's last entry.
func TestAParkIsRecordedWithTheMoveThatEndsTheRun(t *testing.T) {
	principles.Cite(t, principles.P8)
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if run.Park.IsKnown() {
		t.Fatalf("a new run carries park %v", run.Park)
	}
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunPending}, RunRunning); err != nil {
		t.Fatalf("starting the run: %v", err)
	}

	moved, err := s.TransitionRunOnPark(ctx, run.ID, []RunStatus{RunRunning}, roundLimitPark)
	if err != nil {
		t.Fatalf("TransitionRunOnPark: %v", err)
	}
	if moved.Status != RunTerminated || moved.Park != Known(roundLimitPark) {
		t.Fatalf("the move returned %s with park %v, want terminated with %+v", moved.Status, moved.Park, roundLimitPark)
	}
	read, err := s.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	listed, err := s.RunsForRepository(ctx, run.RepositoryID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("RunsForRepository = %v, %v", listed, err)
	}
	if read.Park != Known(roundLimitPark) || listed[0].Park != Known(roundLimitPark) {
		t.Fatalf("the record reads back park %v and lists %v, want %+v", read.Park, listed[0].Park, roundLimitPark)
	}

	// Moving it again changes nothing, the park included.
	again, err := s.TransitionRunOnPark(ctx, run.ID, []RunStatus{RunRunning},
		RunPark{Bound: ParkStepBudget, Stage: "test", Node: "test", Limit: 40, Checkpoint: 9})
	if err != nil || again.Park != Known(roundLimitPark) {
		t.Fatalf("a second park of an ended run returned %v, %v; want the first park unchanged", again.Park, err)
	}
}

// A run a person ended before a bound's park was recorded is not given one:
// the move finds it already terminated and writes nothing, so the record goes
// on reading as the cancellation it was.
func TestACancelledRunIsNotGivenAPark(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunPending}, RunTerminated); err != nil {
		t.Fatalf("ending the run: %v", err)
	}
	moved, err := s.TransitionRunOnPark(ctx, run.ID, []RunStatus{RunPending, RunRunning, RunHeld}, roundLimitPark)
	if err != nil {
		t.Fatalf("TransitionRunOnPark on an ended run: %v", err)
	}
	if moved.Park.IsKnown() {
		t.Fatalf("the cancelled run was given park %v", moved.Park)
	}
}

// A park no bound produces is refused before anything is written, so a run is
// never ended on a record nobody can read; and a run in a status the caller
// did not name is refused as the status error every move gives.
func TestAParkOfNoKnownShapeIsRefused(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	for name, park := range map[string]RunPark{
		"an unknown bound":          {Bound: "patience", Stage: "review", Node: "review", Limit: 1, Checkpoint: 1},
		"no node":                   {Bound: ParkStepBudget, Limit: 3, Checkpoint: 1},
		"no checkpoint":             {Bound: ParkStepBudget, Node: "review", Limit: 3},
		"a round limit on no stage": {Bound: ParkRoundLimit, Node: "fix:review", Limit: 1, Checkpoint: 1},
		"a convergence limit":       {Bound: ParkConvergence, Stage: "review", Node: "review", Limit: 2, Checkpoint: 1},
		"a budget of nothing":       {Bound: ParkStepBudget, Node: "review", Checkpoint: 1},
	} {
		if _, err := s.TransitionRunOnPark(ctx, run.ID, []RunStatus{RunPending}, park); err == nil {
			t.Errorf("%s was recorded", name)
		}
	}
	if read, err := s.Run(ctx, run.ID); err != nil || read.Status != RunPending || read.Park.IsKnown() {
		t.Fatalf("a refused park moved the run: %+v, %v", read, err)
	}
	var wrong *RunStatusError
	if _, err := s.TransitionRunOnPark(ctx, run.ID, []RunStatus{RunHeld}, roundLimitPark); !errors.As(err, &wrong) {
		t.Fatalf("a park from a status the run is not in returned %v, want a status error", err)
	}
}

// The column is written once, by the move that ends the run, and the schema
// refuses every other write rather than leaving that to the absence of a
// second writer: revising a park, clearing one, and giving one to a run that
// is not terminated. An update that does not name the column is not refused.
func TestAParkIsWrittenOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if err := rawWrite(ctx, s, `UPDATE run SET park = '{}' WHERE id = ?`, run.ID); err == nil {
		t.Fatal("a pending run was given a park")
	}
	if _, err := s.TransitionRunOnPark(ctx, run.ID, []RunStatus{RunPending}, roundLimitPark); err != nil {
		t.Fatalf("TransitionRunOnPark: %v", err)
	}
	for name, statement := range map[string]string{
		"revising a park": `UPDATE run SET park = '{"bound":"step_budget","node":"x","limit":2,"checkpoint":1}' WHERE id = ?`,
		"clearing a park": `UPDATE run SET park = NULL WHERE id = ?`,
	} {
		if err := rawWrite(ctx, s, statement, run.ID); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := rawWrite(ctx, s, `UPDATE run SET intent = 'still validate' WHERE id = ?`, run.ID); err != nil {
		t.Fatalf("an update that does not name the park was refused: %v", err)
	}
	if read, _ := s.Run(ctx, run.ID); read.Park != Known(roundLimitPark) {
		t.Fatalf("the park reads back %v, want %+v", read.Park, roundLimitPark)
	}
}

// The migration is additive: the database is taken back to the shape without
// it, by dropping its trigger and its column and forgetting only its version,
// and the shipped migration is applied over the rows. Every run reads back as
// it stood, with no park, and the trigger refuses again. The migration is
// found by name, so the test spells no version the shipped list may grow past.
func TestTheRunParkMigrationKeepsEarlierRuns(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	var added migration
	for _, m := range schema {
		if m.name == runParkMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", runParkMigration)
	}
	run := seedRun(t, s)
	for _, statement := range []string{
		`DROP TRIGGER run_park_is_written_once`,
		`ALTER TABLE run DROP COLUMN park`,
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
	if read.Park.IsKnown() || read.Status != run.Status || read.Branch != run.Branch {
		t.Fatalf("the earlier run reads back %+v, want it as it stood with no park", read)
	}
	if err := rawWrite(ctx, s, `UPDATE run SET park = '{}' WHERE id = ?`, run.ID); err == nil {
		t.Fatal("the reapplied migration did not refuse a park on a pending run")
	}
}
