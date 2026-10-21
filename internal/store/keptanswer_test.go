package store

import (
	"context"
	"errors"
	"testing"
)

// An entry's key round-trips through ParseKeptWorkKey and nothing else
// parses: a hold's key, a finding id, a sign, a zero and an empty number are
// all refused.
func TestAKeptWorkKeyIsParsedOnlyAsKeyWritesIt(t *testing.T) {
	t.Parallel()
	if id, ok := ParseKeptWorkKey(KeptWork{ID: 42}.Key()); !ok || id != 42 {
		t.Fatalf("the key round-trips as %d, %v; want 42", id, ok)
	}
	for _, key := range []string{"", "kept-work/", "kept-work/0", "kept-work/-3", "kept-work/+3", "kept-work/3a",
		"task/abc/finding/3", "3", "kept-work/3/4"} {
		if id, ok := ParseKeptWorkKey(key); ok {
			t.Errorf("%q parsed as entry %d", key, id)
		}
	}
}

// A land answer's run is recorded once, only on an entry answered land and
// only after the landing was claimed, which is itself claimed once: an open
// entry and one answered otherwise are refused, a run before the claim is
// refused, the first run stands against a second, the same run again is
// accepted and writes nothing, the owed listing drops the entry once its
// run is named, and the schema's seals refuse a direct rewrite of either.
func TestALandingRunIsRecordedOnceOnAnEntryAnsweredLand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	land, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/a", Holds: []string{"aaaa"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/b", Holds: []string{"bbbb"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordKeptWorkLanding(ctx, land.ID, run.ID); !errors.Is(err, ErrKeptWorkNotLand) {
		t.Fatalf("a landing on an open entry = %v; want ErrKeptWorkNotLand", err)
	}
	if _, err := s.AnswerKeptWorkDiscard(ctx, other.ID, "shown", ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordKeptWorkLanding(ctx, other.ID, run.ID); !errors.Is(err, ErrKeptWorkNotLand) {
		t.Fatalf("a landing on an entry answered discard = %v; want ErrKeptWorkNotLand", err)
	}
	if _, err := s.AnswerKeptWork(ctx, land.ID, KeptLand, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if owed, err := s.KeptWorkLandingsOwed(ctx); err != nil || len(owed) != 1 || owed[0].ID != land.ID {
		t.Fatalf("KeptWorkLandingsOwed = %+v, %v; want the land entry", owed, err)
	}
	if _, err := s.RecordKeptWorkLanding(ctx, land.ID, "run-landing"); !errors.Is(err, ErrKeptWorkLandingUnclaimed) {
		t.Fatalf("a landing recorded before its claim = %v; want ErrKeptWorkLandingUnclaimed", err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, other.ID); !errors.Is(err, ErrKeptWorkNotLand) {
		t.Fatalf("a claim on an entry answered discard = %v; want ErrKeptWorkNotLand", err)
	}
	claimed, err := s.ClaimKeptWorkLanding(ctx, land.ID)
	if err != nil || !claimed.LandingClaimedAt.IsKnown() || claimed.LandedRun.IsKnown() {
		t.Fatalf("claiming the landing = %+v, %v", claimed, err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, land.ID); !errors.Is(err, ErrKeptWorkLandingClaimed) {
		t.Fatalf("a second claim = %v; want ErrKeptWorkLandingClaimed", err)
	}
	if owed, err := s.KeptWorkLandingsOwed(ctx); err != nil || len(owed) != 1 {
		t.Fatalf("KeptWorkLandingsOwed after the claim = %+v, %v; want the entry still owed", owed, err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET landing_claimed_at = '2020-01-01T00:00:00Z' WHERE id = ?`, land.ID); err == nil {
		t.Fatal("the schema let the landing claim be rewritten")
	}
	got, err := s.RecordKeptWorkLanding(ctx, land.ID, "run-landing")
	if err != nil || got.LandedRun.Or("") != "run-landing" {
		t.Fatalf("recording the landing = %+v, %v", got, err)
	}
	if _, err := s.RecordKeptWorkLanding(ctx, land.ID, "run-landing"); err != nil {
		t.Fatalf("the same landing again = %v; want it accepted", err)
	}
	if _, err := s.RecordKeptWorkLanding(ctx, land.ID, "run-second"); !errors.Is(err, ErrKeptWorkLanded) {
		t.Fatalf("a second landing run = %v; want ErrKeptWorkLanded", err)
	}
	if owed, err := s.KeptWorkLandingsOwed(ctx); err != nil || len(owed) != 0 {
		t.Fatalf("KeptWorkLandingsOwed = %+v, %v; want none once the run is named", owed, err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET landed_run = 'run-other' WHERE id = ?`, land.ID); err == nil {
		t.Fatal("the schema let a landing run be rewritten")
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET landed_run = 'run-other' WHERE id = ?`, other.ID); err == nil {
		t.Fatal("the schema let a landing run be written on an entry answered discard")
	}
}

// A carry-on answer names the task carrying the entry on in the same write,
// refusing an identifier the home would refuse and the generic answer
// without one; the task comes to own the copy once, only for that entry,
// and the owed listing drops the entry when it does; and the schema seals
// both against a direct rewrite.
func TestACarryOnAnswerNamesItsTaskOnceAndIsCarriedOutOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/c", Holds: []string{"cccc"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerKeptWork(ctx, entry.ID, KeptCarryOn, ResolvedByMachineInterface()); err == nil {
		t.Fatal("a carry-on answer naming no task was recorded")
	}
	if _, err := s.AnswerKeptWorkCarryOn(ctx, entry.ID, "not-a-task", ResolvedByMachineInterface()); err == nil {
		t.Fatal("a carry-on answer naming an invalid task was recorded")
	}
	if _, err := s.MarkKeptWorkCarriedOn(ctx, entry.ID); !errors.Is(err, ErrKeptWorkNotCarryOn) {
		t.Fatalf("marking an open entry carried on = %v; want ErrKeptWorkNotCarryOn", err)
	}
	answered, err := s.AnswerKeptWorkCarryOn(ctx, entry.ID, "taskcarrying", ResolvedByMachineInterface())
	if err != nil || answered.Answer.Or("") != KeptCarryOn || answered.CarriedOnTask.Or("") != "taskcarrying" || answered.CarriedOnAt.IsKnown() {
		t.Fatalf("the carry-on answer = %+v, %v; want carry-on naming taskcarrying, not yet carried on", answered, err)
	}
	if owed, err := s.KeptWorkCarryOnsOwed(ctx); err != nil || len(owed) != 1 || owed[0].ID != entry.ID {
		t.Fatalf("KeptWorkCarryOnsOwed = %+v, %v; want the entry", owed, err)
	}
	done, err := s.MarkKeptWorkCarriedOn(ctx, entry.ID)
	if err != nil || !done.CarriedOnAt.IsKnown() {
		t.Fatalf("marking the entry carried on = %+v, %v", done, err)
	}
	if _, err := s.MarkKeptWorkCarriedOn(ctx, entry.ID); !errors.Is(err, ErrKeptWorkCarriedOn) {
		t.Fatalf("marking it again = %v; want ErrKeptWorkCarriedOn", err)
	}
	if owed, err := s.KeptWorkCarryOnsOwed(ctx); err != nil || len(owed) != 0 {
		t.Fatalf("KeptWorkCarryOnsOwed = %+v, %v; want none once carried on", owed, err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET carried_on_task = 'taskother000' WHERE id = ?`, entry.ID); err == nil {
		t.Fatal("the schema let the carry-on task be rewritten")
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET carried_on_at = '2020-01-01T00:00:00Z' WHERE id = ?`, entry.ID); err == nil {
		t.Fatal("the schema let the carried-on instant be rewritten")
	}
	land, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/d", Holds: []string{"dddd"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerKeptWork(ctx, land.ID, KeptLand, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET carried_on_task = 'taskother000' WHERE id = ?`, land.ID); err == nil {
		t.Fatal("the schema let a carry-on task be written on an entry answered land")
	}
}

// A discard answer records what it was shown in the same write, refusing an
// empty digest and the generic answer; the copy's going is marked once,
// only on an entry answered discard; the owed listing drops the entry when
// it is; and the schema seals both against a direct rewrite.
func TestADiscardAnswerRecordsWhatItWasShownOnceAndIsCarriedOutOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/e", Holds: []string{"eeee"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerKeptWork(ctx, entry.ID, KeptDiscard, ResolvedByMachineInterface()); err == nil {
		t.Fatal("a discard answer shown nothing was recorded")
	}
	if _, err := s.AnswerKeptWorkDiscard(ctx, entry.ID, " ", ResolvedByMachineInterface()); err == nil {
		t.Fatal("a discard answer with an empty digest was recorded")
	}
	if _, err := s.MarkKeptWorkDiscarded(ctx, entry.ID); !errors.Is(err, ErrKeptWorkNotDiscard) {
		t.Fatalf("marking an open entry discarded = %v; want ErrKeptWorkNotDiscard", err)
	}
	answered, err := s.AnswerKeptWorkDiscard(ctx, entry.ID, "abc123", ResolvedByMachineInterface())
	if err != nil || answered.Answer.Or("") != KeptDiscard || answered.DiscardDigest.Or("") != "abc123" || answered.DiscardedAt.IsKnown() {
		t.Fatalf("the discard answer = %+v, %v; want discard shown abc123, not yet discarded", answered, err)
	}
	if owed, err := s.KeptWorkDiscardsOwed(ctx); err != nil || len(owed) != 1 || owed[0].ID != entry.ID {
		t.Fatalf("KeptWorkDiscardsOwed = %+v, %v; want the entry", owed, err)
	}
	done, err := s.MarkKeptWorkDiscarded(ctx, entry.ID)
	if err != nil || !done.DiscardedAt.IsKnown() {
		t.Fatalf("marking the entry discarded = %+v, %v", done, err)
	}
	if _, err := s.MarkKeptWorkDiscarded(ctx, entry.ID); !errors.Is(err, ErrKeptWorkDiscarded) {
		t.Fatalf("marking it again = %v; want ErrKeptWorkDiscarded", err)
	}
	if owed, err := s.KeptWorkDiscardsOwed(ctx); err != nil || len(owed) != 0 {
		t.Fatalf("KeptWorkDiscardsOwed = %+v, %v; want none once discarded", owed, err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET discard_digest = 'other' WHERE id = ?`, entry.ID); err == nil {
		t.Fatal("the schema let what the discard was shown be rewritten")
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET discarded_at = '2020-01-01T00:00:00Z' WHERE id = ?`, entry.ID); err == nil {
		t.Fatal("the schema let the discarded instant be rewritten")
	}
	land, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/f", Holds: []string{"ffff"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerKeptWork(ctx, land.ID, KeptLand, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET discard_digest = 'abc' WHERE id = ?`, land.ID); err == nil {
		t.Fatal("the schema let a discard digest be written on an entry answered land")
	}
}
