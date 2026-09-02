package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Hold is a decision waiting on a person, keyed by a stable key so that
// registering the same decision twice is one hold rather than two.
//
// PRD section 8 says a hold is closed only by an explicit resolution and never
// by the surrounding work finishing. This package keeps that literally: the
// only accessor that writes a resolution is ResolveHold, and it refuses an
// empty one. Nothing about a run's status touches a hold.
type Hold struct {
	// Key is the stable identifier of the decision. Registering the same key
	// again is a no-op.
	Key string
	// RunID names the run the decision belongs to, when it belongs to one.
	RunID Optional[string]
	// TaskID names the task the decision belongs to, when it belongs to one.
	TaskID Optional[string]
	// Subject is what the decision is about.
	Subject string
	// Detail is what the person needs in order to make it.
	Detail string
	// OpenedAt is when the hold was first registered.
	OpenedAt time.Time
	// Resolution is what was decided. It is unknown while the hold is open,
	// which is what distinguishes an open hold from one resolved with an empty
	// answer, a state ResolveHold refuses to create.
	Resolution Optional[string]
	// ResolvedAt is when it was decided, unknown while the hold is open.
	ResolvedAt Optional[time.Time]
}

// Open reports whether the hold is still waiting on a person.
func (h Hold) Open() bool { return !h.Resolution.IsKnown() }

// RegisterHold registers a decision and returns the hold as stored.
// Registering a key that already exists returns the existing hold unchanged,
// resolved or not, because the key is what says these are the same decision.
// A genuinely different decision gets a different key.
func (s *Store) RegisterHold(ctx context.Context, h Hold) (Hold, error) {
	if strings.TrimSpace(h.Key) == "" {
		return Hold{}, fmt.Errorf("store: hold has no key")
	}
	if strings.TrimSpace(h.Subject) == "" {
		return Hold{}, fmt.Errorf("store: hold %s has no subject", h.Key)
	}

	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO hold (key, run_id, task_id, subject, detail, opened_at, resolution, resolved_at)
			VALUES (?, ?, ?, ?, ?, ?, NULL, NULL)
			ON CONFLICT(key) DO NOTHING`,
			h.Key, h.RunID, h.TaskID, h.Subject, h.Detail, encodeTime(now))
		return err
	})
	if err != nil {
		return Hold{}, fmt.Errorf("store: registering hold %s: %w", h.Key, err)
	}
	return s.Hold(ctx, h.Key)
}

// ResolveHold closes a hold with an explicit resolution and returns it as
// stored. It refuses an empty resolution with ErrNoResolution, and it refuses
// to overwrite a resolution that is already there: the second decision on a
// closed hold is a new decision and belongs to a new key.
func (s *Store) ResolveHold(ctx context.Context, key, resolution string) (Hold, error) {
	if strings.TrimSpace(resolution) == "" {
		return Hold{}, fmt.Errorf("%w: hold %s", ErrNoResolution, key)
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE hold SET resolution = ?, resolved_at = ? WHERE key = ? AND resolution IS NULL`,
			resolution, encodeTime(nowUTC()), key)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 1 {
			return nil
		}
		var existing Optional[string]
		switch err := tx.QueryRowContext(ctx, `SELECT resolution FROM hold WHERE key = ?`, key).Scan(&existing); {
		case isNoRows(err):
			return ErrNotFound
		case err != nil:
			return err
		}
		return fmt.Errorf("hold is already resolved: %s", existing)
	})
	if err != nil {
		return Hold{}, fmt.Errorf("store: resolving hold %s: %w", key, err)
	}
	return s.Hold(ctx, key)
}

// Hold returns the hold with the given key, or ErrNotFound.
func (s *Store) Hold(ctx context.Context, key string) (Hold, error) {
	if err := s.live(); err != nil {
		return Hold{}, err
	}
	row := s.read.QueryRowContext(ctx, holdColumns+` FROM hold WHERE key = ?`, key)
	h, err := scanHold(row)
	if err != nil {
		return Hold{}, fmt.Errorf("store: reading hold %s: %w", key, errNoRows(err))
	}
	return h, nil
}

// OpenHolds returns every hold still waiting on a person, oldest first.
func (s *Store) OpenHolds(ctx context.Context) ([]Hold, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx,
		holdColumns+` FROM hold WHERE resolution IS NULL ORDER BY opened_at, key`)
	if err != nil {
		return nil, fmt.Errorf("store: listing open holds: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Hold
	for rows.Next() {
		h, err := scanHold(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing open holds: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing open holds: %w", err)
	}
	return out, nil
}

const holdColumns = `SELECT key, run_id, task_id, subject, detail, opened_at, resolution, resolved_at`

func scanHold(sc scanner) (Hold, error) {
	var h Hold
	var opened string
	var resolvedAt Optional[string]
	if err := sc.Scan(&h.Key, &h.RunID, &h.TaskID, &h.Subject, &h.Detail, &opened, &h.Resolution, &resolvedAt); err != nil {
		return Hold{}, err
	}
	var err error
	if h.OpenedAt, err = decodeTime(opened); err != nil {
		return Hold{}, err
	}
	if h.ResolvedAt, err = optionalTimeValue(resolvedAt, "resolved_at"); err != nil {
		return Hold{}, err
	}
	return h, nil
}
