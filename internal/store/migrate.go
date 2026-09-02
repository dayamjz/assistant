package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// createMigrationTable records which migrations have been applied. It is the
// one statement executed outside a migration's own transaction, and it is
// idempotent, so a database interrupted at any point still opens.
const createMigrationTable = `CREATE TABLE IF NOT EXISTS schema_migration (
	version    INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	statements INTEGER NOT NULL,
	applied_at TEXT NOT NULL
) STRICT`

// columnFact is what this package checks a migration against. It is read from
// the database's own catalog rather than from the migration text, because the
// rule is about the shape rows end up with and a text check would pass on
// statements sqlite reads differently than a pattern does.
type columnFact struct {
	declaredType string
	notNull      bool
	defaultValue Optional[string]
}

// migrate brings db up to the last version in ms, applying each missing
// migration in one transaction together with the row recording it. It is
// idempotent: a migration already recorded is skipped, so a second call on an
// up-to-date database executes no schema statement at all.
//
// An interrupted call leaves the database at the last migration that committed.
// Because the schema statements and the row recording them commit together,
// there is no state in which a migration is half-applied, and a later call
// resumes at the same point.
func migrate(ctx context.Context, db *sql.DB, ms []migration) error {
	if err := checkMigrationList(ms); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, createMigrationTable); err != nil {
		return fmt.Errorf("store: creating the migration table: %w", err)
	}
	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return err
	}
	byVersion := make(map[int]migration, len(ms))
	for _, m := range ms {
		byVersion[m.version] = m
	}
	for version, record := range applied {
		known, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("%w: it has migration %d (%s), this build stops at %d",
				ErrSchemaAhead, version, record.name, lastVersion(ms))
		}
		if known.name != record.name || len(known.statements) != record.statements {
			return fmt.Errorf("%w: migration %d is recorded as %q with %d statements, this build has %q with %d",
				ErrSchemaChanged, version, record.name, record.statements, known.name, len(known.statements))
		}
	}
	for _, m := range ms {
		if _, ok := applied[m.version]; ok {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return &MigrationError{Version: m.version, Name: m.name, Err: err}
		}
	}
	return nil
}

// checkMigrationList refuses a list whose versions do not start at one and
// increase by one. Dense versions are what makes "recorded but unknown to this
// build" mean the database is ahead rather than the list having a hole.
func checkMigrationList(ms []migration) error {
	for i, m := range ms {
		if m.version != i+1 {
			return fmt.Errorf("%w: entry %d has version %d, expected %d", ErrMigrationsMalformed, i, m.version, i+1)
		}
		if m.name == "" {
			return fmt.Errorf("%w: migration %d has no name", ErrMigrationsMalformed, m.version)
		}
		if len(m.statements) == 0 {
			return fmt.Errorf("%w: migration %d has no statements", ErrMigrationsMalformed, m.version)
		}
	}
	return nil
}

func lastVersion(ms []migration) int {
	if len(ms) == 0 {
		return 0
	}
	return ms[len(ms)-1].version
}

// migrationRecord is one row of schema_migration.
type migrationRecord struct {
	name       string
	statements int
}

func appliedMigrations(ctx context.Context, db *sql.DB) (map[int]migrationRecord, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, name, statements FROM schema_migration`)
	if err != nil {
		return nil, fmt.Errorf("store: reading applied migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	applied := make(map[int]migrationRecord)
	for rows.Next() {
		var version, statements int
		var name string
		if err := rows.Scan(&version, &name, &statements); err != nil {
			return nil, fmt.Errorf("store: reading applied migrations: %w", err)
		}
		applied[version] = migrationRecord{name: name, statements: statements}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading applied migrations: %w", err)
	}
	return applied, nil
}

// applyMigration runs one migration and records it, in one transaction. The
// additive check runs inside that transaction against the catalog the
// statements just produced, so a migration that fails it changes nothing.
func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	before, err := columnFacts(ctx, tx)
	if err != nil {
		return err
	}
	for i, statement := range m.statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("statement %d: %w", i, err)
		}
	}
	after, err := columnFacts(ctx, tx)
	if err != nil {
		return err
	}
	if err := verifyAdditive(before, after); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migration (version, name, statements, applied_at) VALUES (?, ?, ?, ?)`,
		m.version, m.name, len(m.statements), encodeTime(nowUTC()),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// columnFacts reads every table's columns from the database's own catalog.
func columnFacts(ctx context.Context, tx *sql.Tx) (map[string]map[string]columnFact, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT m.name, ti.name, ti.type, ti."notnull", ti.dflt_value
		FROM sqlite_master AS m
		JOIN pragma_table_info(m.name) AS ti
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, fmt.Errorf("store: reading the table catalog: %w", err)
	}
	defer func() { _ = rows.Close() }()

	facts := make(map[string]map[string]columnFact)
	for rows.Next() {
		var table, column, declaredType string
		var notNull int
		var deflt Optional[string]
		if err := rows.Scan(&table, &column, &declaredType, &notNull, &deflt); err != nil {
			return nil, fmt.Errorf("store: reading the table catalog: %w", err)
		}
		if facts[table] == nil {
			facts[table] = make(map[string]columnFact)
		}
		facts[table][column] = columnFact{
			declaredType: strings.ToUpper(declaredType),
			notNull:      notNull != 0,
			defaultValue: deflt,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the table catalog: %w", err)
	}
	return facts, nil
}

// verifyAdditive holds the rule PRD section 8 fixes: schema changes are
// additive, and a column added later reads back as unknown for old rows rather
// than as a fabricated zero.
//
// Against a table that already existed it requires three things. No column may
// disappear, since the rows that carried it would lose what they recorded. No
// column may change its declared type, nullability, or default, since that
// reinterprets rows written under the old declaration. And a column that
// appears must be nullable and carry no default, so every row written before it
// existed reads back as unknown.
//
// A table created by the migration is unconstrained: it has no old rows, so its
// columns may be NOT NULL and may carry defaults.
func verifyAdditive(before, after map[string]map[string]columnFact) error {
	for _, table := range sortedKeys(before) {
		oldColumns := before[table]
		newColumns, ok := after[table]
		if !ok {
			return fmt.Errorf("%w: it removes table %s", ErrNotAdditive, table)
		}
		for _, column := range sortedKeys(oldColumns) {
			was := oldColumns[column]
			is, ok := newColumns[column]
			if !ok {
				return fmt.Errorf("%w: it removes column %s.%s", ErrNotAdditive, table, column)
			}
			if is != was {
				return fmt.Errorf("%w: it redeclares column %s.%s (was %s, now %s)",
					ErrNotAdditive, table, column, describeColumn(was), describeColumn(is))
			}
		}
		for _, column := range sortedKeys(newColumns) {
			if _, existed := oldColumns[column]; existed {
				continue
			}
			is := newColumns[column]
			if is.notNull {
				return fmt.Errorf("%w: it adds column %s.%s to an existing table as NOT NULL, so rows written before it could not read back as unknown",
					ErrNotAdditive, table, column)
			}
			if is.defaultValue.IsKnown() {
				return fmt.Errorf("%w: it adds column %s.%s to an existing table with default %s, which fabricates a value for rows written before it existed",
					ErrNotAdditive, table, column, is.defaultValue)
			}
		}
	}
	return nil
}

func describeColumn(c columnFact) string {
	parts := []string{c.declaredType}
	if c.notNull {
		parts = append(parts, "NOT NULL")
	}
	if v, ok := c.defaultValue.Get(); ok {
		parts = append(parts, "DEFAULT "+v)
	}
	return strings.Join(parts, " ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// errNoRows folds sql.ErrNoRows into this package's ErrNotFound so a caller has
// one condition to test rather than two.
func errNoRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// isNoRows reports whether err is the empty-result condition.
func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
