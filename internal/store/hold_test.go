package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/redact"
)

func TestRegisterHoldIsIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	h := Hold{Key: "run-1/ask/finding-3", RunID: Known(run.ID),
		Subject: "a finding nobody classified", Detail: "the reviewer omitted an action"}

	first, err := s.RegisterHold(ctx, h)
	if err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if !first.Open() {
		t.Fatal("a newly registered hold is not open")
	}
	if first.Resolution.IsKnown() || first.ResolvedAt.IsKnown() || first.ResolvedBy.IsKnown() {
		t.Fatalf("a newly registered hold carries a resolution: %+v", first)
	}

	// Registering the same decision again is the same hold, not a second one,
	// and it does not disturb what is already there.
	h.Detail = "a different description of the same decision"
	second, err := s.RegisterHold(ctx, h)
	if err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if second.Detail != first.Detail || !second.OpenedAt.Equal(first.OpenedAt) {
		t.Fatalf("re-registering changed the hold: %+v then %+v", first, second)
	}
	open, err := s.OpenHolds(ctx)
	if err != nil {
		t.Fatalf("OpenHolds: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("registering the same key twice made %d holds", len(open))
	}
}

// A hold is closed only by an explicit resolution.
func TestResolveHoldRequiresAnExplicitResolution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedRun(t, s)

	if _, err := s.RegisterHold(ctx, Hold{Key: "k", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	for _, empty := range []string{"", "   ", "\t\n"} {
		if _, err := s.ResolveHold(ctx, "k", empty, ResolvedByPerson()); !errors.Is(err, ErrNoResolution) {
			t.Fatalf("ResolveHold accepted %q as a resolution: %v", empty, err)
		}
	}
	held, err := s.Hold(ctx, "k")
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if !held.Open() {
		t.Fatal("a refused resolution closed the hold anyway")
	}

	resolved, err := s.ResolveHold(ctx, "k", "ship it", ResolvedByPerson())
	if err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	if resolved.Open() {
		t.Fatal("an explicit resolution did not close the hold")
	}
	if answer, known := resolved.Resolution.Get(); !known || answer != "ship it" {
		t.Fatalf("the resolution came back as %q (known=%v)", answer, known)
	}
	if !resolved.ResolvedAt.IsKnown() {
		t.Fatal("a resolved hold has no resolution time")
	}
	open, err := s.OpenHolds(ctx)
	if err != nil {
		t.Fatalf("OpenHolds: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("a resolved hold is still listed as open: %+v", open)
	}
}

// ResolveHoldNoted records free text beside the resolution; ResolveHold
// leaves ResolutionNote unknown. Who decided stays ResolvedBy either way.
func TestResolveHoldNotedRecordsTheNote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.RegisterHold(ctx, Hold{Key: "noted", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	resolved, err := s.ResolveHoldNoted(ctx, "noted", "approved", "the floor is not decided", ResolvedByMachineInterface())
	if err != nil {
		t.Fatalf("ResolveHoldNoted: %v", err)
	}
	if note, ok := resolved.ResolutionNote.Get(); !ok || note != "the floor is not decided" {
		t.Fatalf("ResolutionNote = %+v, want the note", resolved.ResolutionNote)
	}
	if by, _ := resolved.ResolvedBy.Get(); by != ResolvedByMachineInterface() {
		t.Fatalf("ResolvedBy = %v, want the machine interface", by)
	}

	if _, err := s.RegisterHold(ctx, Hold{Key: "plain", Subject: "another"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	plain, err := s.ResolveHold(ctx, "plain", "approved", ResolvedByPerson())
	if err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	if plain.ResolutionNote.IsKnown() {
		t.Fatalf("ResolveHold left ResolutionNote %+v, want unknown", plain.ResolutionNote)
	}
}

// A hold answer's note passes through the redactor before it is stored, on
// every path that writes resolution_note. The test uses the same URL userinfo
// and bearer shapes message_test uses for message.text.
func TestAHoldResolutionNoteIsStoredRedacted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.RegisterHold(ctx, Hold{Key: "noted-url", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	urlNote := "push to https://user:secret@example.com/x.git when done"
	resolved, err := s.ResolveHoldNoted(ctx, "noted-url", "approved", urlNote, ResolvedByPerson())
	if err != nil {
		t.Fatalf("ResolveHoldNoted: %v", err)
	}
	note, ok := resolved.ResolutionNote.Get()
	if !ok {
		t.Fatal("ResolutionNote is unknown")
	}
	if strings.Contains(note, "secret") {
		t.Fatalf("the stored note still carries the credential: %q", note)
	}
	if !strings.Contains(note, "REDACTED@") {
		t.Fatalf("the stored note is %q, want the userinfo redacted", note)
	}
	stored, err := s.Hold(ctx, "noted-url")
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if got, _ := stored.ResolutionNote.Get(); got != note {
		t.Fatalf("Hold returned note %q, want %q", got, note)
	}

	bearerStore := openStore(t, WithRedactor(redact.New()))
	if _, err := bearerStore.RegisterHold(ctx, Hold{Key: "noted-bearer", Subject: "another"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	bearerNote := "the token is Bearer abcdefgh123456"
	bearerResolved, err := bearerStore.ResolveHoldNoted(ctx, "noted-bearer", "retry", bearerNote, ResolvedByPerson())
	if err != nil {
		t.Fatalf("ResolveHoldNoted bearer: %v", err)
	}
	bearerStored, ok := bearerResolved.ResolutionNote.Get()
	if !ok {
		t.Fatal("bearer ResolutionNote is unknown")
	}
	if strings.Contains(bearerStored, "abcdefgh123456") {
		t.Fatalf("the stored note still carries the bearer token: %q", bearerStored)
	}
	if !strings.Contains(bearerStored, redact.Marker) {
		t.Fatalf("the stored note is %q, want the bearer redacted", bearerStored)
	}

	run := seedRun(t, s)
	closing := HoldClosure{
		Hold:       Hold{Key: "run/" + run.ID + "/closure", RunID: Known(run.ID), Subject: "a decision"},
		Resolution: "approved",
		Note:       urlNote,
		By:         ResolvedByStandingConsent(),
	}
	recorded, err := s.RecordHoldResolution(ctx, closing)
	if err != nil {
		t.Fatalf("RecordHoldResolution: %v", err)
	}
	closureNote, ok := recorded.ResolutionNote.Get()
	if !ok || strings.Contains(closureNote, "secret") {
		t.Fatalf("RecordHoldResolution stored %+v, want the note redacted", recorded.ResolutionNote)
	}
}

// A bare API key pasted into a hold answer's note reaches the stored row with
// the value cut, through the redactor the product wires in. The value is
// built at run time in the shape of an Anthropic key (internal/redact's
// tokenShapes row), so no literal here is a credential. The note as written
// is the control: it carries the value, so a store that wrote it unredacted
// would fail on the stored row and not on the input.
func TestABareTokenInAHoldResolutionNoteIsStoredRedacted(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, WithRedactor(redact.New()))
	if _, err := s.RegisterHold(ctx, Hold{Key: "noted-key", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	value := "sk-ant-api03-" + strings.Repeat("Ab3_x-Yz9Q", 9)
	note := "use " + value + " for the retry"
	if !strings.Contains(note, value) {
		t.Fatalf("the note %q does not carry the value, so the test checks nothing", note)
	}
	if _, err := s.ResolveHoldNoted(ctx, "noted-key", "retry", note, ResolvedByPerson()); err != nil {
		t.Fatalf("ResolveHoldNoted: %v", err)
	}
	stored, err := s.Hold(ctx, "noted-key")
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	got, ok := stored.ResolutionNote.Get()
	if !ok {
		t.Fatal("ResolutionNote is unknown")
	}
	if want := "use sk-ant-" + redact.Marker + " for the retry"; got != want {
		t.Fatalf("the stored note is %q, want %q", got, want)
	}
}

// holdResolutionSealVersion is the shipped migration that seals a hold's
// resolution. A shipped version never moves, so pinning to it holds the test
// below to its subject as the list grows.
const holdResolutionSealVersion = 42

// Notes written before every path redacted can still carry credentials until
// the seal migration runs. Opening redacts them in the same transaction that
// installs the trigger.
func TestTheHoldResolutionSealMigrationRedactsExistingNotes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	credNote := "see https://user:secret@example.test/repo.git for context"

	older, err := openPool(ctx, poolDSN(path, true), true)
	if err != nil {
		t.Fatalf("open the older database: %v", err)
	}
	if err := migrate(ctx, older, schemaBefore(t, holdResolutionSealVersion)); err != nil {
		t.Fatalf("migrate to the older schema: %v", err)
	}
	opened, resolved := encodeTime(nowUTC()), encodeTime(nowUTC())
	if _, err := older.ExecContext(ctx, `
		INSERT INTO hold (key, run_id, task_id, subject, detail, opened_at, resolution, resolved_at, resolution_note)
		VALUES (?, NULL, NULL, ?, ?, ?, ?, ?, ?)`,
		"older", "a decision", "", opened, "approved", resolved, credNote); err != nil {
		t.Fatalf("write a resolved hold with an unredacted note: %v", err)
	}
	if err := older.Close(); err != nil {
		t.Fatalf("close the older database: %v", err)
	}

	s := openStoreAt(t, path)
	held, err := s.Hold(ctx, "older")
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	note, ok := held.ResolutionNote.Get()
	if !ok {
		t.Fatal("the note did not survive the migration")
	}
	if strings.Contains(note, "secret") {
		t.Fatalf("the note is still %q, want credentials removed", note)
	}
	if !strings.Contains(note, "REDACTED") {
		t.Fatalf("the note is %q, want the userinfo redacted", note)
	}
}

// The seal holds at the database: a resolved hold's note is not rewritten.
func TestAHoldResolutionIsSealed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.RegisterHold(ctx, Hold{Key: "sealed", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if _, err := s.ResolveHoldNoted(ctx, "sealed", "approved", "the floor is not decided", ResolvedByPerson()); err != nil {
		t.Fatalf("ResolveHoldNoted: %v", err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE hold SET resolution_note = 'rewritten' WHERE key = ?`, "sealed"); err == nil {
		t.Fatal("the database let a hold's resolution note be rewritten")
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE hold SET resolution = 'changed' WHERE key = ?`, "sealed"); err == nil {
		t.Fatal("the database let a hold's resolution be rewritten")
	}
}

func TestResolveHoldRefusesASecondAnswer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.RegisterHold(ctx, Hold{Key: "k", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if _, err := s.ResolveHold(ctx, "k", "ship it", ResolvedByPerson()); err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	_, err := s.ResolveHold(ctx, "k", "actually, do not", ResolvedByMachineInterface())
	if err == nil {
		t.Fatal("ResolveHold overwrote a resolution that was already there")
	}
	if !errors.Is(err, ErrHoldResolved) {
		t.Fatalf("the refusal is not one a caller can branch on: %v", err)
	}
	held, err := s.Hold(ctx, "k")
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if answer, _ := held.Resolution.Get(); answer != "ship it" {
		t.Fatalf("the first resolution was replaced by %q", answer)
	}

	if _, err := s.ResolveHold(ctx, "no-such-hold", "anything", ResolvedByPerson()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveHold on a missing hold: %v", err)
	}
}

// PRD section 8: a hold is never closed by the surrounding work finishing.
func TestFinishingTheWorkDoesNotCloseAHold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	task := seedTask(t, s, "taskaaaaaaab")

	for _, key := range []string{"on-the-run", "on-the-task"} {
		h := Hold{Key: key, Subject: "a decision"}
		if key == "on-the-run" {
			h.RunID = Known(run.ID)
		} else {
			h.TaskID = Known(task.ID)
		}
		if _, err := s.RegisterHold(ctx, h); err != nil {
			t.Fatalf("RegisterHold %s: %v", key, err)
		}
	}

	// Everything the surrounding work can do short of an explicit resolution.
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunPending}, RunTerminated); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}
	if _, err := s.AppendTaskEvent(ctx, task.ID, "finished", "worker exited"); err != nil {
		t.Fatalf("AppendTaskEvent: %v", err)
	}

	open, err := s.OpenHolds(ctx)
	if err != nil {
		t.Fatalf("OpenHolds: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("%d holds are still open after the work finished, want 2", len(open))
	}
}

func TestRegisterHoldRefusesIncompleteRecords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.RegisterHold(ctx, Hold{Subject: "a decision"}); err == nil {
		t.Fatal("RegisterHold accepted a hold with no key")
	}
	if _, err := s.RegisterHold(ctx, Hold{Key: "k"}); err == nil {
		t.Fatal("RegisterHold accepted a hold with no subject")
	}
	if _, err := s.Hold(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused RegisterHold left a row behind: %v", err)
	}
}
