package store

import (
	"context"
	"errors"
	"slices"
	"strings"
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
	if _, err := s.ConfirmKeptWorkDiscard(ctx, other.ID, "shown", nil, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkKeptWorkDiscarded(ctx, other.ID); err != nil {
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
	if _, err := s.AnswerKeptWorkCarryOn(ctx, entry.ID, "not-a-task", "planted-copy", ResolvedByMachineInterface()); err == nil {
		t.Fatal("a carry-on answer naming an invalid task was recorded")
	}
	if _, err := s.MarkKeptWorkCarriedOn(ctx, entry.ID); !errors.Is(err, ErrKeptWorkNotCarryOn) {
		t.Fatalf("marking an open entry carried on = %v; want ErrKeptWorkNotCarryOn", err)
	}
	answered, err := s.AnswerKeptWorkCarryOn(ctx, entry.ID, "taskcarrying", "planted-copy", ResolvedByMachineInterface())
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

// A discard is two facts. The confirmation records what it was shown, who
// confirmed and when, refusing an empty digest, nobody and the generic
// answer, and leaves the entry open, listed and owed: no second
// confirmation and no other answer is taken on it, and it can be withdrawn
// only with its own digest, which leaves the entry open with none. The
// answer is written only with the removal's mark, from the confirmation,
// once; and the schema refuses an entry answered discard without the
// removal, a confirmation written in part or on an answered entry, and a
// rewrite of either fact.
func TestADiscardIsConfirmedAndThenAnsweredWithItsRemoval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/e", Holds: []string{"eeee"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	listed := func() bool {
		t.Helper()
		open, err := s.OpenKeptWork(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(open, func(k KeptWork) bool { return k.ID == entry.ID })
	}
	owed := func() []KeptWork {
		t.Helper()
		got, err := s.KeptWorkDiscardsOwed(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if _, err := s.AnswerKeptWork(ctx, entry.ID, KeptDiscard, ResolvedByMachineInterface()); err == nil {
		t.Fatal("a discard answer with no removal was recorded")
	}
	if _, err := s.ConfirmKeptWorkDiscard(ctx, entry.ID, " ", nil, ResolvedByMachineInterface()); err == nil {
		t.Fatal("a discard confirmation with an empty digest was recorded")
	}
	if _, err := s.ConfirmKeptWorkDiscard(ctx, entry.ID, "abc123", nil, Resolver{}); !errors.Is(err, ErrNoResolver) {
		t.Fatalf("a confirmation from nobody = %v; want ErrNoResolver", err)
	}
	if _, err := s.MarkKeptWorkDiscarded(ctx, entry.ID); !errors.Is(err, ErrKeptWorkNotDiscard) {
		t.Fatalf("marking an entry with no confirmation discarded = %v; want ErrKeptWorkNotDiscard", err)
	}
	if _, err := s.WithdrawKeptWorkDiscard(ctx, entry.ID, "abc123"); !errors.Is(err, ErrKeptWorkNotConfirmed) {
		t.Fatalf("withdrawing a confirmation never made = %v; want ErrKeptWorkNotConfirmed", err)
	}

	confirmed, err := s.ConfirmKeptWorkDiscard(ctx, entry.ID, "abc123", []string{"eeee"}, ResolvedByMachineInterface())
	if err != nil || !confirmed.Open() || !confirmed.DiscardConfirmed() || confirmed.DiscardDigest.Or("") != "abc123" ||
		!slices.Equal(confirmed.DiscardLacks, []string{"eeee"}) || confirmed.DiscardBy.Or(Resolver{}) != ResolvedByMachineInterface() ||
		!confirmed.DiscardConfirmedAt.IsKnown() || confirmed.Answered() || confirmed.DiscardedAt.IsKnown() {
		t.Fatalf("the confirmation = %+v, %v; want it open, confirmed abc123 losing eeee by the machine interface, unanswered", confirmed, err)
	}
	if !listed() {
		t.Fatal("a confirmed entry is not listed among the open kept work")
	}
	if got := owed(); len(got) != 1 || got[0].ID != entry.ID {
		t.Fatalf("KeptWorkDiscardsOwed = %+v; want the entry", got)
	}
	if _, err := s.ConfirmKeptWorkDiscard(ctx, entry.ID, "def456", nil, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkDiscardConfirmed) {
		t.Fatalf("a second confirmation = %v; want ErrKeptWorkDiscardConfirmed", err)
	}
	if _, err := s.AnswerKeptWork(ctx, entry.ID, KeptLand, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a land over the confirmation = %v; want ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.WithdrawKeptWorkDiscard(ctx, entry.ID, "def456"); !errors.Is(err, ErrKeptWorkNotConfirmed) {
		t.Fatalf("withdrawing another digest's confirmation = %v; want ErrKeptWorkNotConfirmed", err)
	}
	withdrawn, err := s.WithdrawKeptWorkDiscard(ctx, entry.ID, "abc123")
	if err != nil || !withdrawn.Open() || withdrawn.DiscardConfirmed() || withdrawn.DiscardDigest.IsKnown() || withdrawn.DiscardLacks != nil ||
		withdrawn.DiscardBy.IsKnown() || withdrawn.DiscardConfirmedAt.IsKnown() {
		t.Fatalf("the withdrawal = %+v, %v; want the entry open with no confirmation", withdrawn, err)
	}
	if got := owed(); len(got) != 0 || !listed() {
		t.Fatalf("after the withdrawal KeptWorkDiscardsOwed = %+v and listed = %v; want none owed and the entry listed", got, listed())
	}

	again, err := s.ConfirmKeptWorkDiscard(ctx, entry.ID, "def456", []string{"eeee"}, ResolvedByMachineInterface())
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.MarkKeptWorkDiscarded(ctx, entry.ID)
	if err != nil || done.Answer.Or("") != KeptDiscard || done.AnsweredBy.Or(Resolver{}) != ResolvedByMachineInterface() ||
		done.AnsweredAt != again.DiscardConfirmedAt || done.DiscardDigest.Or("") != "def456" || !done.DiscardedAt.IsKnown() || done.Open() {
		t.Fatalf("the mark = %+v, %v; want it answered discard as confirmed, def456, and marked", done, err)
	}
	if _, err := s.MarkKeptWorkDiscarded(ctx, entry.ID); !errors.Is(err, ErrKeptWorkDiscarded) {
		t.Fatalf("marking it again = %v; want ErrKeptWorkDiscarded", err)
	}
	if _, err := s.WithdrawKeptWorkDiscard(ctx, entry.ID, "def456"); !errors.Is(err, ErrKeptWorkAnswered) {
		t.Fatalf("withdrawing a confirmation carried out = %v; want ErrKeptWorkAnswered", err)
	}
	if got := owed(); len(got) != 0 || listed() {
		t.Fatalf("once discarded KeptWorkDiscardsOwed = %+v and listed = %v; want neither", got, listed())
	}

	open, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/f", Holds: []string{"ffff"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	land, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/l", Holds: []string{"llll"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerKeptWork(ctx, land.ID, KeptLand, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	for name, write := range map[string]struct {
		statement string
		id        int64
	}{
		"an answer with no removal": {`UPDATE kept_work SET answer = 'discard', answered_by = 'machine', answered_at = kept_at,
			discard_digest = 'x', discard_lacks = '[]', discard_by = 'machine', discard_confirmed_at = kept_at WHERE id = ?`, open.ID},
		"a confirmation in part":            {`UPDATE kept_work SET discard_digest = 'x' WHERE id = ?`, open.ID},
		"a confirmation on an answered one": {`UPDATE kept_work SET discard_digest = 'x', discard_lacks = '[]', discard_by = 'machine', discard_confirmed_at = kept_at WHERE id = ?`, land.ID},
		"a rewritten confirmation":          {`UPDATE kept_work SET discard_digest = 'other' WHERE id = ?`, entry.ID},
		"a withdrawal once discarded": {`UPDATE kept_work SET discard_digest = NULL, discard_lacks = NULL, discard_by = NULL,
			discard_confirmed_at = NULL WHERE id = ?`, entry.ID},
		"a rewritten removal": {`UPDATE kept_work SET discarded_at = '2020-01-01T00:00:00.000000000Z' WHERE id = ?`, entry.ID},
		"a removal with no answer": {`UPDATE kept_work SET discard_digest = 'x', discard_lacks = '[]', discard_by = 'machine',
			discard_confirmed_at = kept_at, discarded_at = kept_at WHERE id = ?`, open.ID},
	} {
		if _, err := s.write.ExecContext(ctx, write.statement, write.id); err == nil || !strings.Contains(err.Error(), "kept work entry") {
			t.Errorf("%s was not refused by the schema: %v", name, err)
		}
	}
}

// A discard settles every entry of its copy kept by its confirmation, and
// no entry is answered or confirmed while another of its copy holds an
// answer not yet carried out. Three entries name one copy and a fourth
// another: with the first answered land and its run not named, a discard
// confirmation and a carry on of the others are refused with
// ErrKeptWorkCopyOwed and write nothing; once the landing is recorded, the
// second is confirmed, which leaves the third open and takes no answer or
// confirmation on it, nor on an entry of the copy kept after; and the mark
// of the second answers and marks the third with the same digest, commits
// and resolver, leaves the land entry, the other copy's entry and the
// entry kept after the confirmation as they were, after which that one
// can be answered. Without the siblings in the mark the third is left
// open over a copy that is gone.
func TestADiscardSettlesEveryEntryOfItsCopyAndWaitsOnAnOwedAnswer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	record := func(path, holds string) KeptWork {
		t.Helper()
		k, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: path, Holds: []string{holds}, Reason: "unproven", Against: "mmmm"})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	read := func(k KeptWork) KeptWork {
		t.Helper()
		got, err := s.KeptWorkEntry(ctx, k.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	land, second, third := record("/worktrees/g", "g111"), record("/worktrees/g", "g222"), record("/worktrees/g", "g333")
	elsewhere := record("/worktrees/h", "h111")
	if _, err := s.AnswerKeptWork(ctx, land.ID, KeptLand, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmKeptWorkDiscard(ctx, second.ID, "shown", []string{"g222"}, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a discard while a land of the copy is owed = %v; want ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.AnswerKeptWorkCarryOn(ctx, third.ID, "taskcarrying", "planted-copy", ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a carry on while a land of the copy is owed = %v; want ErrKeptWorkCopyOwed", err)
	}
	if got := read(second); !got.Open() || got.DiscardConfirmed() || !read(third).Open() {
		t.Fatalf("the refused answers wrote: %+v, %+v", got, read(third))
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, land.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordKeptWorkLanding(ctx, land.ID, "run-landing"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ConfirmKeptWorkDiscard(ctx, second.ID, "shown", []string{"g222"}, ResolvedByMachineInterface()); err != nil {
		t.Fatalf("the discard confirmed once the landing is recorded: %v", err)
	}
	if got := read(third); !got.Open() || got.DiscardConfirmed() {
		t.Fatalf("the copy's other entry reads %+v; want it open and unconfirmed until the mark", got)
	}
	if _, err := s.AnswerKeptWorkCarryOn(ctx, third.ID, "taskcarrying", "planted-copy", ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a carry on while a discard of the copy is confirmed = %v; want ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.ConfirmKeptWorkDiscard(ctx, third.ID, "shown", nil, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a second confirmation of the copy = %v; want ErrKeptWorkCopyOwed", err)
	}
	late := record("/worktrees/g", "g444")
	if _, err := s.AnswerKeptWork(ctx, late.ID, KeptLand, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a land while a discard of the copy is confirmed = %v; want ErrKeptWorkCopyOwed", err)
	}

	if _, err := s.MarkKeptWorkDiscarded(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	sibling := read(third)
	if sibling.Answer.Or("") != KeptDiscard || sibling.DiscardDigest.Or("") != "shown" || !slices.Equal(sibling.DiscardLacks, []string{"g222"}) ||
		sibling.AnsweredBy.Or(Resolver{}) != ResolvedByMachineInterface() || !sibling.DiscardedAt.IsKnown() {
		t.Fatalf("the copy's other entry reads %+v; want it answered discard with what the discard was shown, and marked", sibling)
	}
	if got := read(land); got.Answer.Or("") != KeptLand || got.DiscardDigest.IsKnown() {
		t.Fatalf("the land entry reads %+v; want it as it was", got)
	}
	if !read(elsewhere).Open() {
		t.Fatalf("another copy's entry reads %+v; want it open", read(elsewhere))
	}
	if got := read(late); !got.Open() || got.DiscardDigest.IsKnown() {
		t.Fatalf("the entry kept after the confirmation reads %+v; want it open, not taken by a discard it was not shown in", got)
	}
	if owed, err := s.KeptWorkDiscardsOwed(ctx); err != nil || len(owed) != 0 {
		t.Fatalf("KeptWorkDiscardsOwed = %+v, %v; want none once the copy is marked gone", owed, err)
	}
	if _, err := s.AnswerKeptWork(ctx, late.ID, KeptLand, ResolvedByMachineInterface()); err != nil {
		t.Fatalf("the late entry once nothing of its copy is owed: %v", err)
	}
}
