package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
// on an entry already answered and the nobody resolver are refused. It can
// be withdrawn while unanswered, which leaves the entry open with none. The
// answer is written only with the run, from the claim, once: a run on an
// open entry with no claim and one naming another run on an entry already
// landed are refused, the first run stands against a
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
	landKept(t, s, other.ID)
	if _, err := s.RecordKeptWorkLanding(ctx, other.ID, run.ID); !errors.Is(err, ErrKeptWorkLanded) {
		t.Fatalf("a landing naming another run on an entry already landed = %v; want ErrKeptWorkLanded", err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, other.ID, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkAnswered) {
		t.Fatalf("a claim on an entry already answered = %v; want ErrKeptWorkAnswered", err)
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
