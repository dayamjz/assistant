package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrStaleIncarnation reports a write by a supervisor loop incarnation that
// is no longer the current one: the watchdog has begun a later incarnation
// since this one was handed its Incarnation. The whole transaction is
// refused, so the markers and the wake it would have written are untouched.
// It is the fence that makes repair in place safe, and a loop meeting it has
// been replaced and should return.
var ErrStaleIncarnation = errors.New("store: this supervisor incarnation has been replaced")

// Incarnation is one incarnation of the supervisor loop, as the store knows
// it: the epoch BeginIncarnation assigned. Its field is unexported, so a
// value is obtained only from BeginIncarnation and never composed from a
// number read somewhere else; that is what keeps the fence on the loop's
// writes from being passed by a caller that read the current epoch and
// claimed it.
type Incarnation struct {
	epoch uint64
}

// Epoch is the incarnation's epoch, which the beacon carries beside the
// service's instance so a reader of the beacon can tell which incarnation
// wrote it. It reads the value and cannot build one.
func (i Incarnation) Epoch() uint64 { return i.epoch }

// BeginIncarnation makes the next incarnation current and returns it: the
// epoch after the one recorded, or one for a database that has recorded
// none, written in one transaction with the time it began. Every earlier
// Incarnation is stale from this call on, and the loop writes that take one
// refuse it with ErrStaleIncarnation.
func (s *Store) BeginIncarnation(ctx context.Context) (Incarnation, error) {
	var out Incarnation
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		current, err := currentIncarnation(ctx, tx)
		if err != nil {
			return err
		}
		out.epoch = current + 1
		_, err = tx.ExecContext(ctx, `
			INSERT INTO supervisor_incarnation (id, epoch, started_at) VALUES (1, ?, ?)
			ON CONFLICT(id) DO UPDATE SET epoch = excluded.epoch, started_at = excluded.started_at`,
			out.epoch, encodeTime(nowUTC()))
		return err
	})
	if err != nil {
		return Incarnation{}, fmt.Errorf("store: beginning a supervisor incarnation: %w", err)
	}
	return out, nil
}

// CurrentIncarnation reads the epoch of the current incarnation and when it
// began, and reports false with no error for a database in which none has
// begun. It is a read for a reader outside the loop; the loop itself does
// not compare against it, because the comparison it needs is the one made
// inside each write's transaction.
func (s *Store) CurrentIncarnation(ctx context.Context) (epoch uint64, startedAt time.Time, ok bool, err error) {
	if err := s.live(); err != nil {
		return 0, time.Time{}, false, err
	}
	var started string
	err = s.read.QueryRowContext(ctx, `SELECT epoch, started_at FROM supervisor_incarnation WHERE id = 1`).Scan(&epoch, &started)
	switch {
	case isNoRows(err):
		return 0, time.Time{}, false, nil
	case err != nil:
		return 0, time.Time{}, false, fmt.Errorf("store: reading the supervisor incarnation: %w", err)
	}
	if startedAt, err = decodeTime(started); err != nil {
		return 0, time.Time{}, false, fmt.Errorf("store: reading the supervisor incarnation: %w", err)
	}
	return epoch, startedAt, true, nil
}

// currentIncarnation reads the recorded epoch inside a transaction, zero
// when none is recorded.
func currentIncarnation(ctx context.Context, q querier) (uint64, error) {
	var epoch uint64
	err := q.QueryRowContext(ctx, `SELECT epoch FROM supervisor_incarnation WHERE id = 1`).Scan(&epoch)
	if isNoRows(err) {
		return 0, nil
	}
	return epoch, err
}

// fenceIncarnation refuses a transaction made by an incarnation that is not
// the current one. It runs inside the transaction that would write, which is
// what makes the check and the write one step: an incarnation that was
// current when it decided to write and was replaced before the write began
// is refused, not raced.
func fenceIncarnation(ctx context.Context, q querier, inc Incarnation) error {
	current, err := currentIncarnation(ctx, q)
	if err != nil {
		return err
	}
	if inc.epoch == 0 || inc.epoch != current {
		return fmt.Errorf("%w: epoch %d, current %d", ErrStaleIncarnation, inc.epoch, current)
	}
	return nil
}
