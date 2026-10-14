package store

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// The run copy sweep record is the latest sweep, one row per run: each
// outcome in the closed set is written and read back as written, a later
// sweep replaces an earlier one rather than adding beside it, and the read
// answers the row and not a history (P8). A run that has had no sweep reads
// ErrNotFound, which is the control that the row read afterwards was written
// by the call and not found standing.
func TestARunCopySweepRecordIsTheLatestSweep(t *testing.T) {
	principles.Cite(t, principles.P8)
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	if _, err := s.RunCopySweep(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a run with no sweep = %v, want ErrNotFound", err)
	}
	for _, w := range []RunCopySweep{
		{Outcome: SweepCleared, Ended: Known(1), Remaining: Known(0), Occasion: SweptAtFinish, Detail: "swept: 1 ended"},
		{Outcome: SweepNotCleared, Ended: Known(0), Remaining: Known(2), Occasion: SweptAtRecovery, Detail: "swept: 2 survived"},
		{Outcome: SweepNothingStanding, Occasion: SweptAtFinish},
		{Outcome: SweepUnsupported, Occasion: SweptAtRecovery, Detail: "no sweep"},
		{Outcome: SweepFailed, Occasion: SweptAtFinish, Detail: "listing failed"},
	} {
		w.Run = run.ID
		written, err := s.RecordRunCopySweep(ctx, w)
		if err != nil {
			t.Fatalf("recording a %s sweep: %v", w.Outcome, err)
		}
		read, err := s.RunCopySweep(ctx, run.ID)
		if err != nil {
			t.Fatalf("reading a %s sweep: %v", w.Outcome, err)
		}
		if read.SweptAt.IsZero() || !read.SweptAt.Equal(written.SweptAt) {
			t.Fatalf("a %s sweep reads swept at %v, written %v", w.Outcome, read.SweptAt, written.SweptAt)
		}
		read.SweptAt = w.SweptAt
		if read != w {
			t.Fatalf("a %s sweep reads back as %+v, want %+v", w.Outcome, read, w)
		}
	}
	var rows int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM run_copy_sweep WHERE run = ?`, run.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("five sweeps of one run left %d rows, want the one latest", rows)
	}
	// A repository forgotten takes its runs' sweep records with it, so a
	// sweep recorded does not leave a run that cannot be forgotten.
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunPending}, RunTerminated); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ForgetRepository(ctx, run.RepositoryID); err != nil {
		t.Fatalf("forgetting a repository whose run has a sweep record: %v", err)
	}
	if _, err := s.RunCopySweep(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a forgotten run's sweep record = %v, want ErrNotFound", err)
	}
}

// A sweep record outside the closed sets, or whose counts disagree with its
// outcome, is refused on write and on read, and a run that does not exist is
// ErrNotFound. The positive control is the one valid record written last,
// which is accepted, so the refusals are not a store that refuses everything.
func TestARunCopySweepRecordOutsideTheSetIsRefused(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	for name, w := range map[string]RunCopySweep{
		"an unknown outcome":              {Outcome: "guessed", Occasion: SweptAtFinish},
		"an unknown occasion":             {Outcome: SweepFailed, Occasion: "whenever"},
		"a listed outcome with no counts": {Outcome: SweepNotCleared, Occasion: SweptAtFinish},
		"counts with no listing":          {Outcome: SweepUnsupported, Ended: Known(0), Remaining: Known(0), Occasion: SweptAtFinish},
		"a cleared sweep with survivors":  {Outcome: SweepCleared, Ended: Known(0), Remaining: Known(1), Occasion: SweptAtFinish},
		"a negative count":                {Outcome: SweepNotCleared, Ended: Known(-1), Remaining: Known(1), Occasion: SweptAtFinish},
	} {
		w.Run = run.ID
		if _, err := s.RecordRunCopySweep(ctx, w); !errors.Is(err, ErrUnknownSweep) {
			t.Fatalf("%s = %v, want ErrUnknownSweep", name, err)
		}
	}
	if _, err := s.RecordRunCopySweep(ctx, RunCopySweep{Run: "nope", Outcome: SweepFailed, Occasion: SweptAtFinish}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a sweep of a missing run = %v, want ErrNotFound", err)
	}
	if _, err := s.RecordRunCopySweep(ctx, RunCopySweep{Run: run.ID, Outcome: SweepFailed, Occasion: SweptAtFinish}); err != nil {
		t.Fatalf("a valid record: %v", err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE run_copy_sweep SET outcome = 'guessed' WHERE run = ?`, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunCopySweep(ctx, run.ID); !errors.Is(err, ErrUnknownSweep) {
		t.Fatalf("reading a row outside the set = %v, want ErrUnknownSweep", err)
	}
}

// The sweep record's migration is additive: that one migration is undone by
// hand, a run is written without it, and the shipped list is applied again,
// which runs only the undone migration since every other is recorded. The run reads back unchanged and
// with no sweep recorded, and a sweep can then be recorded for it. The
// migration is found by name, so the test does not spell a version the
// shipped list may grow past.
func TestTheRunCopySweepMigrationKeepsEarlierRuns(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	var added migration
	for _, m := range schema {
		if m.name == runCopySweepMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", runCopySweepMigration)
	}
	// Back to the shape the migration before it left. A migration may not do
	// this, which is why it is done here by hand and not through one.
	if _, err := s.write.ExecContext(ctx, `DROP TABLE run_copy_sweep`); err != nil {
		t.Fatalf("rolling the schema back: %v", err)
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM schema_migration WHERE version = ?`, added.version); err != nil {
		t.Fatalf("forgetting the migration: %v", err)
	}
	run := seedRun(t, s)
	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("applying the shipped migration over the old row: %v", err)
	}
	again, err := s.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("the earlier run does not read back: %v", err)
	}
	if again.ID != run.ID || again.Status != run.Status || again.Branch != run.Branch {
		t.Fatalf("the earlier run reads back as %+v, want %+v", again, run)
	}
	if _, err := s.RunCopySweep(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an earlier run has a sweep record: %v", err)
	}
	if _, err := s.RecordRunCopySweep(ctx, RunCopySweep{Run: run.ID, Outcome: SweepUnsupported, Occasion: SweptAtRecovery}); err != nil {
		t.Fatalf("recording a sweep of the earlier run: %v", err)
	}
}
