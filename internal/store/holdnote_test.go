package store

import (
	"context"
	"strings"
	"testing"
)

// A note given with an answer passes the store's redactor on its way into
// the column, on both routes that resolve a hold: ResolveHoldNoted, and a
// HoldClosure closed in the transaction of another write, which is the
// route a local landing's approval takes. The words around the credential
// are kept, so the control that the note was recorded at all is in the same
// assertion. A note claiming a person decided leaves the resolver the
// caller derived.
func TestAHoldsNoteIsStoredRedacted(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.RegisterHold(ctx, Hold{Key: "noted", Subject: "a decision"}); err != nil {
		t.Fatal(err)
	}
	note := "decided by a person: the floor is not decided; see https://someone:hunter2@example.invalid/notes"
	resolved, err := s.ResolveHoldNoted(ctx, "noted", "approved", note, ResolvedByMachineInterface())
	if err != nil {
		t.Fatalf("ResolveHoldNoted: %v", err)
	}
	stored, err := s.Hold(ctx, "noted")
	if err != nil {
		t.Fatal(err)
	}
	closed, err := s.RecordHoldResolution(ctx, HoldClosure{
		Hold:       Hold{Key: "closure", Subject: "a decision"},
		Resolution: "approve",
		Note:       note,
		By:         ResolvedByMachineInterface(),
	})
	if err != nil {
		t.Fatalf("RecordHoldResolution: %v", err)
	}
	for _, h := range []Hold{resolved, stored, closed} {
		text, ok := h.ResolutionNote.Get()
		if !ok || strings.Contains(text, "hunter2") || !strings.Contains(text, "REDACTED@example.invalid") ||
			!strings.Contains(text, "the floor is not decided") {
			t.Fatalf("hold %s reads back the note %q (known %v), want the words kept and the credential removed", h.Key, text, ok)
		}
		if h.ResolvedBy.Or(Resolver{}) != ResolvedByMachineInterface() {
			t.Fatalf("hold %s: a note claiming a person decided recorded the resolver %v, want the one the caller derived",
				h.Key, h.ResolvedBy)
		}
	}
}

// The database holds a note to its resolution: a note written to a hold
// that stays open, or to one already resolved, is refused, so no note is
// added to a decision afterwards. The control is the write the accessors
// make, a note in the statement that resolves an open hold, which passes;
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
	if h, err := s.Hold(ctx, "control"); err != nil || h.ResolutionNote.Or("") != "with the answer" {
		t.Fatalf("the answered hold reads %+v (%v), want the note it was answered with", h, err)
	}
}

// The sealing migration is additive: taken back to the shape without it and
// reapplied over the rows, a hold answered with a note before it keeps that
// note, one answered without reads back none, and the trigger refuses
// again. The migration is found by name, so the test does not spell out its
// number.
func TestTheHoldNoteSealKeepsEarlierHolds(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	var seal migration
	for _, m := range schema {
		if m.name == sealHoldNoteMigration {
			seal = m
		}
	}
	if seal.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", sealHoldNoteMigration)
	}
	for _, statement := range []string{
		`DROP TRIGGER hold_note_is_written_with_its_resolution`,
		`INSERT INTO hold (key, subject, detail, opened_at, resolution, resolved_at, resolved_by, resolution_note)
			VALUES ('noted', 'a decision', '', '2026-01-01T00:00:00.000000000Z', 'approved', '2026-01-01T00:00:00.000000000Z', 'machine-interface', 'kept')`,
		`INSERT INTO hold (key, subject, detail, opened_at, resolution, resolved_at, resolved_by)
			VALUES ('bare', 'a decision', '', '2026-01-01T00:00:00.000000000Z', 'approved', '2026-01-01T00:00:00.000000000Z', 'machine-interface')`,
	} {
		if _, err := s.write.ExecContext(ctx, statement); err != nil {
			t.Fatalf("rolling the schema back: %v", err)
		}
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM schema_migration WHERE version = ?`, seal.version); err != nil {
		t.Fatalf("rolling the schema back: %v", err)
	}
	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("applying the shipped migration over the old rows: %v", err)
	}
	if h, err := s.Hold(ctx, "noted"); err != nil || h.ResolutionNote.Or("") != "kept" {
		t.Fatalf("the earlier noted hold reads back %+v (%v), want its note kept", h, err)
	}
	if h, err := s.Hold(ctx, "bare"); err != nil || h.Resolution.Or("") != "approved" || h.ResolutionNote.IsKnown() {
		t.Fatalf("the earlier bare hold reads back %+v (%v), want it answered with no note", h, err)
	}
	if err := rawWrite(ctx, s, `UPDATE hold SET resolution_note = 'late' WHERE key = 'bare'`); err == nil {
		t.Fatal("the reapplied migration did not refuse a note written after the answer")
	}
}
