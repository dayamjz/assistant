package store

import (
	"context"
	"strings"
	"testing"
)

// An entry states its basis, PRD section 8's "what it holds that the default
// branch lacks": either the default branch tip its commits were computed
// against or why they could not be, never both and never neither. The
// accessor refuses either mistake, the schema refuses it from a direct write
// too, the basis is sealed with the rest of the entry, and the account of an
// unestablished set passes the redactor. Each refusal is shown against the
// real schema.
func TestAKeptEntryStatesWhatItsCommitsWereComputedAgainst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	for name, k := range map[string]KeptCopy{
		"neither": {RunID: run.ID, CopyPath: "/w/neither", Holds: []string{"aaaa"}, Reason: "unproven"},
		"both":    {RunID: run.ID, CopyPath: "/w/both", Holds: []string{"aaaa"}, Reason: "unproven", Against: "dddd", Unestablished: "unread"},
	} {
		if _, _, err := s.RecordKeptWork(ctx, k); err == nil {
			t.Errorf("%s: RecordKeptWork wrote an entry stating %s as its basis", name, name)
		}
	}
	for name, values := range map[string][2]any{"neither": {nil, nil}, "both": {"dddd", "unread"}, "an empty tip": {"", nil}} {
		_, err := s.write.ExecContext(ctx, `INSERT INTO kept_work (run_id, copy_path, holds, reason, detail, kept_at, against, unestablished)
			VALUES (?, ?, '[]', 'unproven', '', ?, ?, ?)`, run.ID, "/w/direct-"+name, encodeTime(nowUTC()), values[0], values[1])
		if err == nil || !strings.Contains(err.Error(), "kept work entry") {
			t.Errorf("a direct write stating %s was not refused by the schema: %v", name, err)
		}
	}

	established, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/established",
		Holds: []string{"aaaa"}, Reason: "unproven", Against: "dddd"})
	if err != nil || !established.Established() || established.Against.Or("") != "dddd" || established.Unestablished.IsKnown() {
		t.Fatalf("an established entry reads %+v, %v", established, err)
	}
	unestablished, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/unestablished",
		Holds: []string{"aaaa"}, Reason: "unproven", Unestablished: "https://user:secret@example.test/one.git could not be read"})
	if err != nil || unestablished.Established() || unestablished.Against.IsKnown() || !unestablished.Unestablished.IsKnown() {
		t.Fatalf("an unestablished entry reads %+v, %v", unestablished, err)
	}
	if strings.Contains(unestablished.Unestablished.Or(""), "secret") {
		t.Fatalf("the account %q kept a credential", unestablished.Unestablished.Or(""))
	}
	for name, write := range map[string]struct {
		statement string
		id        int64
	}{
		"a rewritten tip":     {`UPDATE kept_work SET against = 'eeee' WHERE id = ?`, established.ID},
		"a basis swapped":     {`UPDATE kept_work SET against = NULL, unestablished = 'later' WHERE id = ?`, established.ID},
		"an account replaced": {`UPDATE kept_work SET unestablished = 'other' WHERE id = ?`, unestablished.ID},
	} {
		if _, err := s.write.ExecContext(ctx, write.statement, write.id); err == nil || !strings.Contains(err.Error(), "kept work entry") {
			t.Errorf("%s was not refused by the seal: %v", name, err)
		}
	}
}

// An entry written before the store recorded a basis reads back with
// neither, and so as unestablished, which is what it was: its holds were
// the commits the copy's references stood on, computed against nothing. The
// basis migration is rolled back by hand, an entry is written the way the
// earlier build wrote it, and the shipped migration is applied over it.
func TestAKeptEntryFromBeforeItsBasisReadsUnestablished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	var basis migration
	for _, m := range schema {
		if m.name == keptWorkAgainstMigration {
			basis = m
		}
	}
	if basis.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", keptWorkAgainstMigration)
	}
	// Back to the shape the kept work migration left. A migration may not
	// do this, which is why it is done here by hand and not through one.
	for _, stmt := range []string{
		`DROP TRIGGER kept_work_states_its_basis`, `DROP TRIGGER kept_work_basis_is_written_once`,
		`ALTER TABLE kept_work DROP COLUMN against`, `ALTER TABLE kept_work DROP COLUMN unestablished`,
	} {
		if _, err := s.write.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("rolling the schema back: %s: %v", stmt, err)
		}
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM schema_migration WHERE version = ?`, basis.version); err != nil {
		t.Fatalf("forgetting the migration: %v", err)
	}
	if _, err := s.write.ExecContext(ctx, `INSERT INTO kept_work (run_id, repository_id, branch, copy_path, holds, reason, detail, kept_at)
		VALUES (?, 'repo-1', 'topic', '/w/old', '["aaaa","bbbb"]', 'unproven', '', ?)`, run.ID, encodeTime(nowUTC())); err != nil {
		t.Fatalf("writing an entry the way an earlier build did: %v", err)
	}
	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("applying the shipped migration over the old entry: %v", err)
	}
	open, err := s.OpenKeptWork(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("OpenKeptWork = %+v, %v; want the old entry", open, err)
	}
	if old := open[0]; old.Established() || old.Against.IsKnown() || old.Unestablished.IsKnown() {
		t.Fatalf("the old entry reads %+v, want neither basis and so unestablished", old)
	}
	if _, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/new", Reason: "unproven"}); err == nil {
		t.Fatal("after the migration an entry stating no basis was written")
	}
}
