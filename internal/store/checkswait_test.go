package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A wait is armed once and then kept: reading the same base again at a later
// instant must not move when the wait began, or an idle timeout measured from
// it would never elapse for a run whose checks stage keeps re-reading.
//
// The positive control is the other base. Without it a store that never wrote
// anything after the first arm would pass the first half for the wrong reason.
func TestAChecksWaitIsKeptForTheSameBaseAndReArmedForAnother(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	began := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	first, err := s.ArmChecksWait(ctx, "run-1", "base-a", began)
	if err != nil {
		t.Fatalf("ArmChecksWait: %v", err)
	}
	if !first.ArmedAt.Equal(began) || first.Base != "base-a" {
		t.Fatalf("a first arm recorded %+v, want base-a armed at %s", first, began)
	}

	again, err := s.ArmChecksWait(ctx, "run-1", "base-a", began.Add(5*time.Hour))
	if err != nil {
		t.Fatalf("ArmChecksWait: %v", err)
	}
	if !again.ArmedAt.Equal(began) {
		t.Fatalf("reading the same base again moved the wait to %s, want it kept at %s", again.ArmedAt, began)
	}

	advanced := began.Add(9 * time.Hour)
	moved, err := s.ArmChecksWait(ctx, "run-1", "base-b", advanced)
	if err != nil {
		t.Fatalf("ArmChecksWait: %v", err)
	}
	if !moved.ArmedAt.Equal(advanced) || moved.Base != "base-b" {
		t.Fatalf("a base that moved left the wait at %+v, want base-b re-armed at %s", moved, advanced)
	}

	other, err := s.ArmChecksWait(ctx, "run-2", "base-b", advanced.Add(time.Hour))
	if err != nil {
		t.Fatalf("ArmChecksWait: %v", err)
	}
	if !other.ArmedAt.Equal(advanced.Add(time.Hour)) {
		t.Fatalf("another run's wait began at %s, want its own instant", other.ArmedAt)
	}
}

// The wait outlives the process that armed it, which is the reason it is a
// row rather than a value the stage holds: a service that restarts resumes
// the checks stage, and a wait that began again then would reset the timeout
// on every restart.
func TestAChecksWaitSurvivesReopeningTheStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	began := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	s, err := Open(ctx, path, WithRedactor(workingRedactor()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.ArmChecksWait(ctx, "run-1", "base-a", began); err != nil {
		t.Fatalf("ArmChecksWait: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openStoreAt(t, path)
	kept, err := reopened.ArmChecksWait(ctx, "run-1", "base-a", began.Add(100*time.Hour))
	if err != nil {
		t.Fatalf("ArmChecksWait: %v", err)
	}
	if !kept.ArmedAt.Equal(began) {
		t.Fatalf("after reopening, the wait began at %s, want %s", kept.ArmedAt, began)
	}
}

// Ending a wait is what makes the next one begin afresh, and ending one that
// never began is not a failure.
func TestEndingAChecksWaitLetsTheNextBeginAfresh(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	began := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	if err := s.EndChecksWait(ctx, "run-1"); err != nil {
		t.Fatalf("ending a wait that never began: %v", err)
	}
	if _, err := s.ArmChecksWait(ctx, "run-1", "base-a", began); err != nil {
		t.Fatalf("ArmChecksWait: %v", err)
	}
	if err := s.EndChecksWait(ctx, "run-1"); err != nil {
		t.Fatalf("EndChecksWait: %v", err)
	}
	later := began.Add(3 * time.Hour)
	fresh, err := s.ArmChecksWait(ctx, "run-1", "base-a", later)
	if err != nil {
		t.Fatalf("ArmChecksWait: %v", err)
	}
	if !fresh.ArmedAt.Equal(later) {
		t.Fatalf("a wait armed after the last one ended began at %s, want %s", fresh.ArmedAt, later)
	}
}

// A wait keyed by nothing, or armed against a base nobody read, is refused.
func TestAChecksWaitRefusesAnEmptyRunOrBase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.ArmChecksWait(ctx, "", "base-a", time.Now()); err == nil {
		t.Fatal("a wait naming no run was accepted")
	}
	if _, err := s.ArmChecksWait(ctx, "run-1", " ", time.Now()); err == nil {
		t.Fatal("a wait naming no base was accepted")
	}
}
