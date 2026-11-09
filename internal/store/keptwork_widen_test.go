package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// keptWorkAnswerCheckWidenVersion is the shipped migration that rebuilds
// kept_work so answer CHECK admits remove-anyway. A shipped version never
// moves, so pinning to it holds the tests below to their subject as the
// list grows.
const keptWorkAnswerCheckWidenVersion = 73

// Before the widen migration, answer remove-anyway fails the CHECK; after
// it, that value is admitted while land/carry-on/discard and a wake that
// names the entry still stand (the rebuild may not lose rows or break
// FKs). Without the rebuild a later remove-anyway answer cannot be written.
func TestKeptWorkAnswerCheckAdmitsRemoveAnywayAfterWiden(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	older, err := openPool(ctx, poolDSN(path, true), true)
	if err != nil {
		t.Fatalf("open the older database: %v", err)
	}
	if err := migrate(ctx, older, schemaBefore(t, keptWorkAnswerCheckWidenVersion)); err != nil {
		t.Fatalf("migrate to schema 72: %v", err)
	}
	s := &Store{write: older, read: older, redact: workingRedactor()}
	run := seedRun(t, s)
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{
		RunID: run.ID, CopyPath: "/worktrees/widen-a", Holds: []string{"aaaa"}, Reason: "unproven", Against: "mmmm",
	})
	if err != nil {
		t.Fatal(err)
	}
	landed, _, err := s.RecordKeptWork(ctx, KeptCopy{
		RunID: run.ID, CopyPath: "/worktrees/widen-b", Holds: []string{"bbbb"}, Reason: "unproven", Against: "mmmm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimKeptWorkLanding(ctx, landed.ID, ResolvedByPerson()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordKeptWorkLanding(ctx, landed.ID, "run-landed"); err != nil {
		t.Fatal(err)
	}
	inc := incarnationOf(t, s)
	wake, err := s.AppendKeptWorkWake(ctx, inc, KeptWorkWakeRequest{KeptWork: entry.ID, Kind: "kept-work", Marker: "kept-work:widen"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = older.ExecContext(ctx, `UPDATE kept_work SET answer = 'remove-anyway', answered_by = 'person', answered_at = ? WHERE id = ?`,
		encodeTime(nowUTC()), entry.ID)
	if err == nil || !strings.Contains(strings.ToUpper(err.Error()), "CHECK") {
		t.Fatalf("answer remove-anyway before widen = %v; want a CHECK refusal", err)
	}
	if err := older.Close(); err != nil {
		t.Fatalf("close the schema-72 database: %v", err)
	}

	opened := openStoreAt(t, path)
	got, err := opened.KeptWorkEntry(ctx, entry.ID)
	if err != nil || !got.Open() || got.CopyPath != entry.CopyPath {
		t.Fatalf("after widen the open entry reads %+v, %v; want it as it was", got, err)
	}
	landedGot, err := opened.KeptWorkEntry(ctx, landed.ID)
	if err != nil || landedGot.Answer.Or("") != KeptLand {
		t.Fatalf("after widen the landed entry reads %+v, %v; want land", landedGot, err)
	}
	wakes, err := opened.KeptWorkWakes(ctx)
	if err != nil || len(wakes) == 0 || wakes[0].KeptWork != entry.ID {
		t.Fatalf("after widen wakes = %+v, %v; want the wake naming entry %d (was %+v)", wakes, err, entry.ID, wake)
	}
	if _, err := opened.write.ExecContext(ctx, `UPDATE kept_work SET answer = 'nope', answered_by = 'person', answered_at = ? WHERE id = ?`,
		encodeTime(nowUTC()), entry.ID); err == nil || !strings.Contains(strings.ToUpper(err.Error()), "CHECK") {
		t.Fatalf("answer nope after widen = %v; want a CHECK refusal", err)
	}
	if _, err := opened.write.ExecContext(ctx, `UPDATE kept_work SET answer = 'remove-anyway', answered_by = 'person', answered_at = ? WHERE id = ?`,
		encodeTime(nowUTC()), entry.ID); err != nil {
		t.Fatalf("answer remove-anyway after widen = %v; want it admitted", err)
	}
}

// Discard claim/record still works on a database that has applied the widen.
func TestDiscardApisSurviveTheAnswerCheckWiden(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{
		RunID: run.ID, CopyPath: "/worktrees/widen-discard", Holds: []string{"cccc"}, Reason: "unproven", Against: "mmmm",
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimKeptWorkDiscard(ctx, entry.ID, "digest-shown", []string{"cccc"}, ResolvedByPerson())
	if err != nil || !claimed.DiscardClaimed() {
		t.Fatalf("claim after widen = %+v, %v", claimed, err)
	}
	done, err := s.RecordKeptWorkDiscard(ctx, entry.ID, nil)
	if err != nil || done.Answer.Or("") != KeptDiscard {
		t.Fatalf("record after widen = %+v, %v", done, err)
	}
}
