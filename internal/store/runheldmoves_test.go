package store

import (
	"context"
	"testing"
)

// Every move into held counts itself, in the statement that makes the move,
// so a run that holds, is released and holds again reads two, and the count
// is what names each hold. A move that is not into held, and a repeated
// move to held that writes nothing, leave the count where it was.
func TestAMoveIntoHeldCountsItself(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if run.HeldMoves.IsKnown() {
		t.Fatalf("a new run reads %v held moves, want none recorded", run.HeldMoves)
	}
	steps := []struct {
		from []RunStatus
		to   RunStatus
		want int64
	}{
		{[]RunStatus{RunPending}, RunRunning, 0},
		{[]RunStatus{RunRunning}, RunHeld, 1},
		{[]RunStatus{RunRunning}, RunHeld, 1},
		{[]RunStatus{RunHeld}, RunRunning, 1},
		{[]RunStatus{RunRunning}, RunHeld, 2},
	}
	for i, step := range steps {
		moved, err := s.TransitionRun(ctx, run.ID, step.from, step.to)
		if err != nil {
			t.Fatalf("step %d: TransitionRun to %s: %v", i, step.to, err)
		}
		if got := moved.HeldMoves.Or(0); got != step.want {
			t.Fatalf("step %d: after the move to %s the run reads %d held moves, want %d", i, step.to, got, step.want)
		}
	}
}

// The count moves only with a move into held, by one: the schema refuses it
// changing on a run that does not move into held in the same statement, and
// a jump of more than one. The control is the update that does move into
// held, which the trigger lets through.
func TestTheSchemaCountsOnlyMovesIntoHeld(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	for name, statement := range map[string]string{
		"without a move":      `UPDATE run SET held_moves = 1 WHERE id = ?`,
		"with another move":   `UPDATE run SET status = 'running', held_moves = 1 WHERE id = ?`,
		"by more than one":    `UPDATE run SET status = 'held', held_moves = 2 WHERE id = ?`,
		"cleared on the move": `UPDATE run SET status = 'held', held_moves = NULL WHERE id = ?`,
	} {
		if err := rawWrite(ctx, s, statement, run.ID); err == nil {
			t.Errorf("a held count changed %s was accepted", name)
		}
	}
	if err := rawWrite(ctx, s, `UPDATE run SET status = 'held', held_moves = coalesce(held_moves, 0) + 1 WHERE id = ?`, run.ID); err != nil {
		t.Fatalf("a move into held counted by one was refused: %v", err)
	}
	if err := rawWrite(ctx, s, `UPDATE run SET held_moves = 2 WHERE id = ?`, run.ID); err == nil {
		t.Fatal("a held run's count moved with no move into held")
	}
}

// The migration is additive: the database is taken back to the shape without
// it and the shipped migration applied over the rows. A run held before it
// reads back with no count recorded, its first move into held after it counts
// one, and the trigger refuses again. The migration is found by name.
func TestTheRunHeldMovesMigrationKeepsEarlierRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	var added migration
	for _, m := range schema {
		if m.name == runHeldMovesMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", runHeldMovesMigration)
	}
	run := seedRun(t, s)
	for _, statement := range []string{
		`DROP TRIGGER run_held_moves_count_moves_into_held`,
		`ALTER TABLE run DROP COLUMN held_moves`,
	} {
		if _, err := s.write.ExecContext(ctx, statement); err != nil {
			t.Fatalf("rolling the schema back: %v", err)
		}
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE run SET status = 'held' WHERE id = ?`, run.ID); err != nil {
		t.Fatalf("holding the run before the migration: %v", err)
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM schema_migration WHERE version = ?`, added.version); err != nil {
		t.Fatalf("forgetting the migration: %v", err)
	}
	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("applying the shipped migration over the old rows: %v", err)
	}
	read, err := s.Run(ctx, run.ID)
	if err != nil || read.Status != RunHeld || read.HeldMoves.IsKnown() {
		t.Fatalf("the earlier run reads back %+v (%v), want it held with no count recorded", read, err)
	}
	if err := rawWrite(ctx, s, `UPDATE run SET held_moves = 7 WHERE id = ?`, run.ID); err == nil {
		t.Fatal("the reapplied migration did not refuse a count with no move")
	}
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunHeld}, RunRunning); err != nil {
		t.Fatalf("releasing: %v", err)
	}
	held, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunRunning}, RunHeld)
	if err != nil || held.HeldMoves != Known(int64(1)) {
		t.Fatalf("the first move into held after the migration reads %v (%v), want one", held.HeldMoves, err)
	}
}
