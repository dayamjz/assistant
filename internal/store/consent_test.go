package store

import (
	"context"
	"errors"
	"testing"
)

// Standing consent is given once and kept: the first give records when, a
// second give returns the run with that instant unchanged, a listing and a
// single read say the same, and a run that does not exist is ErrNotFound.
func TestStandingConsentIsGivenOnceAndReadBackEverywhere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if run.StandingConsent.IsKnown() {
		t.Fatalf("a new run carries standing consent %v", run.StandingConsent)
	}
	given, err := s.GiveRunStandingConsent(ctx, run.ID)
	if err != nil {
		t.Fatalf("GiveRunStandingConsent: %v", err)
	}
	at, known := given.StandingConsent.Get()
	if !known || at.IsZero() {
		t.Fatalf("the give returned %v, want a known instant", given.StandingConsent)
	}
	again, err := s.GiveRunStandingConsent(ctx, run.ID)
	if err != nil {
		t.Fatalf("a second give: %v", err)
	}
	if second, _ := again.StandingConsent.Get(); !second.Equal(at) {
		t.Fatalf("a second give moved the instant from %v to %v", at, second)
	}
	listed, err := s.RunsForRepository(ctx, run.RepositoryID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("RunsForRepository = %v, %v", listed, err)
	}
	if read, _ := listed[0].StandingConsent.Get(); !read.Equal(at) {
		t.Fatalf("the listing reads %v, want %v", listed[0].StandingConsent, at)
	}
	if _, err := s.GiveRunStandingConsent(ctx, "no-such-run"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("giving consent to a run that does not exist: %v, want ErrNotFound", err)
	}
}

// The record of consent cannot be withdrawn or revised by any statement, so a
// run whose decisions the program answered keeps saying so. The control is an
// update that does not name the column, which is accepted.
func TestStandingConsentCannotBeWithdrawnOrRevised(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	given, err := s.GiveRunStandingConsent(ctx, run.ID)
	if err != nil {
		t.Fatalf("GiveRunStandingConsent: %v", err)
	}
	for name, statement := range map[string]string{
		"withdrawing consent": `UPDATE run SET standing_consent = NULL WHERE id = ?`,
		"revising consent":    `UPDATE run SET standing_consent = '2001-01-01T00:00:00Z' WHERE id = ?`,
	} {
		if err := rawWrite(ctx, s, statement, run.ID); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := rawWrite(ctx, s, `UPDATE run SET intent = 'still validate' WHERE id = ?`, run.ID); err != nil {
		t.Fatalf("an update that does not name the column was refused: %v", err)
	}
	read, err := s.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if read.StandingConsent != given.StandingConsent {
		t.Fatalf("consent reads back %v, want %v", read.StandingConsent, given.StandingConsent)
	}
}

// The migration is additive: taken back to the shape without it, the shipped
// migration applied over the rows leaves every run as it stood with no
// consent, and the trigger refuses again. It is found by name, so the test
// spells no version the shipped list may grow past.
func TestTheStandingConsentMigrationKeepsEarlierRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	var added migration
	for _, m := range schema {
		if m.name == runStandingConsentMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", runStandingConsentMigration)
	}
	run := seedRun(t, s)
	for _, statement := range []string{
		`DROP TRIGGER run_standing_consent_is_given_once`,
		`ALTER TABLE run DROP COLUMN standing_consent`,
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
	if read.StandingConsent.IsKnown() || read.Status != run.Status || read.Branch != run.Branch {
		t.Fatalf("the earlier run reads back %+v, want it as it stood with no consent", read)
	}
	if _, err := s.GiveRunStandingConsent(ctx, run.ID); err != nil {
		t.Fatalf("giving consent after the migration: %v", err)
	}
	if err := rawWrite(ctx, s, `UPDATE run SET standing_consent = NULL WHERE id = ?`, run.ID); err == nil {
		t.Fatal("the reapplied trigger let consent be withdrawn")
	}
}

// A decision the recording caller answered itself is registered and resolved
// together, with who answered it, and recording the same key again is the one
// record rather than a refusal. A closure missing a subject, a resolution or a
// resolver is refused before anything is written.
func TestRecordHoldResolutionRegistersAndResolvesTogether(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	closing := HoldClosure{
		Hold:       Hold{Key: "run/" + run.ID + "/decision/3", RunID: Known(run.ID), Subject: "a decision", Detail: "why"},
		Resolution: "fix: a, b",
		By:         ResolvedByStandingConsent(),
	}
	recorded, err := s.RecordHoldResolution(ctx, closing)
	if err != nil {
		t.Fatalf("RecordHoldResolution: %v", err)
	}
	if recorded.Open() {
		t.Fatal("the recorded hold is open")
	}
	if by, _ := recorded.ResolvedBy.Get(); by != ResolvedByStandingConsent() {
		t.Fatalf("the hold records %s as its resolver", by)
	}
	if resolution, _ := recorded.Resolution.Get(); resolution != closing.Resolution {
		t.Fatalf("the hold records %q", resolution)
	}
	again := closing
	again.Resolution = "approved"
	second, err := s.RecordHoldResolution(ctx, again)
	if err != nil {
		t.Fatalf("recording the same decision again: %v", err)
	}
	if resolution, _ := second.Resolution.Get(); resolution != closing.Resolution {
		t.Fatalf("recording the same decision again rewrote it to %q", resolution)
	}
	for name, bad := range map[string]HoldClosure{
		"no key":        {Hold: Hold{Subject: "s"}, Resolution: "x", By: ResolvedByStandingConsent()},
		"no subject":    {Hold: Hold{Key: "k1"}, Resolution: "x", By: ResolvedByStandingConsent()},
		"no resolution": {Hold: Hold{Key: "k2", Subject: "s"}, Resolution: " ", By: ResolvedByStandingConsent()},
		"nobody":        {Hold: Hold{Key: "k3", Subject: "s"}, Resolution: "x"},
	} {
		if _, err := s.RecordHoldResolution(ctx, bad); err == nil {
			t.Errorf("a closure with %s was recorded", name)
		}
		if bad.Hold.Key != "" {
			if _, err := s.Hold(ctx, bad.Hold.Key); !errors.Is(err, ErrNotFound) {
				t.Errorf("a refused closure with %s left a hold behind: %v", name, err)
			}
		}
	}
}
