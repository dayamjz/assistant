package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// A remove-anyway is two facts. The claim records the discard refusal it
// overrides and what of the loss was shown, who answered on an open entry,
// once, and leaves it open, listed and owed. It can be withdrawn while
// unanswered. The answer is written only with the release of the entry,
// from the claim, once, and closes every other open entry of the copy the
// caller held it to; the copy still stands. The schema refuses an entry
// answered remove-anyway without the release, a claim written or withdrawn
// in part or under an answer, and a rewrite of either fact.
func TestARemoveAnywayIsClaimedAndThenAnsweredWithItsRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/remove-anyway-a", Holds: []string{"aaaa"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordKeptWorkRemoveAnyway(ctx, entry.ID, nil); !errors.Is(err, ErrKeptWorkNotRemoveAnyway) {
		t.Fatalf("a remove-anyway on an open entry with no claim = %v; want ErrKeptWorkNotRemoveAnyway", err)
	}
	sibling, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/remove-anyway-a", Holds: []string{"a222"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimKeptWorkRemoveAnyway(ctx, entry.ID, "", []string{"aaaa"}, ResolvedByPerson()); err == nil {
		t.Fatal("a remove-anyway claim naming no refusal was recorded")
	}
	if _, err := s.ClaimKeptWorkRemoveAnyway(ctx, entry.ID, "discard refused: shallow", []string{"aaaa"}, Resolver{}); !errors.Is(err, ErrNoResolver) {
		t.Fatalf("a claim by nobody = %v; want ErrNoResolver", err)
	}
	claimed, err := s.ClaimKeptWorkRemoveAnyway(ctx, entry.ID, "discard refused: shallow", []string{"aaaa", "bbbb"}, ResolvedByPerson())
	if err != nil || !claimed.RemoveAnywayClaimed() || !claimed.Open() || claimed.RemoveAnywayBy.Or(Resolver{}) != ResolvedByPerson() ||
		claimed.RemoveAnywayRefusal.Or("") != "discard refused: shallow" || !slices.Equal(claimed.RemoveAnywayShown, []string{"aaaa", "bbbb"}) ||
		claimed.RemovedAnywayAt.IsKnown() {
		t.Fatalf("claiming the remove-anyway = %+v, %v; want it open and claimed with the refusal and commits shown", claimed, err)
	}
	if open, err := s.OpenKeptWork(ctx); err != nil || !slices.ContainsFunc(open, func(k KeptWork) bool { return k.ID == entry.ID }) {
		t.Fatalf("OpenKeptWork = %+v, %v; want the claimed entry listed", open, err)
	}
	if owed, err := s.KeptWorkRemoveAnywaysOwed(ctx); err != nil || len(owed) != 1 || owed[0].ID != entry.ID {
		t.Fatalf("KeptWorkRemoveAnywaysOwed = %+v, %v; want the remove-anyway entry", owed, err)
	}
	if _, err := s.ClaimKeptWorkRemoveAnyway(ctx, entry.ID, "other", []string{"aaaa"}, ResolvedByPerson()); !errors.Is(err, ErrKeptWorkRemoveAnywayClaimed) ||
		!errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a second claim = %v; want ErrKeptWorkRemoveAnywayClaimed and ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, entry.ID, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a land on a claimed entry = %v; want ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, sibling.ID, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a land on another entry of the claimed copy = %v; want ErrKeptWorkCopyOwed", err)
	}
	if _, err := s.ClaimKeptWorkDiscard(ctx, sibling.ID, "digest", []string{"a222"}, ResolvedByPerson()); !errors.Is(err, ErrKeptWorkCopyOwed) {
		t.Fatalf("a discard of another entry of the claimed copy = %v; want ErrKeptWorkCopyOwed", err)
	}
	withdrawn, err := s.WithdrawKeptWorkRemoveAnyway(ctx, entry.ID)
	if err != nil || !withdrawn.Open() || withdrawn.RemoveAnywayClaimed() || withdrawn.RemoveAnywayBy.IsKnown() ||
		withdrawn.RemoveAnywayRefusal.IsKnown() || withdrawn.RemoveAnywayShown != nil {
		t.Fatalf("withdrawing the claim = %+v, %v; want the entry open with none", withdrawn, err)
	}
	if _, err := s.WithdrawKeptWorkRemoveAnyway(ctx, entry.ID); !errors.Is(err, ErrKeptWorkNotRemoveAnyway) {
		t.Fatalf("withdrawing no claim = %v; want ErrKeptWorkNotRemoveAnyway", err)
	}
	reclaimed, err := s.ClaimKeptWorkRemoveAnyway(ctx, entry.ID, "discard refused: shallow", []string{"aaaa", "bbbb"}, ResolvedByPerson())
	if err != nil {
		t.Fatal(err)
	}
	late, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/remove-anyway-a", Holds: []string{"a333"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/remove-anyway-b", Holds: []string{"b444"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if !late.KeptAt.After(reclaimed.RemoveAnywayClaimedAt.Or(time.Time{})) {
		t.Fatalf("the late entry was kept at %v, not after the claim at %v", late.KeptAt, reclaimed.RemoveAnywayClaimedAt)
	}
	for name, statement := range map[string]string{
		"answered remove-anyway without the release": `UPDATE kept_work SET answer = 'remove-anyway', answered_by = remove_anyway_by, answered_at = remove_anyway_claimed_at WHERE id = ?`,
		"the claim withdrawn in part":                `UPDATE kept_work SET remove_anyway_by = NULL WHERE id = ?`,
		"the claim rewritten":                        `UPDATE kept_work SET remove_anyway_claimed_at = '2020-01-01T00:00:00Z' WHERE id = ?`,
		"the refusal rewritten":                      `UPDATE kept_work SET remove_anyway_refusal = 'rogue' WHERE id = ?`,
		"removed anyway with no answer":              `UPDATE kept_work SET removed_anyway_at = '2020-01-01T00:00:00Z' WHERE id = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, statement, entry.ID); err == nil {
			t.Errorf("the schema let %s be written", name)
		}
	}
	if _, err := s.RecordKeptWorkRemoveAnyway(ctx, entry.ID, []int64{sibling.ID}); !errors.Is(err, ErrKeptWorkUnverified) {
		t.Fatalf("recording with the late entry unverified = %v; want ErrKeptWorkUnverified", err)
	}
	if got, err := s.KeptWorkEntry(ctx, entry.ID); err != nil || got.RemovedAnywayAt.IsKnown() {
		t.Fatalf("the refused record wrote %+v, %v", got, err)
	}
	done, err := s.RecordKeptWorkRemoveAnyway(ctx, entry.ID, []int64{sibling.ID, late.ID})
	if err != nil || done.Answer.Or("") != KeptRemoveAnyway || done.AnsweredBy.Or(Resolver{}) != ResolvedByPerson() ||
		!done.AnsweredAt.Or(time.Time{}).Equal(reclaimed.RemoveAnywayClaimedAt.Or(time.Time{})) || !done.RemovedAnywayAt.IsKnown() ||
		done.RemoveAnywayRefusal.Or("") != "discard refused: shallow" || !slices.Equal(done.RemoveAnywayShown, []string{"aaaa", "bbbb"}) {
		t.Fatalf("recording the remove-anyway = %+v, %v; want it answered remove-anyway from the claim", done, err)
	}
	if _, err := s.RecordKeptWorkRemoveAnyway(ctx, entry.ID, nil); !errors.Is(err, ErrKeptWorkRemovedAnyway) {
		t.Fatalf("recording it again = %v; want ErrKeptWorkRemovedAnyway", err)
	}
	if got, err := s.KeptWorkEntry(ctx, sibling.ID); err != nil || got.Answer.Or("") != KeptRemoveAnyway ||
		got.RemoveAnywayRefusal.Or("") != "discard refused: shallow" || !got.RemovedAnywayAt.IsKnown() {
		t.Fatalf("the copy's entry kept before the claim reads %+v, %v; want it removed-anyway with the claim", got, err)
	}
	if got, err := s.KeptWorkEntry(ctx, late.ID); err != nil || got.Answer.Or("") != KeptRemoveAnyway ||
		got.RemoveAnywayRefusal.Or("") != "discard refused: shallow" || !got.RemovedAnywayAt.IsKnown() {
		t.Fatalf("the copy's entry kept after the claim reads %+v, %v; want it removed-anyway with the claim", got, err)
	}
	if got, err := s.KeptWorkEntry(ctx, elsewhere.ID); err != nil || !got.Open() || got.RemoveAnywayClaimedAt.IsKnown() {
		t.Fatalf("another copy's entry reads %+v, %v; want it open, not taken by a remove-anyway of another copy", got, err)
	}
	if owed, err := s.KeptWorkRemoveAnywaysOwed(ctx); err != nil || len(owed) != 0 {
		t.Fatalf("KeptWorkRemoveAnywaysOwed = %+v, %v; want none once released", owed, err)
	}
	if _, err := s.WithdrawKeptWorkRemoveAnyway(ctx, entry.ID); !errors.Is(err, ErrKeptWorkAnswered) {
		t.Fatalf("withdrawing an answered remove-anyway = %v; want ErrKeptWorkAnswered", err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET remove_anyway_claimed_at = '2020-01-01T00:00:00Z' WHERE id = ?`, entry.ID); err == nil {
		t.Fatal("the schema let the remove-anyway claim be rewritten")
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET removed_anyway_at = '2020-01-01T00:00:00Z' WHERE id = ?`, entry.ID); err == nil {
		t.Fatal("the schema let the removed-anyway instant be rewritten")
	}
	landKept(t, s, elsewhere.ID)
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET remove_anyway_claimed_at = '2020-01-01T00:00:00Z', remove_anyway_by = 'person',
		remove_anyway_refusal = 'rogue', remove_anyway_shown = '[]' WHERE id = ?`, elsewhere.ID); err == nil {
		t.Fatal("the schema let a remove-anyway claim be written on an entry answered land")
	}
}
