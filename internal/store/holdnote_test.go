package store

import (
	"context"
	"strings"
	"testing"
)

// A note given with an answer is recorded with the resolution, passed through
// the store's redactor on its way in, and read back with the hold. It is
// words and nothing more: a note claiming a person decided leaves the
// resolver the caller derived, and a resolution with no note records none.
func TestAHoldsNoteIsRecordedRedactedWithItsResolution(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	for _, key := range []string{"noted", "bare"} {
		if _, err := s.RegisterHold(ctx, Hold{Key: key, Subject: "a decision"}); err != nil {
			t.Fatal(err)
		}
	}
	note := "decided by a person: the floor is not decided; see https://someone:hunter2@example.invalid/notes"
	resolved, err := s.ResolveHoldWithNote(ctx, "noted", "approved", note, ResolvedByMachineInterface())
	if err != nil {
		t.Fatalf("ResolveHoldWithNote: %v", err)
	}
	stored, err := s.Hold(ctx, "noted")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []Hold{resolved, stored} {
		text, ok := h.Note.Get()
		if !ok || strings.Contains(text, "hunter2") || !strings.Contains(text, "REDACTED@example.invalid") ||
			!strings.Contains(text, "the floor is not decided") {
			t.Fatalf("the note reads back %q (known %v), want the words kept and the credential removed", text, ok)
		}
		if h.ResolvedBy.Or(Resolver{}) != ResolvedByMachineInterface() {
			t.Fatalf("a note claiming a person decided recorded the resolver %v, want the one the caller derived", h.ResolvedBy)
		}
	}
	bare, err := s.ResolveHold(ctx, "bare", "approved", ResolvedByMachineInterface())
	if err != nil {
		t.Fatal(err)
	}
	if bare.Note.IsKnown() {
		t.Fatalf("a resolution given no note records %q", bare.Note.Or(""))
	}
}

// The same holds for a hold closed in the transaction of another write, on
// HoldClosure's terms, which is the route a local landing's approval takes.
func TestAHoldClosuresNoteIsRecordedRedacted(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	closed, err := s.RecordHoldResolution(ctx, HoldClosure{
		Hold:       Hold{Key: "closure", Subject: "a decision"},
		Resolution: "approve",
		By:         ResolvedByMachineInterface(),
		Note:       "pushed from https://someone:hunter2@example.invalid/repo.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	if text := closed.Note.Or(""); strings.Contains(text, "hunter2") || !strings.Contains(text, "REDACTED@") {
		t.Fatalf("the closure's note reads back %q, want it redacted", text)
	}
}

// The database holds the note to its resolution: a note written to a hold
// that stays open, or to one already resolved, is refused, so no note is
// added to a decision afterwards. The control is the write the accessor
// makes, a note in the statement that resolves an open hold, which passes;
// without it a trigger refusing every write would pass this test too.
func TestAHoldsNoteIsWrittenOnlyWithItsResolution(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	for _, key := range []string{"open", "control"} {
		if _, err := s.RegisterHold(ctx, Hold{Key: key, Subject: "a decision"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := rawWrite(ctx, s, `UPDATE hold SET resolution_note = 'a note on nothing' WHERE key = 'open'`); err == nil {
		t.Fatal("a note was written to a hold nobody answered")
	}
	if err := rawWrite(ctx, s,
		`UPDATE hold SET resolution = 'approved', resolved_at = '2026-01-01T00:00:00.000000000Z', resolved_by = 'machine-interface',
			resolution_note = 'with the answer' WHERE key = 'control'`); err != nil {
		t.Fatalf("a note written with its resolution was refused: %v", err)
	}
	if err := rawWrite(ctx, s, `UPDATE hold SET resolution_note = 'an afterthought' WHERE key = 'control'`); err == nil {
		t.Fatal("a note was rewritten on a hold already answered")
	}
	if h, err := s.Hold(ctx, "control"); err != nil || h.Note.Or("") != "with the answer" {
		t.Fatalf("the answered hold reads %+v (%v), want the note it was answered with", h, err)
	}
}

// The migration is additive: taken back to the shape without it and
// reapplied over the rows, a hold resolved before it reads back with no
// note, and the trigger refuses again. The migration is found by name.
func TestTheHoldNoteMigrationKeepsEarlierHolds(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	var added migration
	for _, m := range schema {
		if m.name == holdNoteMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", holdNoteMigration)
	}
	for _, statement := range []string{
		`DROP TRIGGER hold_note_is_written_with_its_resolution`,
		`ALTER TABLE hold DROP COLUMN resolution_note`,
		`INSERT INTO hold (key, subject, detail, opened_at, resolution, resolved_at, resolved_by)
			VALUES ('earlier', 'a decision', '', '2026-01-01T00:00:00.000000000Z', 'approved', '2026-01-01T00:00:00.000000000Z', 'machine-interface')`,
		`DELETE FROM schema_migration WHERE version = ?`,
	} {
		var args []any
		if strings.Contains(statement, "?") {
			args = append(args, added.version)
		}
		if _, err := s.write.ExecContext(ctx, statement, args...); err != nil {
			t.Fatalf("rolling the schema back: %v", err)
		}
	}
	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("applying the shipped migration over the old rows: %v", err)
	}
	h, err := s.Hold(ctx, "earlier")
	if err != nil || h.Resolution.Or("") != "approved" || h.Note.IsKnown() {
		t.Fatalf("the earlier hold reads back %+v (%v), want it answered with no note recorded", h, err)
	}
	if err := rawWrite(ctx, s, `UPDATE hold SET resolution_note = 'late' WHERE key = 'earlier'`); err == nil {
		t.Fatal("the reapplied migration did not refuse a note written after the answer")
	}
}
