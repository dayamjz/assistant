package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

// Every member of the closed set records, and reads back as the member that was
// written. The person value is one of them, which is the half that keeps the
// absence tests elsewhere from passing on a build where nothing can record a
// person at all.
func TestEveryResolverRecordsAndReadsBack(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	for _, by := range resolvers() {
		key := "hold/" + by.String()
		if _, err := s.RegisterHold(ctx, Hold{Key: key, Subject: "a decision"}); err != nil {
			t.Fatalf("RegisterHold %s: %v", key, err)
		}
		resolved, err := s.ResolveHold(ctx, key, "ship it", by)
		if err != nil {
			t.Fatalf("ResolveHold %s: %v", key, err)
		}
		got, known := resolved.ResolvedBy.Get()
		if !known {
			t.Fatalf("hold %s was resolved with nobody recorded", key)
		}
		if got != by {
			t.Fatalf("hold %s recorded %s, want %s", key, got, by)
		}
		// The record is durable rather than a value the write echoed back.
		reread, err := s.Hold(ctx, key)
		if err != nil {
			t.Fatalf("Hold %s: %v", key, err)
		}
		if again, _ := reread.ResolvedBy.Get(); again != by {
			t.Fatalf("hold %s reads back as %s, want %s", key, again, by)
		}
	}
}

// PRD section 8: a resolution records who made it, so one that names nobody is
// refused rather than stored with an empty who.
func TestResolveHoldRefusesAResolutionThatNamesNobody(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.RegisterHold(ctx, Hold{Key: "k", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}

	// The zero Resolver is what a caller that never thought about the question
	// holds, and it is the only value of this type a caller outside this
	// package can produce without naming one of the three.
	_, err := s.ResolveHold(ctx, "k", "ship it", Resolver{})
	if !errors.Is(err, ErrNoResolver) {
		t.Fatalf("ResolveHold with nobody recorded: %v, want ErrNoResolver", err)
	}
	held, err := s.Hold(ctx, "k")
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if !held.Open() {
		t.Fatal("a refused resolution closed the hold anyway")
	}
	if held.ResolvedBy.IsKnown() {
		t.Fatalf("a refused resolution recorded a resolver: %s", held.ResolvedBy)
	}
}

// This records and does not gate. What a resolution would say about who made it
// changes nothing about whether it may be made: the same answer on the same
// kind of hold succeeds identically for every member of the set, and the only
// difference afterwards is the record.
func TestWhoResolvedDoesNotDecideWhetherAResolutionMayProceed(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	for _, by := range resolvers() {
		key := "gate/" + by.String()
		if _, err := s.RegisterHold(ctx, Hold{Key: key, Subject: "approve the run", Detail: "same decision, different who"}); err != nil {
			t.Fatalf("RegisterHold %s: %v", key, err)
		}
		resolved, err := s.ResolveHold(ctx, key, "approve", by)
		if err != nil {
			t.Fatalf("resolving as %s was refused: %v", by, err)
		}
		if resolved.Open() {
			t.Fatalf("resolving as %s left the hold open", by)
		}
		if answer, _ := resolved.Resolution.Get(); answer != "approve" {
			t.Fatalf("resolving as %s stored the answer as %q", by, answer)
		}
		// A second answer is refused for every member, for being second rather
		// than for who is asking.
		if _, err := s.ResolveHold(ctx, key, "approve", by); !errors.Is(err, ErrHoldResolved) {
			t.Fatalf("a second answer as %s: %v, want ErrHoldResolved", by, err)
		}
	}
}

// A Resolver is not something text can become. This is the property PRD
// section 8 rests on: an agent reaches this program with bytes, and no decoding
// of bytes anywhere produces the value meaning a person decided.
func TestAResolverIsNotSomethingDecodingProduces(t *testing.T) {
	person := ResolvedByPerson()

	// A bare value, whatever a caller writes on the wire. The decode goes
	// through decodeInto because staticcheck's SA9005 reports the direct call
	// as one that cannot populate anything, which is the property under test
	// stated as a diagnostic; the assertion below is that property checked
	// against a value rather than taken from a linter.
	for _, encoded := range []string{`"person"`, `1`, `{"name":"person"}`, `{"Name":"person"}`, `["person"]`} {
		var r Resolver
		_ = decodeInto(&r, encoded)
		if r == person {
			t.Fatalf("decoding %s produced the person resolver", encoded)
		}
	}

	// A field of a shape a handler might decode a request body into, including
	// the optional wrapper a hold carries it in.
	var body struct {
		By       Resolver           `json:"by"`
		Optional Optional[Resolver] `json:"optional"`
	}
	for _, encoded := range []string{
		`{"by":"person","optional":"person"}`,
		`{"by":{"name":"person"},"optional":{"value":{"name":"person"},"known":true}}`,
	} {
		_ = json.Unmarshal([]byte(encoded), &body)
		if body.By == person {
			t.Fatalf("decoding %s produced the person resolver", encoded)
		}
		if got, known := body.Optional.Get(); known && got == person {
			t.Fatalf("decoding %s produced the person resolver", encoded)
		}
	}

	// The whole record a caller could hand back, so the field cannot be filled
	// by round-tripping a hold through the wire either.
	var hold Hold
	_ = json.Unmarshal([]byte(`{"Key":"k","ResolvedBy":"person"}`), &hold)
	if got, known := hold.ResolvedBy.Get(); known && got == person {
		t.Fatal("decoding a hold produced the person resolver")
	}
}

// A hold resolved before the column existed reads back with nobody recorded,
// which is a different fact from a resolution nobody was recorded for: this
// package refuses to write one of those.
func TestAHoldResolvedBeforeTheColumnExistedReadsBackUnknown(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	// The insert names the columns the build before migration 3 wrote, so what
	// lands in the table is the row that build's ResolveHold left rather than a
	// shape invented here.
	if err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO hold (key, run_id, task_id, subject, detail, opened_at, resolution, resolved_at)
			VALUES (?, NULL, NULL, ?, ?, ?, ?, ?)`,
			"older", "a decision made before the column existed", "", encodeTime(nowUTC()), "ship it", encodeTime(nowUTC()))
		return err
	}); err != nil {
		t.Fatalf("writing a pre-migration row: %v", err)
	}

	held, err := s.Hold(ctx, "older")
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if held.Open() {
		t.Fatal("a resolved hold read back as open")
	}
	if held.ResolvedBy.IsKnown() {
		t.Fatalf("a hold resolved before the column existed names %s", held.ResolvedBy)
	}
	if held.ResolvedBy.String() != "unknown" {
		t.Fatalf("an unrecorded resolver rendered as %q", held.ResolvedBy.String())
	}
}

// A column holding something outside the closed set is a row this package's
// accessors did not write, which is what a hand-edited or foreign-written
// database looks like. Reading it is an error rather than an unknown, because
// reporting it as "nobody was recorded" would turn a row nobody can account for
// into a fact about the resolution.
func TestAResolverOutsideTheClosedSetIsRefusedOnRead(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.RegisterHold(ctx, Hold{Key: "k", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if _, err := s.ResolveHold(ctx, "k", "ship it", ResolvedByProgram()); err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE hold SET resolved_by = ? WHERE key = ?`, "a committee", "k"); err != nil {
		t.Fatalf("writing a resolver outside the set: %v", err)
	}

	if _, err := s.Hold(ctx, "k"); !errors.Is(err, ErrUnknownResolver) {
		t.Fatalf("Hold on a row outside the set: %v, want ErrUnknownResolver", err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE hold SET resolution = NULL, resolved_at = NULL WHERE key = ?`, "k"); err != nil {
		t.Fatalf("reopening the hold: %v", err)
	}
	if _, err := s.OpenHolds(ctx); !errors.Is(err, ErrUnknownResolver) {
		t.Fatalf("OpenHolds over a row outside the set: %v, want ErrUnknownResolver", err)
	}
}

// The zero Resolver renders as a who rather than as an empty string, so a
// formatted record cannot print nothing where a name belongs.
func TestResolverRenders(t *testing.T) {
	if got := (Resolver{}).String(); got != "nobody" {
		t.Errorf("the zero Resolver rendered as %q", got)
	}
	for _, by := range resolvers() {
		if by.String() == "" {
			t.Errorf("%#v rendered as an empty string", by)
		}
	}
	if ResolvedByPerson().String() == ResolvedByMachineInterface().String() {
		t.Error("two members of the set render identically, so a record cannot tell them apart")
	}
}

// decodeInto is json.Unmarshal with the destination held as any, which is what
// a decoder reached through an interface does.
func decodeInto(target any, encoded string) error {
	return json.Unmarshal([]byte(encoded), target)
}
