package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// An entry states its basis, PRD section 8's "what it holds that the default
// branch lacks": either the default branch tip its set was computed against,
// with the set, or why it could not be, never both and never neither. The
// accessor refuses either mistake, the schema refuses it from a direct write
// too, and the account of an unestablished set passes the redactor. The
// basis is no part of the entry's identity: the same copy state recorded
// again with another basis is the same entry with the basis it has.
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
	for name, values := range map[string][3]any{
		"neither":             {nil, nil, nil},
		"both":                {"[]", "dddd", "unread"},
		"an empty tip":        {"[]", "", nil},
		"a tip with no set":   {nil, "dddd", nil},
		"a set with no tip":   {"[]", nil, "unread"},
		"an empty account":    {nil, nil, ""},
		"a set beside an acc": {`["aaaa"]`, nil, "unread"},
	} {
		_, err := s.write.ExecContext(ctx, `INSERT INTO kept_work (run_id, copy_path, holds, reason, detail, kept_at, lacks, against, unestablished)
			VALUES (?, ?, '[]', 'unproven', '', ?, ?, ?, ?)`, run.ID, "/w/direct-"+name, encodeTime(nowUTC()), values[0], values[1], values[2])
		if err == nil || !strings.Contains(err.Error(), "kept work entry") {
			t.Errorf("a direct write stating %s was not refused by the schema: %v", name, err)
		}
	}

	established, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/established",
		Holds: []string{"aaaa", "bbbb"}, Lacks: []string{"bbbb"}, Reason: "unproven", Against: "dddd"})
	if err != nil || !established.Established() || established.Against.Or("") != "dddd" || established.Unestablished.IsKnown() ||
		!slices.Equal(established.Lacks, []string{"bbbb"}) {
		t.Fatalf("an established entry reads %+v, %v", established, err)
	}
	nothing, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/nothing",
		Holds: []string{"aaaa"}, Reason: "copy-dirty", Against: "dddd"})
	if err != nil || nothing.Lacks == nil || len(nothing.Lacks) != 0 {
		t.Fatalf("an established entry lacking nothing reads %+v, %v; want an empty list and not an unknown one", nothing, err)
	}
	unestablished, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/unestablished",
		Holds: []string{"aaaa"}, Reason: "unproven", Unestablished: "https://user:secret@example.test/one.git could not be read"})
	if err != nil || unestablished.Established() || unestablished.Against.IsKnown() || unestablished.Lacks != nil ||
		!unestablished.Unestablished.IsKnown() {
		t.Fatalf("an unestablished entry reads %+v, %v", unestablished, err)
	}
	if strings.Contains(unestablished.Unestablished.Or(""), "secret") {
		t.Fatalf("the account %q kept a credential", unestablished.Unestablished.Or(""))
	}
	same, wrote, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/unestablished",
		Holds: []string{"aaaa"}, Lacks: []string{"aaaa"}, Reason: "unproven", Against: "dddd"})
	if err != nil || wrote || same.ID != unestablished.ID || same.Established() {
		t.Fatalf("the same copy state with another basis = %+v, wrote %v, %v; want the entry as it stands", same, wrote, err)
	}
}

// An entry's basis is established once, from unestablished, only while the
// entry is open, and one way: EstablishKeptWork fills in the set and the
// tip of an entry written unestablished and keeps the account, refuses an
// entry already established (the first basis stands), an empty tip, and an
// unknown entry, and the seal refuses every other move against the real
// schema: a tip rewritten, a set rewritten, a basis taken back, and an
// account replaced. An entry a person answered or the program closed is
// sealed whole, PRD section 8's "its one revision is the answer": the
// accessor refuses it with the typed refusal, and the seal refuses the same
// write made directly, so a basis cannot be written past an answer by any
// path.
func TestAKeptEntryIsEstablishedOnceFromUnestablished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/later",
		Holds: []string{"aaaa", "bbbb"}, Reason: "unproven", Unestablished: "the upstream could not be read"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EstablishKeptWork(ctx, entry.ID, "", []string{"bbbb"}); err == nil {
		t.Fatal("an empty tip established the entry")
	}
	if _, err := s.EstablishKeptWork(ctx, 999, "dddd", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("establishing an unknown entry = %v; want ErrNotFound", err)
	}
	got, err := s.EstablishKeptWork(ctx, entry.ID, "dddd", []string{"bbbb"})
	if err != nil || !got.Established() || got.Against.Or("") != "dddd" || !slices.Equal(got.Lacks, []string{"bbbb"}) ||
		got.Unestablished.Or("") != "the upstream could not be read" {
		t.Fatalf("EstablishKeptWork = %+v, %v; want the set and the tip with the account kept", got, err)
	}
	if _, err := s.EstablishKeptWork(ctx, entry.ID, "eeee", nil); !errors.Is(err, ErrKeptWorkEstablished) {
		t.Fatalf("a second establishment = %v; want ErrKeptWorkEstablished", err)
	}
	if again, err := s.KeptWorkEntry(ctx, entry.ID); err != nil || again.Against.Or("") != "dddd" {
		t.Fatalf("after a refused second establishment the entry reads %+v, %v; want the first", again, err)
	}
	for name, statement := range map[string]string{
		"a rewritten tip":      `UPDATE kept_work SET against = 'eeee' WHERE id = ?`,
		"a rewritten set":      `UPDATE kept_work SET lacks = '[]' WHERE id = ?`,
		"a basis taken back":   `UPDATE kept_work SET against = NULL, lacks = NULL WHERE id = ?`,
		"an account replaced":  `UPDATE kept_work SET unestablished = 'other' WHERE id = ?`,
		"an account taken off": `UPDATE kept_work SET unestablished = NULL WHERE id = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, statement, entry.ID); err == nil || !strings.Contains(err.Error(), "kept work entry") {
			t.Errorf("%s was not refused by the seal: %v", name, err)
		}
	}
	open, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/open",
		Holds: []string{"cccc"}, Reason: "unproven", Unestablished: "unread"})
	if err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"a tip without a set":    `UPDATE kept_work SET against = 'dddd' WHERE id = ?`,
		"a set without a tip":    `UPDATE kept_work SET lacks = '[]' WHERE id = ?`,
		"an account swapped too": `UPDATE kept_work SET against = 'dddd', lacks = '[]', unestablished = 'other' WHERE id = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, statement, open.ID); err == nil || !strings.Contains(err.Error(), "kept work entry") {
			t.Errorf("%s was not refused by the seal: %v", name, err)
		}
	}
	if _, err := s.AnswerKeptWorkCarryOn(ctx, open.ID, "taskcarrying", ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EstablishKeptWork(ctx, open.ID, "dddd", []string{"cccc"}); !errors.Is(err, ErrKeptWorkAnswered) {
		t.Fatalf("establishing an answered entry = %v; want ErrKeptWorkAnswered", err)
	}
	closed, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/closed",
		Holds: []string{"eeee"}, Reason: "unproven", Unestablished: "unread"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run.ID, Proof: Known(CleanupProofRemoteBranch), Examined: []string{"eeee"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRunCleanupRemoved(ctx, run.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EstablishKeptWork(ctx, closed.ID, "dddd", []string{"eeee"}); !errors.Is(err, ErrKeptWorkClosed) {
		t.Fatalf("establishing a closed entry = %v; want ErrKeptWorkClosed", err)
	}
	for name, id := range map[string]int64{"an answered entry": open.ID, "a closed entry": closed.ID} {
		if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET against = 'dddd', lacks = '[]' WHERE id = ?`, id); err == nil ||
			!strings.Contains(err.Error(), "kept work entry") {
			t.Errorf("establishing %s directly was not refused by the seal: %v", name, err)
		}
	}
	for name, id := range map[string]int64{"the answered entry": open.ID, "the closed entry": closed.ID} {
		if got, err := s.KeptWorkEntry(ctx, id); err != nil || got.Established() {
			t.Errorf("%s reads %+v, %v; want it still unestablished", name, got, err)
		}
	}
}

// KeptWorkOf is the read of what RecordKeptWork would find: the entry
// standing for a copy state, whatever its answer or closure, keyed as the
// write keys it, and ErrNotFound for a state no entry records.
func TestKeptWorkOfReadsTheEntryStandingForACopyState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if _, err := s.KeptWorkOf(ctx, "/w/none", []string{"aaaa"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("KeptWorkOf with no entry = %v; want ErrNotFound", err)
	}
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/of", Holds: []string{"aaaa", "bbbb"},
		Reason: "unproven", Unestablished: "unread"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.KeptWorkOf(ctx, "/w/of", []string{"aaaa", "bbbb"}); err != nil || got.ID != entry.ID {
		t.Fatalf("KeptWorkOf = %+v, %v; want entry %d", got, err, entry.ID)
	}
	if _, err := s.KeptWorkOf(ctx, "/w/of", []string{"bbbb", "aaaa"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("KeptWorkOf with the commits in another order = %v; want ErrNotFound, as the write keys them", err)
	}
	if _, err := s.AnswerKeptWorkDiscard(ctx, entry.ID, "shown", nil, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.KeptWorkOf(ctx, "/w/of", []string{"aaaa", "bbbb"}); err != nil || got.ID != entry.ID || !got.Answered() {
		t.Fatalf("KeptWorkOf of an answered entry = %+v, %v; want it as it stands", got, err)
	}
}

// An entry written before the store recorded a basis reads back with
// neither, and so as unestablished, which is what it was: its holds were
// the commits the copy's references stood on, computed against nothing. The
// basis migration is rolled back by hand, an entry is written the way the
// earlier build wrote it, and the shipped migration is applied over it; such
// an entry can then be established like one that says why.
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
		`DROP TRIGGER kept_work_states_its_basis`, `DROP TRIGGER kept_work_basis_is_established_once`,
		`ALTER TABLE kept_work DROP COLUMN lacks`, `ALTER TABLE kept_work DROP COLUMN against`,
		`ALTER TABLE kept_work DROP COLUMN unestablished`,
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
	if old := open[0]; old.Established() || old.Against.IsKnown() || old.Unestablished.IsKnown() || old.Lacks != nil {
		t.Fatalf("the old entry reads %+v, want neither basis and so unestablished", old)
	}
	if _, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/new", Reason: "unproven"}); err == nil {
		t.Fatal("after the migration an entry stating no basis was written")
	}
	if got, err := s.EstablishKeptWork(ctx, open[0].ID, "dddd", []string{"bbbb"}); err != nil || !got.Established() {
		t.Fatalf("establishing the old entry = %+v, %v", got, err)
	}
}
