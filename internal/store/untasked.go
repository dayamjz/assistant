package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// untaskedShape is one of the queue's shapes about no task: migration 48's
// domain_wake, migration 55's kept_work_wake and migration 66's
// housekeeping_wake. Each is the wake table's
// columns with the task replaced by their owner, both are the first
// coordinator's alone, and both are offered, handed out and acknowledged on
// the same terms, so one implementation serves both and neither can drift
// from the other. What each row is about, and how it is read, stays its
// own file's.
type untaskedShape struct {
	// table is the shape's table.
	table string
	// noun is what an error calls one of its rows.
	noun string
}

var (
	domainWakeShape   = untaskedShape{table: "domain_wake", noun: "domain wake"}
	keptWorkWakeShape = untaskedShape{table: "kept_work_wake", noun: "kept work wake"}
	// housekeepingWakeShape is migration 66's housekeeping_wake, the
	// fourth shape, about a repository, on the same terms.
	housekeepingWakeShape = untaskedShape{table: "housekeeping_wake", noun: "housekeeping wake"}
)

// offeredClause is offeredClause for the shape: a subordinate is offered
// nothing, since no row of either shape is about a task in its domain, and
// the first coordinator every queued row not held for an attachment still
// attached.
func (sh untaskedShape) offeredClause(held []string, route WakeRoute) (string, []any, bool) {
	if route.Subordinate != "" {
		return "", nil, false
	}
	clause := sh.table + `.handled_at IS NULL`
	var args []any
	if len(held) > 0 {
		marks := make([]string, len(held))
		for i, h := range held {
			marks[i] = "?"
			args = append(args, h)
		}
		clause += ` AND (` + sh.table + `.handed_out_under IS NULL OR ` + sh.table + `.handed_out_under NOT IN (` + strings.Join(marks, ",") + `))`
	}
	return clause, args, true
}

// offered counts the rows a drain made now with held and route would offer.
func (sh untaskedShape) offered(ctx context.Context, s *Store, held []string, route WakeRoute) (int, error) {
	if err := s.live(); err != nil {
		return 0, err
	}
	clause, args, offered := sh.offeredClause(held, route)
	if !offered {
		return 0, nil
	}
	var n int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM `+sh.table+` WHERE `+clause, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: counting the offered %ss: %w", sh.noun, err)
	}
	return n, nil
}

// handOutTx records the rows on offer, oldest first, at most limit of
// them, as handed out to h.To under h.Under, inside the caller's
// transaction, and returns their identifiers. A subordinate's route is
// offered none and writes nothing.
func (sh untaskedShape) handOutTx(ctx context.Context, tx *sql.Tx, limit int, h WakeHandout) ([]int64, error) {
	clause, args, offered := sh.offeredClause(h.Held, h.Route)
	if !offered {
		return nil, nil
	}
	to, _ := acknowledgerText(h.To)
	rows, err := tx.QueryContext(ctx, `SELECT id FROM `+sh.table+` WHERE `+clause+` ORDER BY id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	now := encodeTime(nowUTC())
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+sh.table+` SET handed_out_at = ?, handed_out_to = ?, handed_out_under = ?,
				handouts = coalesce(handouts, 0) + 1
			WHERE id = ? AND handled_at IS NULL`,
			now, to, h.Under, id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// unacknowledged counts the shape's queued rows, handed out or not.
func (sh untaskedShape) unacknowledged(ctx context.Context, s *Store) (int, error) {
	if err := s.live(); err != nil {
		return 0, err
	}
	var n int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM `+sh.table+` WHERE handled_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: counting queued %ss: %w", sh.noun, err)
	}
	return n, nil
}

// acknowledgeTx closes the named rows inside a transaction, on
// acknowledgeWakesTx's terms: every row is checked before any is written,
// so a refusal writes nothing.
func (sh untaskedShape) acknowledgeTx(ctx context.Context, tx *sql.Tx, ids []int64, who, now string) error {
	for _, id := range ids {
		var handled Optional[string]
		err := tx.QueryRowContext(ctx, `SELECT handled_at FROM `+sh.table+` WHERE id = ?`, id).Scan(&handled)
		switch {
		case isNoRows(err):
			return &WakeIDError{ID: id, Reason: ErrWakeUnknown}
		case err != nil:
			return fmt.Errorf("store: acknowledging %s %d: %w", sh.noun, id, err)
		case handled.IsKnown():
			return &WakeIDError{ID: id, Reason: ErrWakeAcknowledged}
		}
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+sh.table+` SET handled_at = ?, acknowledged_by = ? WHERE id = ? AND handled_at IS NULL`,
			now, who, id); err != nil {
			return fmt.Errorf("store: acknowledging %s %d: %w", sh.noun, id, err)
		}
	}
	return nil
}
