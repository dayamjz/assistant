package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// keptWorkAnswerCheckWidenMigration names the migration that rebuilds
// kept_work so its answer CHECK admits remove-anyway. It is a constant so
// applyMigration can special-case the rebuild (statements alone cannot
// ALTER a CHECK). Unlike holdResolutionSealMigration, this migration's
// statements list is empty: the rebuild is entirely the Go helper.
const keptWorkAnswerCheckWidenMigration = "kept work answers admit remove-anyway"

// answerCheckLandCarryDiscard is the CHECK clause migration 51 wrote.
const answerCheckLandCarryDiscard = "answer IN ('land', 'carry-on', 'discard')"

// answerCheckWithRemoveAnyway is that clause with remove-anyway admitted.
const answerCheckWithRemoveAnyway = "answer IN ('land', 'carry-on', 'discard', 'remove-anyway')"

// widenKeptWorkAnswerCheck rebuilds kept_work inside tx so its answer CHECK
// admits remove-anyway, copying every row and recreating every index and
// trigger on that table. The caller turns foreign_keys off on the write
// connection before BeginTx so child rows that reference kept_work survive
// the drop; SQLite will not change that pragma inside a transaction. It
// writes no remove-anyway answer; Claim/Record for that answer are a later
// migration.
func widenKeptWorkAnswerCheck(ctx context.Context, tx *sql.Tx) error {
	var createSQL string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'kept_work'`).Scan(&createSQL); err != nil {
		return fmt.Errorf("store: reading kept_work's create statement: %w", err)
	}
	if !strings.Contains(createSQL, answerCheckLandCarryDiscard) {
		return fmt.Errorf("store: kept_work's create statement does not carry the answer CHECK to widen: %s", createSQL)
	}
	if strings.Contains(createSQL, answerCheckWithRemoveAnyway) {
		return fmt.Errorf("store: kept_work already admits remove-anyway")
	}
	const temp = "kept_work__answer_widen"
	createTemp := strings.Replace(createSQL, answerCheckLandCarryDiscard, answerCheckWithRemoveAnyway, 1)
	createTemp = strings.Replace(createTemp, "CREATE TABLE kept_work", "CREATE TABLE "+temp, 1)
	if !strings.HasPrefix(strings.TrimSpace(createTemp), "CREATE TABLE "+temp) {
		return fmt.Errorf("store: rewriting kept_work's create statement did not rename the table")
	}

	indexes, err := schemaObjects(ctx, tx, `SELECT name, sql FROM sqlite_master WHERE type = 'index' AND tbl_name = 'kept_work' AND sql IS NOT NULL`)
	if err != nil {
		return err
	}
	// Triggers on kept_work go with DROP TABLE; triggers on other tables
	// whose bodies name kept_work (wake and notification seals) must be
	// dropped first too: after the parent is gone, renaming the widened
	// table into place fails while those bodies still stand. Save every
	// matching trigger's SQL and put them all back after the rename.
	triggers, err := schemaObjects(ctx, tx, `SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND sql LIKE '%kept_work%'`)
	if err != nil {
		return err
	}
	for _, tr := range triggers {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+quoteIdent(tr.name)); err != nil {
			return fmt.Errorf("store: dropping trigger %s: %w", tr.name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, createTemp); err != nil {
		return fmt.Errorf("store: creating the widened kept_work: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+temp+` SELECT * FROM kept_work`); err != nil {
		return fmt.Errorf("store: copying kept_work into the widened table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE kept_work`); err != nil {
		return fmt.Errorf("store: dropping the prior kept_work: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE `+temp+` RENAME TO kept_work`); err != nil {
		return fmt.Errorf("store: renaming the widened kept_work into place: %w", err)
	}
	for _, idx := range indexes {
		if _, err := tx.ExecContext(ctx, idx.sql); err != nil {
			return fmt.Errorf("store: recreating index %s: %w", idx.name, err)
		}
	}
	for _, tr := range triggers {
		if _, err := tx.ExecContext(ctx, tr.sql); err != nil {
			return fmt.Errorf("store: recreating trigger %s: %w", tr.name, err)
		}
	}
	return nil
}

// quoteIdent quotes a SQLite identifier that is already a known schema name.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

type schemaObject struct {
	name, sql string
}

func schemaObjects(ctx context.Context, tx *sql.Tx, query string) ([]schemaObject, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("store: listing schema objects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []schemaObject
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.name, &o.sql); err != nil {
			return nil, fmt.Errorf("store: reading a schema object: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing schema objects: %w", err)
	}
	return out, nil
}
