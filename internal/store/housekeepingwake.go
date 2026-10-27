package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// HousekeepingWake is one row of the queue's fourth shape: PRD section 8's
// housekeeping-due wake, an actionable wake about a repository rather than a
// task, keyed by the repository and by the pass it follows, or by none
// before the first pass. It is written by the supervisor loop on Wake's
// terms and drained, handed out, acknowledged and offered to the private
// channel on the kept-work wake's terms. It is in no domain, so it is the
// first coordinator's alone. Acknowledging it creates nothing and answers
// nothing. Its identifiers are a sequence of their own, apart from every
// other shape's.
type HousekeepingWake struct {
	// ID is the row's identifier, which an acknowledgement names.
	ID int64 `json:"id"`
	// Repository is the repository the wake is about.
	Repository string `json:"repository"`
	// Pass is the pass the wake follows: the newest pass of the repository
	// when it was raised, open or not, and unknown before the first pass.
	Pass Optional[string] `json:"pass"`
	// Kind is what the wake is, in the supervisor's vocabulary.
	Kind string `json:"kind"`
	// Marker is the detector's idempotency component for this wake.
	Marker string `json:"marker"`
	// Detail is what the wake names, stored redacted: what could not be
	// read, or the open pass and what it waits on.
	Detail string `json:"detail"`
	// CreatedAt is when the row was appended.
	CreatedAt time.Time `json:"created_at"`
	// HandledAt is when the wake was acknowledged, unknown while queued.
	HandledAt Optional[time.Time] `json:"handled_at"`
	// AcknowledgedBy is who acknowledged it, unknown while queued.
	AcknowledgedBy Optional[Acknowledger] `json:"acknowledged_by"`
	// HandedOutAt is when the latest drain handed the row out.
	HandedOutAt Optional[time.Time] `json:"handed_out_at"`
	// HandedOutTo is kept off the wire on Wake.HandedOutTo's terms.
	HandedOutTo Optional[Acknowledger] `json:"-"`
	// HandedOutUnder is kept off the wire on Wake.HandedOutUnder's terms.
	HandedOutUnder Optional[string] `json:"-"`
	// Handouts is how many drains have handed the row out.
	Handouts Optional[int64] `json:"handouts"`
}

// Queued reports whether the wake has not been acknowledged.
func (w HousekeepingWake) Queued() bool { return !w.HandledAt.IsKnown() }

// HandedOut reports whether a drain has handed the wake out and it has not
// been acknowledged since.
func (w HousekeepingWake) HandedOut() bool { return w.Queued() && w.HandedOutAt.IsKnown() }

// HousekeepingWakeRequest is the housekeeping wake a loop asks to append.
type HousekeepingWakeRequest struct {
	// Repository is the repository the wake is about.
	Repository string
	// Pass is the pass it follows, and empty for none.
	Pass string
	// Kind is what the wake is, checked only for being non-empty.
	Kind string
	// Marker is the detector's idempotency component, unique within the
	// repository.
	Marker string
	// Detail is what the wake names, redacted before it is stored.
	Detail string
}

func (w HousekeepingWakeRequest) validate() error {
	switch {
	case strings.TrimSpace(w.Repository) == "":
		return errors.New("store: a housekeeping wake names no repository")
	case strings.TrimSpace(w.Kind) == "":
		return fmt.Errorf("store: repository %s: a housekeeping wake has no kind", w.Repository)
	case strings.TrimSpace(w.Marker) == "":
		return fmt.Errorf("store: repository %s: a %s housekeeping wake has no marker", w.Repository, w.Kind)
	case strings.TrimSpace(w.Detail) == "":
		return fmt.Errorf("store: repository %s: a %s housekeeping wake names nothing", w.Repository, w.Kind)
	}
	return nil
}

// HousekeepingWakeAppend is AppendHousekeepingWake's answer, on WakeAppend's
// terms.
type HousekeepingWakeAppend struct {
	Disposition WakeDisposition
	Wake        HousekeepingWake
}

// AppendHousekeepingWake appends an actionable housekeeping wake, fenced
// by the incarnation on AppendWakeWithMarkers's terms. A marker already
// raised for the repository answers WakeAlreadyQueued with its queued row
// and writes nothing, or ErrWakeHandled once that row was acknowledged,
// also writing nothing.
func (s *Store) AppendHousekeepingWake(ctx context.Context, inc Incarnation, w HousekeepingWakeRequest) (HousekeepingWakeAppend, error) {
	if err := w.validate(); err != nil {
		return HousekeepingWakeAppend{}, err
	}
	detail := s.redact.Redact(w.Detail)
	var out HousekeepingWakeAppend
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := fenceIncarnation(ctx, tx, inc); err != nil {
			return err
		}
		existing, err := scanHousekeepingWake(tx.QueryRowContext(ctx, housekeepingWakeColumns+
			` WHERE repository_id = ? AND marker = ?`, w.Repository, w.Marker))
		switch {
		case err == nil && existing.Queued():
			out.Disposition = WakeAlreadyQueued
			out.Wake = existing
			return nil
		case err == nil:
			return fmt.Errorf("%w: housekeeping wake %d", ErrWakeHandled, existing.ID)
		case !isNoRows(err):
			return err
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO housekeeping_wake (repository_id, pass, kind, marker, detail, created_at, handled_at, acknowledged_by)
			VALUES (?, ?, ?, ?, ?, ?, NULL, NULL)`,
			w.Repository, namedOrNull(w.Pass), w.Kind, w.Marker, detail, encodeTime(nowUTC()))
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		appended, err := scanHousekeepingWake(tx.QueryRowContext(ctx, housekeepingWakeColumns+` WHERE id = ?`, id))
		if err != nil {
			return err
		}
		out.Disposition = WakeAppended
		out.Wake = appended
		return nil
	})
	if err != nil {
		return HousekeepingWakeAppend{}, fmt.Errorf("store: appending a %s wake for repository %s: %w", w.Kind, w.Repository, err)
	}
	return out, nil
}

// HousekeepingWakes returns every housekeeping wake, acknowledged or not,
// oldest first.
func (s *Store) HousekeepingWakes(ctx context.Context) ([]HousekeepingWake, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	return s.queryHousekeepingWakes(ctx, "the housekeeping wakes", housekeepingWakeColumns+` ORDER BY id`)
}

// QueuedHousekeepingWakes returns the queued housekeeping wakes, oldest
// first, at most limit of them, without handing them out.
func (s *Store) QueuedHousekeepingWakes(ctx context.Context, limit int) ([]HousekeepingWake, error) {
	if limit < 1 {
		return nil, fmt.Errorf("store: a limit of %d queued housekeeping wakes is not a batch size", limit)
	}
	if err := s.live(); err != nil {
		return nil, err
	}
	return s.queryHousekeepingWakes(ctx, "the queued housekeeping wakes",
		housekeepingWakeColumns+` WHERE handled_at IS NULL ORDER BY id LIMIT ?`, limit)
}

// OfferedHousekeepingWakes reports how many queued housekeeping wakes a
// drain made now with held and route would offer, on the kept-work wake's
// terms.
func (s *Store) OfferedHousekeepingWakes(ctx context.Context, held []string, route WakeRoute) (int, error) {
	return housekeepingWakeShape.offered(ctx, s, held, route)
}

// drainHousekeepingWakesTx hands out the housekeeping wakes on offer inside
// a transaction.
func drainHousekeepingWakesTx(ctx context.Context, tx *sql.Tx, limit int, h WakeHandout) ([]HousekeepingWake, error) {
	ids, err := housekeepingWakeShape.handOutTx(ctx, tx, limit, h)
	if err != nil {
		return nil, err
	}
	out := make([]HousekeepingWake, 0, len(ids))
	for _, id := range ids {
		w, err := scanHousekeepingWake(tx.QueryRowContext(ctx, housekeepingWakeColumns+` WHERE id = ?`, id))
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// UnacknowledgedHousekeepingWakes returns how many housekeeping wakes are
// queued, handed out or not, the count the in-flight table reads beside the
// other shapes.
func (s *Store) UnacknowledgedHousekeepingWakes(ctx context.Context) (int, error) {
	return housekeepingWakeShape.unacknowledged(ctx, s)
}

const housekeepingWakeColumns = `SELECT id, repository_id, pass, kind, marker, detail, created_at, handled_at,
	acknowledged_by, handed_out_at, handed_out_to, handed_out_under, handouts FROM housekeeping_wake`

func (s *Store) queryHousekeepingWakes(ctx context.Context, what, query string, args ...any) ([]HousekeepingWake, error) {
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: reading %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	var out []HousekeepingWake
	for rows.Next() {
		w, err := scanHousekeepingWake(rows)
		if err != nil {
			return nil, fmt.Errorf("store: reading %s: %w", what, err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading %s: %w", what, err)
	}
	return out, nil
}

func scanHousekeepingWake(sc scanner) (HousekeepingWake, error) {
	var w HousekeepingWake
	var created string
	var handled, by, handedAt, handedTo Optional[string]
	if err := sc.Scan(&w.ID, &w.Repository, &w.Pass, &w.Kind, &w.Marker, &w.Detail, &created, &handled, &by,
		&handedAt, &handedTo, &w.HandedOutUnder, &w.Handouts); err != nil {
		return HousekeepingWake{}, err
	}
	var err error
	if w.CreatedAt, err = decodeTime(created); err != nil {
		return HousekeepingWake{}, err
	}
	if w.HandledAt, err = optionalTimeValue(handled, "handled_at"); err != nil {
		return HousekeepingWake{}, err
	}
	if w.AcknowledgedBy, err = acknowledgerValue(by, "acknowledged_by"); err != nil {
		return HousekeepingWake{}, err
	}
	if w.HandedOutAt, err = optionalTimeValue(handedAt, "handed_out_at"); err != nil {
		return HousekeepingWake{}, err
	}
	if w.HandedOutTo, err = acknowledgerValue(handedTo, "handed_out_to"); err != nil {
		return HousekeepingWake{}, err
	}
	return w, nil
}
