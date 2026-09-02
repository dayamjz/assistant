package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// extend returns the shipped schema with extra migrations appended, without
// touching the package-level list.
func extend(extra ...migration) []migration {
	out := make([]migration, 0, len(schema)+len(extra))
	out = append(out, schema...)
	out = append(out, extra...)
	return out
}

// addColumn builds a migration that adds one column to the run table.
func addColumn(version int, name, declaration string) migration {
	return migration{
		version:    version,
		name:       name,
		statements: []string{`ALTER TABLE run ADD COLUMN ` + declaration},
	}
}

// runNotes reads the value of a column this package's own types do not carry,
// which is how the tests observe what a later migration's column holds for a
// row written before it existed.
func runNotes(t *testing.T, s *Store, runID string) Optional[string] {
	t.Helper()
	var notes Optional[string]
	if err := s.read.QueryRowContext(context.Background(),
		`SELECT notes FROM run WHERE id = ?`, runID).Scan(&notes); err != nil {
		t.Fatalf("reading run.notes: %v", err)
	}
	return notes
}

func TestMigrationPreservesRowsAndAddsUnknownColumn(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if _, err := s.AppendRound(ctx, Round{RunID: run.ID, Stage: "review", Number: 1, Summary: "first"}); err != nil {
		t.Fatalf("AppendRound: %v", err)
	}

	if err := migrate(ctx, s.write, extend(addColumn(2, "run notes", "notes TEXT"))); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The populated rows survived, field for field.
	after, err := s.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run after migrating: %v", err)
	}
	if after.Branch != run.Branch || after.SubmittedHead != run.SubmittedHead ||
		after.Build != run.Build || after.ConfigDigest != run.ConfigDigest {
		t.Fatalf("the migration changed the run: before %+v, after %+v", run, after)
	}
	rounds, err := s.Rounds(ctx, run.ID)
	if err != nil {
		t.Fatalf("Rounds after migrating: %v", err)
	}
	if len(rounds) != 1 || rounds[0].Summary != "first" {
		t.Fatalf("the migration changed the rounds: %+v", rounds)
	}

	// The column the migration added reads back as unknown, not as "".
	notes := runNotes(t, s, run.ID)
	if notes.IsKnown() {
		t.Fatalf("run.notes reads back as known %q for a row written before the column existed", notes)
	}
	if notes.String() != "unknown" {
		t.Fatalf("an unknown Optional rendered as %q", notes.String())
	}

	// A row written after the migration can record a genuine empty string, and
	// that is a different fact from the unknown above.
	if _, err := s.write.ExecContext(ctx, `UPDATE run SET notes = '' WHERE id = ?`, run.ID); err != nil {
		t.Fatalf("writing an empty note: %v", err)
	}
	notes = runNotes(t, s, run.ID)
	value, known := notes.Get()
	if !known || value != "" {
		t.Fatalf("a genuinely empty note read back as %v (known=%v), so it is not distinguishable from unknown", value, known)
	}
}

func TestMigrationRefusesNonAdditiveChanges(t *testing.T) {
	cases := []struct {
		name string
		m    migration
		want string
	}{
		{
			// sqlite accepts a NOT NULL column when it carries a default, so
			// this reaches the check rather than being refused by the engine.
			name: "not null with a default",
			m:    addColumn(2, "not null", `notes TEXT NOT NULL DEFAULT ''`),
			want: "NOT NULL",
		},
		{
			name: "nullable with a default",
			m:    addColumn(2, "default", `notes TEXT DEFAULT 'none'`),
			want: "default",
		},
		{
			name: "dropping a column",
			m: migration{version: 2, name: "drop", statements: []string{
				`ALTER TABLE run DROP COLUMN intent_source`,
			}},
			want: "removes column run.intent_source",
		},
		{
			name: "dropping a table",
			m:    migration{version: 2, name: "drop table", statements: []string{`DROP TABLE round`}},
			want: "removes table round",
		},
		{
			name: "renaming a column",
			m: migration{version: 2, name: "rename", statements: []string{
				`ALTER TABLE run RENAME COLUMN intent TO purpose`,
			}},
			want: "removes column run.intent",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := openStore(t)
			run := seedRun(t, s)

			err := migrate(ctx, s.write, extend(tc.m))
			if !errors.Is(err, ErrNotAdditive) {
				t.Fatalf("migrate accepted a non-additive migration: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the refusal does not say what it refused: %v", err)
			}
			var migrationErr *MigrationError
			if !errors.As(err, &migrationErr) || migrationErr.Version != 2 {
				t.Fatalf("the refusal does not name the migration: %v", err)
			}

			// The refusal rolled the whole migration back, so the schema and
			// the rows are exactly what they were.
			if _, err := s.Run(ctx, run.ID); err != nil {
				t.Fatalf("the refused migration disturbed the run: %v", err)
			}
			if _, err := s.read.ExecContext(ctx, `SELECT intent_source FROM run`); err != nil {
				t.Fatalf("the refused migration disturbed the schema: %v", err)
			}
			applied, err := appliedMigrations(ctx, s.write)
			if err != nil {
				t.Fatalf("appliedMigrations: %v", err)
			}
			if _, ok := applied[2]; ok {
				t.Fatal("the refused migration was recorded as applied")
			}
		})
	}
}

// A guard that refuses everything is not a guard. This is the accepting path
// for the same check the refusals above exercise.
func TestMigrationAcceptsAnAdditiveChange(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	list := extend(
		addColumn(2, "run notes", "notes TEXT"),
		migration{version: 3, name: "new table", statements: []string{
			// A table the migration creates has no old rows, so NOT NULL and a
			// default are both fine on it.
			`CREATE TABLE note (id TEXT PRIMARY KEY, body TEXT NOT NULL DEFAULT '') STRICT`,
		}},
	)
	if err := migrate(ctx, s.write, list); err != nil {
		t.Fatalf("migrate refused an additive migration: %v", err)
	}
	applied, err := appliedMigrations(ctx, s.write)
	if err != nil {
		t.Fatalf("appliedMigrations: %v", err)
	}
	if len(applied) != 3 {
		t.Fatalf("applied %d migrations, want 3", len(applied))
	}
}

func TestMigrateTwiceIsANoOp(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	list := extend(addColumn(2, "run notes", "notes TEXT"))
	if err := migrate(ctx, s.write, list); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	before := schemaSnapshot(t, s)
	appliedBefore, err := appliedMigrations(ctx, s.write)
	if err != nil {
		t.Fatalf("appliedMigrations: %v", err)
	}

	if err := migrate(ctx, s.write, list); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if got := schemaSnapshot(t, s); got != before {
		t.Fatalf("the second migrate changed the schema:\nbefore %s\nafter  %s", before, got)
	}
	appliedAfter, err := appliedMigrations(ctx, s.write)
	if err != nil {
		t.Fatalf("appliedMigrations: %v", err)
	}
	if len(appliedAfter) != len(appliedBefore) {
		t.Fatalf("the second migrate recorded %d migrations, want %d", len(appliedAfter), len(appliedBefore))
	}
	if _, err := s.Run(ctx, run.ID); err != nil {
		t.Fatalf("the second migrate disturbed the run: %v", err)
	}
}

// schemaSnapshot renders the whole schema as text so two states can be compared
// as one value.
func schemaSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.read.QueryContext(context.Background(),
		`SELECT type, name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var b strings.Builder
	for rows.Next() {
		var kind, name, sql string
		if err := rows.Scan(&kind, &name, &sql); err != nil {
			t.Fatalf("reading the schema: %v", err)
		}
		b.WriteString(kind + " " + name + ": " + sql + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	return b.String()
}

func TestInterruptedMigrationReopensAndResumes(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	broken := extend(
		addColumn(2, "run notes", "notes TEXT"),
		migration{version: 3, name: "broken", statements: []string{`ALTER TABLE run ADD COLUMN`}},
	)
	err := migrate(ctx, s.write, broken)
	if err == nil {
		t.Fatal("migrate accepted a migration with a broken statement")
	}
	var migrationErr *MigrationError
	if !errors.As(err, &migrationErr) || migrationErr.Version != 3 {
		t.Fatalf("the failure does not name the migration that failed: %v", err)
	}

	// Version 2 committed before version 3 failed, and version 3 recorded
	// nothing, so the database is at a version rather than between two.
	applied, err := appliedMigrations(ctx, s.write)
	if err != nil {
		t.Fatalf("appliedMigrations: %v", err)
	}
	if _, ok := applied[2]; !ok {
		t.Fatal("version 2 committed before version 3 failed, but is not recorded")
	}
	if _, ok := applied[3]; ok {
		t.Fatal("the failed migration was recorded as applied")
	}

	// The database still opens, and migrating again with the statement fixed
	// picks up where it stopped without disturbing what was already there.
	fixed := extend(
		addColumn(2, "run notes", "notes TEXT"),
		addColumn(3, "broken", "extra TEXT"),
	)
	if err := migrate(ctx, s.write, fixed); err != nil {
		t.Fatalf("migrate after the interruption: %v", err)
	}
	if _, err := s.Run(ctx, run.ID); err != nil {
		t.Fatalf("the run did not survive the interruption: %v", err)
	}
	if notes := runNotes(t, s, run.ID); notes.IsKnown() {
		t.Fatalf("run.notes reads back as known %q after the interruption", notes)
	}
}

func TestOpenRefusesADatabaseAheadOfThisBuild(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/state.db"
	s := openStoreAt(t, path)
	if err := migrate(ctx, s.write, extend(addColumn(2, "run notes", "notes TEXT"))); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := Open(ctx, path, WithRedactor(workingRedactor()))
	if !errors.Is(err, ErrSchemaAhead) {
		t.Fatalf("Open accepted a database migrated by a newer build: %v", err)
	}
}

func TestMigrateRefusesAnEditedMigration(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	edited := make([]migration, len(schema))
	copy(edited, schema)
	edited[0].name = schema[0].name + " with one more thing"

	err := migrate(ctx, s.write, edited)
	if !errors.Is(err, ErrSchemaChanged) {
		t.Fatalf("migrate accepted an edited migration: %v", err)
	}
}

func TestMigrateRefusesAMalformedList(t *testing.T) {
	cases := map[string][]migration{
		"a gap in the versions": {schema[0], addColumn(3, "gap", "notes TEXT")},
		"a version out of order": {
			addColumn(2, "second first", "notes TEXT"), schema[0],
		},
		"a migration with no name":       {{version: 1, statements: []string{`SELECT 1`}}},
		"a migration with no statements": {{version: 1, name: "empty"}},
	}
	for name, list := range cases {
		t.Run(name, func(t *testing.T) {
			s := openStore(t)
			if err := migrate(context.Background(), s.write, list); !errors.Is(err, ErrMigrationsMalformed) {
				t.Fatalf("migrate accepted a malformed list: %v", err)
			}
		})
	}
}

// The shipped list has to satisfy the same rule the tests impose on invented
// ones, so a bad migration cannot ship by never being exercised here.
func TestShippedSchemaIsWellFormed(t *testing.T) {
	if err := checkMigrationList(schema); err != nil {
		t.Fatalf("the shipped migration list is malformed: %v", err)
	}
}
