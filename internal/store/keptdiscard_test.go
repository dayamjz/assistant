package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// A discard is two facts. The claim records the digest and the commits it
// was shown, who answered discard on an open entry, once, and leaves it
// open, listed and owed: a second claim, a claim on an entry already
// answered, an empty digest and the nobody resolver are refused. It can be
// withdrawn while unanswered, which leaves the entry open with none. The
// answer is written only with the removal, from the claim, once, and
// closes every other open entry of the copy the caller held it to (a mark
// naming neither is refused with nothing written); the schema refuses an
// entry answered discard without the removal, a claim written or withdrawn
// in part or under an answer, and a rewrite of either fact. Another entry
// of the copy takes no answer while the claim stands.
func TestADiscardIsClaimedAndThenAnsweredWithItsRemoval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/discard-a", Holds: []string{"aaaa"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordKeptWorkDiscard(ctx, entry.ID, nil); !errors.Is(err, ErrKeptWorkNotDiscard) {
		t.Fatalf("a discard on an open entry with no claim = %v; want ErrKeptWorkNotDiscard", err)
	}
	sibling, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/discard-a", Holds: []string{"a222"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimKeptWorkDiscard(ctx, entry.ID, "", []string{"aaaa"}, ResolvedByPerson()); err == nil {
		t.Fatal("a discard claim naming no digest was recorded")
	}
	if _, err := s.ClaimKeptWorkDiscard(ctx, entry.ID, "digest-shown", []string{"aaaa"}, Resolver{}); !errors.Is(err, ErrNoResolver) {
		t.Fatalf("a claim by nobody = %v; want ErrNoResolver", err)
	}
	claimed, err := s.ClaimKeptWorkDiscard(ctx, entry.ID, "digest-shown", []string{"aaaa", "bbbb"}, ResolvedByPerson())
	if err != nil || !claimed.DiscardClaimed() || !claimed.Open() || claimed.DiscardBy.Or(Resolver{}) != ResolvedByPerson() ||
		claimed.DiscardDigest.Or("") != "digest-shown" || !slices.Equal(claimed.DiscardLacks, []string{"aaaa", "bbbb"}) ||
		claimed.DiscardedAt.IsKnown() {
		t.Fatalf("claiming the discard = %+v, %v; want it open and claimed with the digest and commits shown", claimed, err)
	}
	if open, err := s.OpenKeptWork(ctx); err != nil || !slices.ContainsFunc(open, func(k KeptWork) bool { return k.ID == entry.ID }) {
		t.Fatalf("OpenKeptWork = %+v, %v; want the claimed entry listed", open, err)
	}
	if owed, err := s.KeptWorkDiscardsOwed(ctx); err != nil || len(owed) != 1 || owed[0].ID != entry.ID {
		t.Fatalf("KeptWorkDiscardsOwed = %+v, %v; want the discard entry", owed, err)
	}
	if _, err := s.ClaimKeptWorkDiscard(ctx, entry.ID, "digest-other", []string{"aaaa"}, ResolvedByPerson()); !errors.Is(err, ErrKeptWorkDiscardClaimed) ||
		!errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a second claim = %v; want ErrKeptWorkDiscardClaimed and ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, entry.ID, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a land on a claimed entry = %v; want ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, sibling.ID, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a land on another entry of the claimed copy = %v; want ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.ClaimKeptWorkCarryOn(ctx, sibling.ID, "taskdiscarda", "copy-a", ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a carry on of another entry of the claimed copy = %v; want ErrKeptWorkCopyOwed", err)
	}
	withdrawn, err := s.WithdrawKeptWorkDiscard(ctx, entry.ID)
	if err != nil || !withdrawn.Open() || withdrawn.DiscardClaimed() || withdrawn.DiscardBy.IsKnown() ||
		withdrawn.DiscardDigest.IsKnown() || withdrawn.DiscardLacks != nil {
		t.Fatalf("withdrawing the claim = %+v, %v; want the entry open with none", withdrawn, err)
	}
	if _, err := s.WithdrawKeptWorkDiscard(ctx, entry.ID); !errors.Is(err, ErrKeptWorkNotDiscard) {
		t.Fatalf("withdrawing no claim = %v; want ErrKeptWorkNotDiscard", err)
	}
	reclaimed, err := s.ClaimKeptWorkDiscard(ctx, entry.ID, "digest-shown", []string{"aaaa", "bbbb"}, ResolvedByPerson())
	if err != nil {
		t.Fatal(err)
	}
	late, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/discard-a", Holds: []string{"a333"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/discard-b", Holds: []string{"b444"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if !late.KeptAt.After(reclaimed.DiscardClaimedAt.Or(time.Time{})) {
		t.Fatalf("the late entry was kept at %v, not after the claim at %v", late.KeptAt, reclaimed.DiscardClaimedAt)
	}
	for name, statement := range map[string]string{
		"answered discard without the removal": `UPDATE kept_work SET answer = 'discard', answered_by = discard_by, answered_at = discard_claimed_at WHERE id = ?`,
		"the claim withdrawn in part":          `UPDATE kept_work SET discard_by = NULL WHERE id = ?`,
		"the claim rewritten":                  `UPDATE kept_work SET discard_claimed_at = '2020-01-01T00:00:00Z' WHERE id = ?`,
		"the digest rewritten":                 `UPDATE kept_work SET discard_digest = 'rogue' WHERE id = ?`,
		"discarded with no answer":             `UPDATE kept_work SET discarded_at = '2020-01-01T00:00:00Z' WHERE id = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, statement, entry.ID); err == nil {
			t.Errorf("the schema let %s be written", name)
		}
	}
	if _, err := s.RecordKeptWorkDiscard(ctx, entry.ID, []int64{sibling.ID}); !errors.Is(err, ErrKeptWorkUnverified) {
		t.Fatalf("recording with the late entry unverified = %v; want ErrKeptWorkUnverified", err)
	}
	if got, err := s.KeptWorkEntry(ctx, entry.ID); err != nil || got.DiscardedAt.IsKnown() {
		t.Fatalf("the refused record wrote %+v, %v", got, err)
	}
	done, err := s.RecordKeptWorkDiscard(ctx, entry.ID, []int64{sibling.ID, late.ID})
	if err != nil || done.Answer.Or("") != KeptDiscard || done.AnsweredBy.Or(Resolver{}) != ResolvedByPerson() ||
		!done.AnsweredAt.Or(time.Time{}).Equal(reclaimed.DiscardClaimedAt.Or(time.Time{})) || !done.DiscardedAt.IsKnown() ||
		done.DiscardDigest.Or("") != "digest-shown" || !slices.Equal(done.DiscardLacks, []string{"aaaa", "bbbb"}) {
		t.Fatalf("recording the discard = %+v, %v; want it answered discard from the claim", done, err)
	}
	if _, err := s.RecordKeptWorkDiscard(ctx, entry.ID, nil); !errors.Is(err, ErrKeptWorkDiscarded) {
		t.Fatalf("recording it again = %v; want ErrKeptWorkDiscarded", err)
	}
	if got, err := s.KeptWorkEntry(ctx, sibling.ID); err != nil || got.Answer.Or("") != KeptDiscard ||
		got.DiscardDigest.Or("") != "digest-shown" || !got.DiscardedAt.IsKnown() {
		t.Fatalf("the copy's entry kept before the claim reads %+v, %v; want it discarded with the claim", got, err)
	}
	if got, err := s.KeptWorkEntry(ctx, late.ID); err != nil || got.Answer.Or("") != KeptDiscard ||
		got.DiscardDigest.Or("") != "digest-shown" || !got.DiscardedAt.IsKnown() {
		t.Fatalf("the copy's entry kept after the claim reads %+v, %v; want it discarded with the claim", got, err)
	}
	if got, err := s.KeptWorkEntry(ctx, elsewhere.ID); err != nil || !got.Open() || got.DiscardClaimedAt.IsKnown() {
		t.Fatalf("another copy's entry reads %+v, %v; want it open, not taken by a discard of another copy", got, err)
	}
	if owed, err := s.KeptWorkDiscardsOwed(ctx); err != nil || len(owed) != 0 {
		t.Fatalf("KeptWorkDiscardsOwed = %+v, %v; want none once discarded", owed, err)
	}
	if _, err := s.WithdrawKeptWorkDiscard(ctx, entry.ID); !errors.Is(err, ErrKeptWorkAnswered) {
		t.Fatalf("withdrawing an answered discard = %v; want ErrKeptWorkAnswered", err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET discard_claimed_at = '2020-01-01T00:00:00Z' WHERE id = ?`, entry.ID); err == nil {
		t.Fatal("the schema let the discard claim be rewritten")
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET discarded_at = '2020-01-01T00:00:00Z' WHERE id = ?`, entry.ID); err == nil {
		t.Fatal("the schema let the discarded instant be rewritten")
	}
	landKept(t, s, elsewhere.ID)
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET discard_claimed_at = '2020-01-01T00:00:00Z', discard_by = 'person',
		discard_digest = 'rogue', discard_lacks = '[]' WHERE id = ?`, elsewhere.ID); err == nil {
		t.Fatal("the schema let a discard claim be written on an entry answered land")
	}
}
