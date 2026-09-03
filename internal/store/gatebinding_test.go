package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestGateBindingIsReadableAndEnumerable(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	written, err := s.BindGate(ctx, "/checkouts/one", "aaaa")
	if err != nil {
		t.Fatalf("BindGate: %v", err)
	}
	if written.WorkingPath != "/checkouts/one" || written.GateID != "aaaa" {
		t.Fatalf("BindGate stored %+v", written)
	}
	if written.BoundAt.IsZero() || written.UpdatedAt.IsZero() {
		t.Fatalf("BindGate left a timestamp unset: %+v", written)
	}

	read, err := s.GateBinding(ctx, "/checkouts/one")
	if err != nil {
		t.Fatalf("GateBinding: %v", err)
	}
	if read != written {
		t.Fatalf("GateBinding returned %+v, want %+v", read, written)
	}

	listed, err := s.GateBindings(ctx, "aaaa")
	if err != nil {
		t.Fatalf("GateBindings: %v", err)
	}
	if len(listed) != 1 || listed[0] != written {
		t.Fatalf("GateBindings returned %+v", listed)
	}
}

// The whole point of the index is that a gate can be asked who points at it, so
// more than one binding on one gate has to be representable and has to come
// back whole. That state is what a working copy that moved without being
// unbound leaves behind.
func TestOneGateCanBeNamedByMoreThanOneWorkingCopy(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	for _, path := range []string{"/checkouts/second", "/checkouts/first"} {
		if _, err := s.BindGate(ctx, path, "aaaa"); err != nil {
			t.Fatalf("BindGate %s: %v", path, err)
		}
	}
	if _, err := s.BindGate(ctx, "/checkouts/other", "bbbb"); err != nil {
		t.Fatalf("BindGate: %v", err)
	}

	listed, err := s.GateBindings(ctx, "aaaa")
	if err != nil {
		t.Fatalf("GateBindings: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("GateBindings returned %d bindings, want 2: %+v", len(listed), listed)
	}
	if listed[0].WorkingPath != "/checkouts/first" || listed[1].WorkingPath != "/checkouts/second" {
		t.Fatalf("GateBindings is not ordered by working path: %+v", listed)
	}
	// The gate nobody asked about is not in the answer.
	for _, b := range listed {
		if b.GateID != "aaaa" {
			t.Fatalf("GateBindings returned a binding on another gate: %+v", b)
		}
	}
}

// A working copy is bound to at most one gate, so binding it again moves it
// rather than leaving it named by two gates at once.
func TestRebindingAWorkingCopyReplacesItsGateAndKeepsBoundAt(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	first, err := s.BindGate(ctx, "/checkouts/one", "aaaa")
	if err != nil {
		t.Fatalf("BindGate: %v", err)
	}
	second, err := s.BindGate(ctx, "/checkouts/one", "bbbb")
	if err != nil {
		t.Fatalf("BindGate again: %v", err)
	}
	if second.GateID != "bbbb" {
		t.Fatalf("rebinding left the gate at %s", second.GateID)
	}
	if !second.BoundAt.Equal(first.BoundAt) {
		t.Fatalf("rebinding rewrote BoundAt: %v then %v", first.BoundAt, second.BoundAt)
	}
	if second.UpdatedAt.Before(first.UpdatedAt) {
		t.Fatalf("rebinding moved UpdatedAt backwards: %v then %v", first.UpdatedAt, second.UpdatedAt)
	}

	if listed, err := s.GateBindings(ctx, "aaaa"); err != nil || len(listed) != 0 {
		t.Fatalf("the old gate still lists %+v (err %v)", listed, err)
	}
	if listed, err := s.GateBindings(ctx, "bbbb"); err != nil || len(listed) != 1 {
		t.Fatalf("the new gate lists %+v (err %v)", listed, err)
	}
}

func TestUnbindGateRemovesTheBindingAndSaysWhenThereWasNone(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	if _, err := s.BindGate(ctx, "/checkouts/one", "aaaa"); err != nil {
		t.Fatalf("BindGate: %v", err)
	}
	if err := s.UnbindGate(ctx, "/checkouts/one"); err != nil {
		t.Fatalf("UnbindGate: %v", err)
	}
	if _, err := s.GateBinding(ctx, "/checkouts/one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GateBinding after UnbindGate: %v", err)
	}
	if listed, err := s.GateBindings(ctx, "aaaa"); err != nil || len(listed) != 0 {
		t.Fatalf("the gate still lists %+v (err %v)", listed, err)
	}
	// Unbinding again reports that there was nothing to unbind rather than
	// succeeding over a row it never saw.
	if err := s.UnbindGate(ctx, "/checkouts/one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UnbindGate on a path with no binding: %v", err)
	}
}

// Nobody being bound is an answer. A gate nothing points at has to be
// distinguishable from a failed read, because it is the state that lets an
// initialization adopt a repository.
func TestAGateNothingIsBoundToListsNothingWithoutFailing(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	listed, err := s.GateBindings(ctx, "never-bound")
	if err != nil {
		t.Fatalf("GateBindings on an unbound gate: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("GateBindings on an unbound gate returned %+v", listed)
	}
	if _, err := s.GateBinding(ctx, "/checkouts/absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GateBinding on an unbound working copy: %v", err)
	}
}

func TestBindGateRefusesABindingThatNamesNothing(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	if _, err := s.BindGate(ctx, "  ", "aaaa"); err == nil {
		t.Fatal("BindGate accepted a binding with no working path")
	}
	if _, err := s.BindGate(ctx, "/checkouts/one", " "); err == nil {
		t.Fatal("BindGate accepted a binding with no gate identifier")
	}
	if listed, err := s.GateBindings(ctx, "aaaa"); err != nil || len(listed) != 0 {
		t.Fatalf("a refused binding was written anyway: %+v (err %v)", listed, err)
	}
}

func TestGateBindingAccessorsFailAfterClose(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.BindGate(ctx, "/checkouts/one", "aaaa"); err != nil {
		t.Fatalf("BindGate: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := s.BindGate(ctx, "/checkouts/two", "aaaa"); !errors.Is(err, ErrClosed) {
		t.Fatalf("BindGate after Close: %v", err)
	}
	if err := s.UnbindGate(ctx, "/checkouts/one"); !errors.Is(err, ErrClosed) {
		t.Fatalf("UnbindGate after Close: %v", err)
	}
	if _, err := s.GateBinding(ctx, "/checkouts/one"); !errors.Is(err, ErrClosed) {
		t.Fatalf("GateBinding after Close: %v", err)
	}
	if _, err := s.GateBindings(ctx, "aaaa"); !errors.Is(err, ErrClosed) {
		t.Fatalf("GateBindings after Close: %v", err)
	}
}

// A database that predates the ownership index has rows in it, and the
// migration that adds it has to reach that database rather than only a fresh
// one. This builds a database at the schema an older build left, with a row in
// it, and then opens it the way this build does.
func TestTheOwnershipIndexMigrationReachesAnOlderDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	// The schema as it stood before the ownership index, with a row written
	// against it.
	older, err := openPool(ctx, poolDSN(path, true), true)
	if err != nil {
		t.Fatalf("open the older database: %v", err)
	}
	if err := migrate(ctx, older, schema[:len(schema)-1]); err != nil {
		t.Fatalf("migrate to the older schema: %v", err)
	}
	now := encodeTime(nowUTC())
	if _, err := older.ExecContext(ctx, `
		INSERT INTO repository (id, working_path, upstream_url, fork_url, default_branch, created_at, updated_at)
		VALUES (?, ?, ?, NULL, ?, ?, ?)`,
		"repo-1", "/checkouts/one", "https://example.test/one.git", "main", now, now); err != nil {
		t.Fatalf("write a row against the older schema: %v", err)
	}
	if err := older.Close(); err != nil {
		t.Fatalf("close the older database: %v", err)
	}

	// Opening it with this build applies the migration.
	s := openStoreAt(t, path)
	if r, err := s.Repository(ctx, "repo-1"); err != nil {
		t.Fatalf("the row written against the older schema did not survive: %v", err)
	} else if r.WorkingPath != "/checkouts/one" {
		t.Fatalf("the row came back as %+v", r)
	}
	if _, err := s.BindGate(ctx, "/checkouts/one", "aaaa"); err != nil {
		t.Fatalf("BindGate after the migration: %v", err)
	}
	if listed, err := s.GateBindings(ctx, "aaaa"); err != nil || len(listed) != 1 {
		t.Fatalf("GateBindings after the migration returned %+v (err %v)", listed, err)
	}
}
