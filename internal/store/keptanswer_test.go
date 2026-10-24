package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// landKept answers the entry land the way a landing does: claimed, then
// answered with the run it started, named after the entry.
func landKept(t *testing.T, s *Store, id int64) KeptWork {
	t.Helper()
	if _, err := s.ClaimKeptWorkLanding(context.Background(), id, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	k, err := s.RecordKeptWorkLanding(context.Background(), id, fmt.Sprintf("run-landing-%d", id))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// discardKept answers the entry discard the way a discard does: confirmed
// on a digest, then marked once the copy is gone.
func discardKept(t *testing.T, s *Store, id int64) KeptWork {
	t.Helper()
	if _, err := s.ConfirmKeptWorkDiscard(context.Background(), id, fmt.Sprintf("digest-%d", id), nil, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	k, err := s.MarkKeptWorkDiscarded(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// carryOnKept answers the entry carry on the way a carry on does: claimed
// naming task, whose row must exist, then marked carried on by it.
func carryOnKept(t *testing.T, s *Store, id int64, task string) KeptWork {
	t.Helper()
	if _, err := s.ClaimKeptWorkCarryOn(context.Background(), id, task, fmt.Sprintf("copy-of-%d", id), ResolvedByPerson()); err != nil {
		t.Fatal(err)
	}
	k, err := s.MarkKeptWorkCarriedOn(context.Background(), id, task)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

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

// A landing is two facts. The claim records who answered land on an open
// entry, once, and leaves it open, listed and owed: a second claim, a claim
// on an entry answered otherwise and the nobody resolver are refused, and
// no other answer is taken on it. It can be withdrawn while unanswered,
// which leaves the entry open with none. The answer is written only with
// the run, from the claim, once: a run on an open entry with no claim and
// on one answered otherwise is refused, the first run stands against a
// second and the same run again is accepted and writes nothing, and the
// owed listing drops the entry once its run is named. The schema refuses
// an entry answered land without its run, a claim written or withdrawn in
// part or under an answer, and a rewrite of either fact.
func TestALandingIsClaimedAndThenAnsweredWithItsRun(t *testing.T) {
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
		t.Fatalf("a landing on an open entry with no claim = %v; want ErrKeptWorkNotLand", err)
	}
	if _, err := s.ConfirmKeptWorkDiscard(ctx, other.ID, "shown", nil, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkKeptWorkDiscarded(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordKeptWorkLanding(ctx, other.ID, run.ID); !errors.Is(err, ErrKeptWorkAnswered) {
		t.Fatalf("a landing on an entry answered discard = %v; want ErrKeptWorkAnswered", err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, other.ID, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkAnswered) {
		t.Fatalf("a claim on an entry answered discard = %v; want ErrKeptWorkAnswered", err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, land.ID, Resolver{}); !errors.Is(err, ErrNoResolver) {
		t.Fatalf("a claim by nobody = %v; want ErrNoResolver", err)
	}
	claimed, err := s.ClaimKeptWorkLanding(ctx, land.ID, ResolvedByMachineInterface())
	if err != nil || !claimed.LandingClaimed() || !claimed.Open() || claimed.LandingBy.Or(Resolver{}) != ResolvedByMachineInterface() ||
		claimed.LandedRun.IsKnown() {
		t.Fatalf("claiming the landing = %+v, %v; want it open and claimed", claimed, err)
	}
	if open, err := s.OpenKeptWork(ctx); err != nil || !slices.ContainsFunc(open, func(k KeptWork) bool { return k.ID == land.ID }) {
		t.Fatalf("OpenKeptWork = %+v, %v; want the claimed entry listed", open, err)
	}
	if owed, err := s.KeptWorkLandingsOwed(ctx); err != nil || len(owed) != 1 || owed[0].ID != land.ID {
		t.Fatalf("KeptWorkLandingsOwed = %+v, %v; want the land entry", owed, err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, land.ID, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkLandingClaimed) ||
		!errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a second claim = %v; want ErrKeptWorkLandingClaimed and ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.ConfirmKeptWorkDiscard(ctx, land.ID, "shown", nil, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a discard on a claimed entry = %v; want ErrKeptWorkCopyOwed", err)
	}
	withdrawn, err := s.WithdrawKeptWorkLanding(ctx, land.ID)
	if err != nil || !withdrawn.Open() || withdrawn.LandingClaimed() || withdrawn.LandingBy.IsKnown() {
		t.Fatalf("withdrawing the claim = %+v, %v; want the entry open with none", withdrawn, err)
	}
	if _, err := s.WithdrawKeptWorkLanding(ctx, land.ID); !errors.Is(err, ErrKeptWorkNotLand) {
		t.Fatalf("withdrawing no claim = %v; want ErrKeptWorkNotLand", err)
	}
	reclaimed, err := s.ClaimKeptWorkLanding(ctx, land.ID, ResolvedByMachineInterface())
	if err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"answered land without the run": `UPDATE kept_work SET answer = 'land', answered_by = landing_by, answered_at = landing_claimed_at WHERE id = ?`,
		"the claim withdrawn in part":   `UPDATE kept_work SET landing_by = NULL WHERE id = ?`,
		"the claim rewritten":           `UPDATE kept_work SET landing_claimed_at = '2020-01-01T00:00:00Z' WHERE id = ?`,
		"a run with no land answer":     `UPDATE kept_work SET landed_run = 'run-direct' WHERE id = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, statement, land.ID); err == nil {
			t.Errorf("the schema let %s be written", name)
		}
	}
	got, err := s.RecordKeptWorkLanding(ctx, land.ID, "run-landing")
	if err != nil || got.Answer.Or("") != KeptLand || got.LandedRun.Or("") != "run-landing" ||
		got.AnsweredBy.Or(Resolver{}) != ResolvedByMachineInterface() || !got.AnsweredAt.Or(time.Time{}).Equal(reclaimed.LandingClaimedAt.Or(time.Time{})) {
		t.Fatalf("recording the landing = %+v, %v; want it answered land from the claim, naming the run", got, err)
	}
	if _, err := s.RecordKeptWorkLanding(ctx, land.ID, "run-landing"); err != nil {
		t.Fatalf("the same landing again = %v; want it accepted", err)
	}
	if _, err := s.RecordKeptWorkLanding(ctx, land.ID, "run-second"); !errors.Is(err, ErrKeptWorkLanded) {
		t.Fatalf("a second landing run = %v; want ErrKeptWorkLanded", err)
	}
	if _, err := s.WithdrawKeptWorkLanding(ctx, land.ID); !errors.Is(err, ErrKeptWorkAnswered) {
		t.Fatalf("withdrawing an answered landing = %v; want ErrKeptWorkAnswered", err)
	}
	if owed, err := s.KeptWorkLandingsOwed(ctx); err != nil || len(owed) != 0 {
		t.Fatalf("KeptWorkLandingsOwed = %+v, %v; want none once the run is named", owed, err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET landing_claimed_at = '2020-01-01T00:00:00Z' WHERE id = ?`, land.ID); err == nil {
		t.Fatal("the schema let the landing claim be rewritten")
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET landed_run = 'run-other' WHERE id = ?`, land.ID); err == nil {
		t.Fatal("the schema let a landing run be rewritten")
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET landed_run = 'run-other' WHERE id = ?`, other.ID); err == nil {
		t.Fatal("the schema let a landing run be written on an entry answered discard")
	}
}

// A carry on is two facts. The claim names the task that will carry the
// entry on and the copy it adopts, refusing an identifier the home would
// refuse, an empty copy and the generic answer, and leaves the entry open,
// listed and owed: no second claim and no other answer is taken on it. It
// can be withdrawn only while the task it names does not exist, which
// leaves the entry open with none. The answer is written only with the
// adoption's mark, from the claim, once, and only once the task exists;
// and the schema refuses an entry answered carry-on without the adoption,
// a claim written in part, withdrawn under an answer or written on an
// answered entry, and a rewrite of either fact. Another entry of the copy
// takes no answer while the claim stands; the mark answers it with the
// claim whether it was kept before the claim or after, since the copy
// moved whole, and leaves an entry of another copy open.
func TestACarryOnIsClaimedAndThenAnsweredWithItsAdoption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/c", Holds: []string{"cccc"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimKeptWorkCarryOn(ctx, entry.ID, "not-a-task", "planted-copy", ResolvedByMachineInterface()); err == nil {
		t.Fatal("a carry-on claim naming an invalid task was recorded")
	}
	if _, err := s.ClaimKeptWorkCarryOn(ctx, entry.ID, "taskcarrying", " ", ResolvedByMachineInterface()); err == nil {
		t.Fatal("a carry-on claim naming no copy was recorded")
	}
	if _, err := s.MarkKeptWorkCarriedOn(ctx, entry.ID, "taskcarrying"); !errors.Is(err, ErrKeptWorkNotCarryOn) {
		t.Fatalf("marking an open entry carried on = %v; want ErrKeptWorkNotCarryOn", err)
	}
	sibling, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/c", Holds: []string{"c222"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimKeptWorkCarryOn(ctx, entry.ID, "taskcarrying", "planted-copy", ResolvedByMachineInterface())
	if err != nil || !claimed.Open() || !claimed.CarryOnClaimed() || claimed.CarriedOnTask.Or("") != "taskcarrying" ||
		claimed.CarryOnBy.Or(Resolver{}) != ResolvedByMachineInterface() || !claimed.CarryOnClaimedAt.IsKnown() || claimed.CarriedOnAt.IsKnown() {
		t.Fatalf("the carry-on claim = %+v, %v; want it open, claimed by taskcarrying, not yet carried on", claimed, err)
	}
	if open, err := s.OpenKeptWork(ctx); err != nil || !slices.ContainsFunc(open, func(k KeptWork) bool { return k.ID == entry.ID }) {
		t.Fatalf("OpenKeptWork = %+v, %v; want the claimed entry listed", open, err)
	}
	if owed, err := s.KeptWorkCarryOnsOwed(ctx); err != nil || len(owed) != 1 || owed[0].ID != entry.ID {
		t.Fatalf("KeptWorkCarryOnsOwed = %+v, %v; want the entry", owed, err)
	}
	if _, err := s.ClaimKeptWorkCarryOn(ctx, entry.ID, "taskanothers", "planted-copy", ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) ||
		!errors.Is(err, ErrKeptWorkCarryOnClaimed) {
		t.Fatalf("a second claim = %v; want ErrKeptWorkCopyOwed and ErrKeptWorkCarryOnClaimed", err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, entry.ID, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a land on a claimed entry = %v; want ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, sibling.ID, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a land on another entry of the claimed copy = %v; want ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.ConfirmKeptWorkDiscard(ctx, entry.ID, "shown", nil, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a discard on a claimed entry = %v; want ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.MarkKeptWorkCarriedOn(ctx, entry.ID, "taskcarrying"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("marking a claim whose task does not exist = %v; want ErrNotFound", err)
	}
	if _, err := s.WithdrawKeptWorkCarryOn(ctx, entry.ID, "taskanothers"); !errors.Is(err, ErrKeptWorkNotClaimed) {
		t.Fatalf("withdrawing a claim of another task = %v; want ErrKeptWorkNotClaimed", err)
	}
	withdrawn, err := s.WithdrawKeptWorkCarryOn(ctx, entry.ID, "taskcarrying")
	if err != nil || !withdrawn.Open() || withdrawn.CarryOnClaimed() || withdrawn.CarriedOnCopy.IsKnown() || withdrawn.CarryOnBy.IsKnown() {
		t.Fatalf("the withdrawal = %+v, %v; want the entry open with no claim", withdrawn, err)
	}

	reclaimed, err := s.ClaimKeptWorkCarryOn(ctx, entry.ID, "taskcarrying", "planted-copy", ResolvedByMachineInterface())
	if err != nil {
		t.Fatal(err)
	}
	seedTask(t, s, "taskcarrying")
	late, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/c", Holds: []string{"c333"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/d", Holds: []string{"d444"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if !late.KeptAt.After(reclaimed.CarryOnClaimedAt.Or(time.Time{})) {
		t.Fatalf("the late entry was kept at %v, not after the claim at %v", late.KeptAt, reclaimed.CarryOnClaimedAt)
	}
	if _, err := s.WithdrawKeptWorkCarryOn(ctx, entry.ID, "taskcarrying"); !errors.Is(err, ErrCarryOnTaskStands) {
		t.Fatalf("withdrawing a claim whose task exists = %v; want ErrCarryOnTaskStands", err)
	}
	for name, statement := range map[string]string{
		"answered carry-on without the adoption": `UPDATE kept_work SET answer = 'carry-on', answered_by = carry_on_by, answered_at = carry_on_claimed_at WHERE id = ?`,
		"the claim withdrawn in part":            `UPDATE kept_work SET carried_on_task = NULL WHERE id = ?`,
		"the claim rewritten":                    `UPDATE kept_work SET carried_on_task = 'taskother000' WHERE id = ?`,
		"carried on with no answer":              `UPDATE kept_work SET carried_on_at = '2020-01-01T00:00:00Z' WHERE id = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, statement, entry.ID); err == nil {
			t.Errorf("the schema let %s be written", name)
		}
	}
	if _, err := s.MarkKeptWorkCarriedOn(ctx, entry.ID, "taskanothers"); !errors.Is(err, ErrKeptWorkNotCarryOn) {
		t.Fatalf("marking the claim carried on by a task it does not name = %v; want ErrKeptWorkNotCarryOn", err)
	}
	done, err := s.MarkKeptWorkCarriedOn(ctx, entry.ID, "taskcarrying")
	if err != nil || done.Answer.Or("") != KeptCarryOn || done.AnsweredBy.Or(Resolver{}) != ResolvedByMachineInterface() ||
		!done.AnsweredAt.Or(time.Time{}).Equal(reclaimed.CarryOnClaimedAt.Or(time.Time{})) || !done.CarriedOnAt.IsKnown() ||
		done.CarriedOnTask.Or("") != "taskcarrying" {
		t.Fatalf("marking the entry carried on = %+v, %v; want it answered carry-on from the claim, and carried on", done, err)
	}
	if _, err := s.MarkKeptWorkCarriedOn(ctx, entry.ID, "taskcarrying"); !errors.Is(err, ErrKeptWorkCarriedOn) {
		t.Fatalf("marking it again = %v; want ErrKeptWorkCarriedOn", err)
	}
	if got, err := s.KeptWorkEntry(ctx, sibling.ID); err != nil || got.Answer.Or("") != KeptCarryOn || got.CarriedOnTask.Or("") != "taskcarrying" ||
		!got.CarriedOnAt.IsKnown() {
		t.Fatalf("the copy's entry kept before the claim reads %+v, %v; want it carried on with the claim", got, err)
	}
	if got, err := s.KeptWorkEntry(ctx, late.ID); err != nil || got.Answer.Or("") != KeptCarryOn || got.CarriedOnTask.Or("") != "taskcarrying" ||
		!got.CarriedOnAt.IsKnown() {
		t.Fatalf("the copy's entry kept after the claim reads %+v, %v; want it carried on with the claim, since its state moved with the copy", got, err)
	}
	if got, err := s.KeptWorkEntry(ctx, elsewhere.ID); err != nil || !got.Open() || got.CarriedOnTask.IsKnown() {
		t.Fatalf("another copy's entry reads %+v, %v; want it open, not taken by a carry on of another copy", got, err)
	}
	if owed, err := s.KeptWorkCarryOnsOwed(ctx); err != nil || len(owed) != 0 {
		t.Fatalf("KeptWorkCarryOnsOwed = %+v, %v; want none once carried on", owed, err)
	}
	if _, err := s.WithdrawKeptWorkCarryOn(ctx, entry.ID, "taskcarrying"); !errors.Is(err, ErrKeptWorkAnswered) {
		t.Fatalf("withdrawing an answered carry on = %v; want ErrKeptWorkAnswered", err)
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
	landKept(t, s, land.ID)
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET carried_on_task = 'taskother000', carried_on_copy = 'c', carry_on_by = 'machine',
		carry_on_claimed_at = '2020-01-01T00:00:00Z' WHERE id = ?`, land.ID); err == nil {
		t.Fatal("the schema let a carry-on claim be written on an entry answered land")
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
	if _, err := s.ClaimKeptWorkLanding(ctx, entry.ID, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
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
	landKept(t, s, land.ID)
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
// of the second, once when the removal's read of the copy began is
// recorded on it, answers
// and marks the third and the one kept after the confirmation before that
// read with the same digest, commits and resolver, and leaves the land
// entry, the other copy's entry and an entry kept after the read as they
// were, after which that one can be answered. Without the siblings in the
// mark the third is left open over a copy that is gone; with the bound at
// the confirmation, so is the one kept before the read.
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
	if _, err := s.ClaimKeptWorkLanding(ctx, land.ID, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmKeptWorkDiscard(ctx, second.ID, "shown", []string{"g222"}, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a discard while a land of the copy is owed = %v; want ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.ClaimKeptWorkCarryOn(ctx, third.ID, "taskcarrying", "planted-copy", ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a carry on while a land of the copy is owed = %v; want ErrKeptWorkCopyOwed", err)
	}
	if got := read(second); !got.Open() || got.DiscardConfirmed() || !read(third).Open() {
		t.Fatalf("the refused answers wrote: %+v, %+v", got, read(third))
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
	if _, err := s.ClaimKeptWorkCarryOn(ctx, third.ID, "taskcarrying", "planted-copy", ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a carry on while a discard of the copy is confirmed = %v; want ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.ConfirmKeptWorkDiscard(ctx, third.ID, "shown", nil, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a second confirmation of the copy = %v; want ErrKeptWorkCopyOwed", err)
	}
	late := record("/worktrees/g", "g444")
	if _, err := s.ClaimKeptWorkLanding(ctx, late.ID, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a land while a discard of the copy is confirmed = %v; want ErrKeptWorkCopyOwed", err)
	}

	// The removal's read of the copy begins once late is kept, and unread
	// is kept after it began.
	removalRead := time.Now()
	unread := record("/worktrees/g", "g555")
	if !unread.KeptAt.After(removalRead) {
		t.Fatalf("the entry kept after the read was kept at %v, not after the read at %v", unread.KeptAt, removalRead)
	}
	if err := s.RecordKeptWorkDiscardRead(ctx, second.ID, "shown", removalRead); err != nil {
		t.Fatal(err)
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
	if got := read(late); got.Answer.Or("") != KeptDiscard || !got.DiscardedAt.IsKnown() {
		t.Fatalf("the entry kept after the confirmation and before the removal's read reads %+v; want it answered discard, "+
			"since the read the removal was held to covers it", got)
	}
	if got := read(unread); !got.Open() || got.DiscardDigest.IsKnown() {
		t.Fatalf("the entry kept after the removal's read reads %+v; want it open, not taken by a discard that did not read it", got)
	}
	if owed, err := s.KeptWorkDiscardsOwed(ctx); err != nil || len(owed) != 0 {
		t.Fatalf("KeptWorkDiscardsOwed = %+v, %v; want none once the copy is marked gone", owed, err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, unread.ID, ResolvedByMachineInterface()); err != nil {
		t.Fatalf("the entry kept after the read once nothing of its copy is owed: %v", err)
	}
}

// A discard's mark is bounded by the read its removal was held to, as
// recorded on the entry, never by when the mark is written, the final
// review of c04c40f at the store: an entry is confirmed, a second entry of
// the copy is kept after the confirmation, the read's start is recorded,
// and a third is kept after it; the mark, written later as the next start
// writes it over a copy already gone, answers the first two and leaves the
// third open. Without the read recorded, the mark took the caller's
// instant and with it the third. The read is recorded only under the
// confirmation of its digest on an entry still owed (ErrKeptWorkNotConfirmed,
// ErrKeptWorkAnswered, ErrKeptWorkClosed, nothing written), a withdrawal
// takes it with the confirmation, and the schema refuses every other write
// of it.
func TestADiscardsMarkIsBoundedByTheReadRecordedOnTheEntry(t *testing.T) {
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
	readAt := func(id int64) string {
		t.Helper()
		var at sql.NullString
		if err := s.write.QueryRowContext(ctx, `SELECT discard_read_at FROM kept_work WHERE id = ?`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at.String
	}
	first := record("/worktrees/r", "r111")
	if err := s.RecordKeptWorkDiscardRead(ctx, first.ID, "shown", time.Now()); !errors.Is(err, ErrKeptWorkNotConfirmed) || readAt(first.ID) != "" {
		t.Fatalf("a read recorded with no confirmation = %v, %q; want ErrKeptWorkNotConfirmed and nothing", err, readAt(first.ID))
	}
	if _, err := s.ConfirmKeptWorkDiscard(ctx, first.ID, "shown", []string{"r111"}, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordKeptWorkDiscardRead(ctx, first.ID, "another", time.Now()); !errors.Is(err, ErrKeptWorkNotConfirmed) || readAt(first.ID) != "" {
		t.Fatalf("a read recorded under another digest = %v, %q; want ErrKeptWorkNotConfirmed and nothing", err, readAt(first.ID))
	}
	before := record("/worktrees/r", "r222")
	read := time.Now()
	if err := s.RecordKeptWorkDiscardRead(ctx, first.ID, "shown", read); err != nil {
		t.Fatal(err)
	}
	after := record("/worktrees/r", "r333")
	if !after.KeptAt.After(read) {
		t.Fatalf("the entry kept after the read was kept at %v, not after %v", after.KeptAt, read)
	}
	if _, err := s.MarkKeptWorkDiscarded(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{first.ID, before.ID} {
		if got, err := s.KeptWorkEntry(ctx, id); err != nil || got.Answer.Or("") != KeptDiscard || !got.DiscardedAt.IsKnown() {
			t.Fatalf("entry %d, kept by the read, reads %+v, %v; want it answered discard and marked", id, got, err)
		}
	}
	if got, err := s.KeptWorkEntry(ctx, after.ID); err != nil || !got.Open() || got.DiscardDigest.IsKnown() {
		t.Fatalf("the entry kept after the read reads %+v, %v; want it open, the mark written after it notwithstanding", got, err)
	}
	if err := s.RecordKeptWorkDiscardRead(ctx, first.ID, "shown", time.Now()); !errors.Is(err, ErrKeptWorkAnswered) ||
		readAt(first.ID) != encodeTime(read) {
		t.Fatalf("a read recorded once discarded = %v, %q; want ErrKeptWorkAnswered and the read as it was", err, readAt(first.ID))
	}

	withdrawn := record("/worktrees/w", "w111")
	if _, err := s.ConfirmKeptWorkDiscard(ctx, withdrawn.ID, "changed", nil, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordKeptWorkDiscardRead(ctx, withdrawn.ID, "changed", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WithdrawKeptWorkDiscard(ctx, withdrawn.ID, "changed"); err != nil || readAt(withdrawn.ID) != "" {
		t.Fatalf("the withdrawal = %v and left the read %q; want it taken with the confirmation", err, readAt(withdrawn.ID))
	}

	confirmed := record("/worktrees/c", "c111")
	if _, err := s.ConfirmKeptWorkDiscard(ctx, confirmed.ID, "kept", nil, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordKeptWorkDiscardRead(ctx, confirmed.ID, "kept", time.Now()); err != nil {
		t.Fatal(err)
	}
	unconfirmed := record("/worktrees/u", "u111")
	for name, write := range map[string]struct {
		statement string
		id        int64
	}{
		"a read with no confirmation":          {`UPDATE kept_work SET discard_read_at = kept_at WHERE id = ?`, unconfirmed.ID},
		"a read rewritten once discarded":      {`UPDATE kept_work SET discard_read_at = kept_at WHERE id = ?`, first.ID},
		"a read cleared once discarded":        {`UPDATE kept_work SET discard_read_at = NULL WHERE id = ?`, first.ID},
		"a confirmation withdrawn in its read": {`UPDATE kept_work SET discard_digest = NULL, discard_lacks = NULL, discard_by = NULL, discard_confirmed_at = NULL WHERE id = ?`, confirmed.ID},
	} {
		if _, err := s.write.ExecContext(ctx, write.statement, write.id); err == nil || !strings.Contains(err.Error(), "kept work entry") {
			t.Errorf("%s was not refused by the schema: %v", name, err)
		}
	}

	// A removal on a proof closes the confirmed entries of the run's copies
	// still owed, after which no read is recorded on them.
	if _, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run.ID, Proof: Known(CleanupProofRemoteBranch), Examined: []string{"aaaa"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRunCleanupRemoved(ctx, run.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	stamped := readAt(confirmed.ID)
	if err := s.RecordKeptWorkDiscardRead(ctx, confirmed.ID, "kept", time.Now()); !errors.Is(err, ErrKeptWorkClosed) || readAt(confirmed.ID) != stamped {
		t.Fatalf("a read recorded on a closed entry = %v, %q; want ErrKeptWorkClosed and the read as it was", err, readAt(confirmed.ID))
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET discard_read_at = kept_at WHERE id = ?`, confirmed.ID); err == nil ||
		!strings.Contains(err.Error(), "kept work entry") {
		t.Errorf("a read rewritten on a closed entry was not refused by the schema: %v", err)
	}
}

// KeptWorkCarriedOnBy reads the entries a carry on answered for a task, the
// copy identity each recorded with them, and nothing else: a claim not yet
// carried out is not among them, nor an entry carried on by another task,
// and a task no carry on named reads none.
func TestTheKeptWorkATaskCarriedOnIsReadByThatTask(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedRun(t, s) // records the repository the tasks name
	finished := seedTaskOnBranch(t, s, "finishedaaaa", "topic", 0)
	keep := func(path string) KeptWork {
		t.Helper()
		k, _, err := s.RecordKeptWork(ctx, KeptCopy{TaskID: finished.ID, CopyPath: path,
			Holds: []string{"aaaa"}, Reason: "unproven", Against: "dddd"})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	answered, owed, other := keep("/kept/one"), keep("/kept/two"), keep("/kept/three")
	carrier := seedTaskOnBranch(t, s, "carriesonaaa", "topic", finished.Row)
	elsewhere := seedTaskOnBranch(t, s, "carriesonbbb", "topic", carrier.Row)
	nobody := seedTaskOnBranch(t, s, "carriesnonea", "topic", elsewhere.Row)
	carryOnKept(t, s, answered.ID, carrier.ID)
	if _, err := s.ClaimKeptWorkCarryOn(ctx, owed.ID, carrier.ID, "copy-owed", ResolvedByPerson()); err != nil {
		t.Fatal(err)
	}
	carryOnKept(t, s, other.ID, elsewhere.ID)

	got, err := s.KeptWorkCarriedOnBy(ctx, carrier.ID)
	if err != nil || len(got) != 1 || got[0].ID != answered.ID || got[0].CarriedOnCopy.Or("") != fmt.Sprintf("copy-of-%d", answered.ID) {
		t.Fatalf("KeptWorkCarriedOnBy(carrier) = %+v, %v; want entry %d with its copy identity alone", got, err, answered.ID)
	}
	if got, err := s.KeptWorkCarriedOnBy(ctx, elsewhere.ID); err != nil || len(got) != 1 || got[0].ID != other.ID {
		t.Fatalf("KeptWorkCarriedOnBy(elsewhere) = %+v, %v; want entry %d", got, err, other.ID)
	}
	if got, err := s.KeptWorkCarriedOnBy(ctx, nobody.ID); err != nil || len(got) != 0 {
		t.Fatalf("KeptWorkCarriedOnBy(nobody) = %+v, %v; want none", got, err)
	}
}
