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
// Seeding the schema-72 database uses SQL rather than RecordKeptWork: the
// current keptWorkColumns name columns migration 74 adds.
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
	at := encodeTime(nowUTC())
	res, err := older.ExecContext(ctx, `INSERT INTO kept_work (run_id, repository_id, branch, copy_path, holds, reason, detail, kept_at, lacks, against)
		VALUES (?, ?, ?, '/worktrees/widen-a', ?, 'unproven', '', ?, '[]', 'mmmm')`,
		run.ID, run.RepositoryID, run.Branch, `["aaaa"]`, at)
	if err != nil {
		t.Fatal(err)
	}
	entryID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	res, err = older.ExecContext(ctx, `INSERT INTO kept_work (run_id, repository_id, branch, copy_path, holds, reason, detail, kept_at, lacks, against,
		landing_claimed_at, landing_by, answer, answered_by, answered_at, landed_run)
		VALUES (?, ?, ?, '/worktrees/widen-b', ?, 'unproven', '', ?, '[]', 'mmmm', ?, 'person', 'land', 'person', ?, 'run-landed')`,
		run.ID, run.RepositoryID, run.Branch, `["bbbb"]`, at, at, at)
	if err != nil {
		t.Fatal(err)
	}
	landedID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	inc := incarnationOf(t, s)
	wakeRes, err := older.ExecContext(ctx, `INSERT INTO kept_work_wake (kept_work, kind, marker, created_at)
		VALUES (?, 'kept-work', 'kept-work:widen', ?)`, entryID, at)
	if err != nil {
		t.Fatal(err)
	}
	wakeID, err := wakeRes.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := older.ExecContext(ctx, `UPDATE kept_work SET surfaced_at = ? WHERE id = ?`, at, entryID); err != nil {
		t.Fatal(err)
	}
	_ = inc
	_ = wakeID
	_, err = older.ExecContext(ctx, `UPDATE kept_work SET answer = 'remove-anyway', answered_by = 'person', answered_at = ? WHERE id = ?`,
		encodeTime(nowUTC()), entryID)
	if err == nil || !strings.Contains(strings.ToUpper(err.Error()), "CHECK") {
		t.Fatalf("answer remove-anyway before widen = %v; want a CHECK refusal", err)
	}
	if err := older.Close(); err != nil {
		t.Fatalf("close the schema-72 database: %v", err)
	}

	opened := openStoreAt(t, path)
	got, err := opened.KeptWorkEntry(ctx, entryID)
	if err != nil || !got.Open() || got.CopyPath != "/worktrees/widen-a" {
		t.Fatalf("after widen the open entry reads %+v, %v; want it as it was", got, err)
	}
	landedGot, err := opened.KeptWorkEntry(ctx, landedID)
	if err != nil || landedGot.Answer.Or("") != KeptLand {
		t.Fatalf("after widen the landed entry reads %+v, %v; want land", landedGot, err)
	}
	wakes, err := opened.KeptWorkWakes(ctx)
	if err != nil || len(wakes) == 0 || wakes[0].KeptWork != entryID {
		t.Fatalf("after widen wakes = %+v, %v; want the wake naming entry %d", wakes, err, entryID)
	}
	// Seals on kept_work must stand after the rebuild: DROP TABLE removes
	// them, and a recreate that was skipped would still pass row/wake checks.
	if _, err := opened.write.ExecContext(ctx, `UPDATE kept_work SET holds = '[]' WHERE id = ?`, entryID); err == nil ||
		!strings.Contains(err.Error(), "written once") {
		t.Fatalf("rewrite holds after widen = %v; want the written-once seal", err)
	}
	if _, err := opened.write.ExecContext(ctx, `UPDATE kept_work SET answer = 'nope', answered_by = 'person', answered_at = ? WHERE id = ?`,
		encodeTime(nowUTC()), entryID); err == nil || !strings.Contains(strings.ToUpper(err.Error()), "CHECK") {
		t.Fatalf("answer nope after widen = %v; want a CHECK refusal", err)
	}
	// After migration 74 the remove-anyway answer needs its claim columns;
	// the CHECK itself is what link 1 widened.
	claimed, err := opened.ClaimKeptWorkRemoveAnyway(ctx, entryID, "discard refused: shallow", []string{"aaaa"}, ResolvedByPerson())
	if err != nil {
		t.Fatalf("claim remove-anyway after widen = %v; want it admitted", err)
	}
	done, err := opened.RecordKeptWorkRemoveAnyway(ctx, entryID, nil)
	if err != nil || done.Answer.Or("") != KeptRemoveAnyway || !done.RemovedAnywayAt.IsKnown() {
		t.Fatalf("record remove-anyway after widen = %+v, %v; want it released", done, err)
	}
	if _, err := opened.write.ExecContext(ctx, `UPDATE kept_work SET answered_by = 'other' WHERE id = ?`, claimed.ID); err == nil ||
		!strings.Contains(err.Error(), "answered once") {
		t.Fatalf("rewrite answered_by after widen = %v; want the answered-once seal", err)
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
