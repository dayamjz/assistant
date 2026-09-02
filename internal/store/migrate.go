package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// createMigrationTable records which migrations have been applied. It is one of
// the two statements executed outside a migration's own transaction, and it is
// idempotent, so a database interrupted at any point still opens.
const createMigrationTable = `CREATE TABLE IF NOT EXISTS schema_migration (
	version    INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	statements INTEGER NOT NULL,
	digest     TEXT,
	applied_at TEXT NOT NULL
) STRICT`

// addMigrationDigest gives the digest column to a schema_migration table
// created by a build that recorded no digest. That table is created outside the
// migration system, so nothing in it is governed by verifyAdditive and this
// statement carries the same rule by hand: the column is nullable and has no
// default, so a row recorded before digests existed reads back as unknown
// rather than as a fabricated fingerprint that would never match.
const addMigrationDigest = `ALTER TABLE schema_migration ADD COLUMN digest TEXT`

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
	if err := ensureMigrationDigestColumn(ctx, db); err != nil {
		return err
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
		recorded, hasDigest := record.digest.Get()
		if hasDigest && recorded != statementsDigest(known) {
			return fmt.Errorf("%w: migration %d (%s) is recorded with digest %s, this build's copy digests to %s",
				ErrSchemaChanged, version, known.name, recorded, statementsDigest(known))
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

// statementsDigest fingerprints a migration's statements, so an edit to a
// statement's text is visible to a build that did not make it. Each statement
// is hashed with its length in front of it, so moving text from one statement
// to the next changes the digest rather than cancelling out.
//
// The digest is over the exact text. Reformatting a shipped migration changes
// it just as rewriting one does, and that is the intended reading: this package
// cannot tell the two apart, and the answer to either is a new migration.
func statementsDigest(m migration) string {
	var b strings.Builder
	for _, statement := range m.statements {
		b.WriteString(strconv.Itoa(len(statement)))
		b.WriteString(":")
		b.WriteString(statement)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// ensureMigrationDigestColumn adds the digest column to a schema_migration
// table that predates it, so a database created by an earlier build opens
// instead of failing on a column that is not there. It is idempotent: the
// column is added only when the catalog says it is missing.
func ensureMigrationDigestColumn(ctx context.Context, db *sql.DB) error {
	var present int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('schema_migration') WHERE name = 'digest'`,
	).Scan(&present); err != nil {
		return fmt.Errorf("store: reading the migration table's columns: %w", err)
	}
	if present > 0 {
		return nil
	}
	if _, err := db.ExecContext(ctx, addMigrationDigest); err != nil {
		return fmt.Errorf("store: adding the migration digest column: %w", err)
	}
	return nil
}

// migrationRecord is one row of schema_migration.
type migrationRecord struct {
	name       string
	statements int
	// digest is the fingerprint of the statements as applied. It is unknown for
	// a row recorded by a build that did not write one, which is the one case
	// where an in-place edit to that migration cannot be detected.
	digest Optional[string]
}

func appliedMigrations(ctx context.Context, db *sql.DB) (map[int]migrationRecord, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, name, statements, digest FROM schema_migration`)
	if err != nil {
		return nil, fmt.Errorf("store: reading applied migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	applied := make(map[int]migrationRecord)
	for rows.Next() {
		var version, statements int
		var name string
		var digest Optional[string]
		if err := rows.Scan(&version, &name, &statements, &digest); err != nil {
			return nil, fmt.Errorf("store: reading applied migrations: %w", err)
		}
		applied[version] = migrationRecord{name: name, statements: statements, digest: digest}
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
		`INSERT INTO schema_migration (version, name, statements, digest, applied_at) VALUES (?, ?, ?, ?, ?)`,
		m.version, m.name, len(m.statements), Known(statementsDigest(m)), encodeTime(nowUTC()),
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
