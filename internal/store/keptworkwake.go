package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// KeptWorkWake is one row of the queue's third shape: PRD section 8's
// kept-work wake, an actionable wake about a kept work entry rather than a
// task, for an entry no task names, such as the run of a person's own
// push. It is written by the supervisor loop on Wake's terms and drained,
// handed out and acknowledged on the same terms. It is owned by the entry
// (P14): Project and Branch are read off the entry's row at every read and
// recorded on no row here. An entry is raised at most once, which the table's unique
// key holds, and AppendKeptWorkWake records when the entry surfaced in the
// same transaction. Its identifiers are a sequence of their own, apart
// from Wake's and DomainWake's, so a caller names the shapes apart. It is
// the first coordinator's alone: a drain on a subordinate's route is
// offered none, since the entry names no task and so no domain.
//
// Acknowledging the wake closes the wake and answers nothing: the entry
// stays open until it is answered or closed on a proof.
type KeptWorkWake struct {
	// ID is the row's identifier, which the store assigns in the order it
	// accepted the rows, and which an acknowledgement names.
	ID int64 `json:"id"`
	// KeptWork names the kept work entry the wake is about.
	KeptWork int64 `json:"kept_work"`
	// Project is the entry's project, read off its row, and unknown for an
	// entry whose owner names none.
	Project Optional[string] `json:"project"`
	// Branch is the branch the entry's copy was kept on, read off its row,
	// and unknown for an entry whose owner names none.
	Branch Optional[string] `json:"branch"`
	// Kind is what the wake is, in the supervisor's vocabulary.
	Kind string `json:"kind"`
	// Marker is the detector's idempotency component for this wake.
	Marker string `json:"marker"`
	// CreatedAt is when the row was appended.
	CreatedAt time.Time `json:"created_at"`
	// HandledAt is when the wake was acknowledged, and unknown while it is
	// still queued.
	HandledAt Optional[time.Time] `json:"handled_at"`
	// AcknowledgedBy is who acknowledged it, and unknown while it is still
	// queued.
	AcknowledgedBy Optional[Acknowledger] `json:"acknowledged_by"`
	// HandedOutAt is when the latest drain handed the row out, and unknown
	// while no drain has.
	HandedOutAt Optional[time.Time] `json:"handed_out_at"`
	// HandedOutTo is who the latest drain handed the row to, kept off the
	// wire on Wake.HandedOutTo's terms.
	HandedOutTo Optional[Acknowledger] `json:"-"`
	// HandedOutUnder is the attachment the latest drain handed the row out
	// under, kept off the wire on Wake.HandedOutUnder's terms.
	HandedOutUnder Optional[string] `json:"-"`
	// Handouts is how many drains have handed the row out.
	Handouts Optional[int64] `json:"handouts"`
}

// Queued reports whether the wake has not been acknowledged.
func (w KeptWorkWake) Queued() bool { return !w.HandledAt.IsKnown() }

// HandedOut reports whether a drain has handed the wake out and it has not
// been acknowledged since.
func (w KeptWorkWake) HandedOut() bool { return w.Queued() && w.HandedOutAt.IsKnown() }

// KeptWorkWakeRequest is the kept-work wake a loop asks to append.
type KeptWorkWakeRequest struct {
	// KeptWork names the entry.
	KeptWork int64
	// Kind is what the wake is, in the supervisor's vocabulary, checked
	// only for being non-empty.
	Kind string
	// Marker is the detector's idempotency component.
	Marker string
}

func (w KeptWorkWakeRequest) validate() error {
	if w.KeptWork <= 0 {
		return errors.New("store: a kept work wake names no kept work entry")
	}
	if strings.TrimSpace(w.Kind) == "" {
		return fmt.Errorf("store: kept work entry %d: a kept work wake has no kind", w.KeptWork)
	}
	if strings.TrimSpace(w.Marker) == "" {
		return fmt.Errorf("store: kept work entry %d: a %s kept work wake has no marker", w.KeptWork, w.Kind)
	}
	return nil
}

// KeptWorkWakeAppend is AppendKeptWorkWake's answer, on WakeAppend's
// terms.
type KeptWorkWakeAppend struct {
	Disposition WakeDisposition
	Wake        KeptWorkWake
}

// AppendKeptWorkWake appends an actionable kept-work wake about an open
// entry and records, in the same transaction, that the entry surfaced at
// the row's creation, so no later pass finds it unsurfaced. It is fenced by
// the incarnation on AppendWakeWithMarkers's terms. An entry already raised
// on this shape answers WakeAlreadyQueued with its queued row and writes
// nothing, or ErrWakeHandled when that row was acknowledged. An entry that
// does not exist is ErrNotFound, and one answered or closed since the
// caller read it is ErrKeptWorkAnswered or ErrKeptWorkClosed, each wrapped
// and with nothing written, because such an entry is owed no wake.
func (s *Store) AppendKeptWorkWake(ctx context.Context, inc Incarnation, w KeptWorkWakeRequest) (KeptWorkWakeAppend, error) {
	if err := w.validate(); err != nil {
		return KeptWorkWakeAppend{}, err
	}
	var out KeptWorkWakeAppend
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := fenceIncarnation(ctx, tx, inc); err != nil {
			return err
		}
		existing, err := scanKeptWorkWake(tx.QueryRowContext(ctx, keptWorkWakeColumns+` WHERE kept_work_wake.kept_work = ?`, w.KeptWork))
		switch {
		case err == nil && existing.Queued():
			out.Disposition = WakeAlreadyQueued
			out.Wake = existing
			return nil
		case err == nil:
			return fmt.Errorf("%w: kept work wake %d", ErrWakeHandled, existing.ID)
		case !isNoRows(err):
			return err
		}
		entry, err := scanKeptWork(tx.QueryRowContext(ctx, keptWorkColumns+` FROM kept_work WHERE id = ?`, w.KeptWork))
		if err != nil {
			return errNoRows(err)
		}
		switch {
		case entry.Answered():
			return ErrKeptWorkAnswered
		case !entry.Open():
			return ErrKeptWorkClosed
		}
		now := nowUTC()
		result, err := tx.ExecContext(ctx, `
			INSERT INTO kept_work_wake (kept_work, kind, marker, created_at, handled_at, acknowledged_by)
			VALUES (?, ?, ?, ?, NULL, NULL)`,
			w.KeptWork, w.Kind, w.Marker, encodeTime(now))
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if err := markKeptWorkSurfacedTx(ctx, tx, w.KeptWork, now); err != nil {
			return err
		}
		appended, err := keptWorkWakeByID(ctx, tx, id)
		if err != nil {
			return err
		}
		out.Disposition = WakeAppended
		out.Wake = appended
		return nil
	})
	if err != nil {
		return KeptWorkWakeAppend{}, fmt.Errorf("store: appending a %s wake for kept work entry %d: %w", w.Kind, w.KeptWork, err)
	}
	return out, nil
}

// markKeptWorkSurfacedTx records, inside the caller's transaction, that the
// entry surfaced at at. When the entry surfaced only moves forward, which
// the schema holds, so an instant already recorded past this one stands. An
// entry that does not exist is ErrNotFound.
func markKeptWorkSurfacedTx(ctx context.Context, tx *sql.Tx, entry int64, at time.Time) error {
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM kept_work WHERE id = ?`, entry).Scan(&one); err != nil {
		return errNoRows(err)
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE kept_work SET surfaced_at = ? WHERE id = ? AND (surfaced_at IS NULL OR surfaced_at < ?)`,
		encodeTime(at), entry, encodeTime(at))
	return err
}

// TaskWakeKeyed returns the oldest wake of any task with the kind and
// marker, queued or acknowledged, and whether there is one. It is the read
// of whether a detection keyed that way already surfaced on a task, which
// the supervisor loop asks of a kept work entry before it raises the entry
// on the third shape.
func (s *Store) TaskWakeKeyed(ctx context.Context, kind, marker string) (Wake, bool, error) {
	if err := s.live(); err != nil {
		return Wake{}, false, err
	}
	w, err := scanWake(s.read.QueryRowContext(ctx, wakeColumns+` FROM wake WHERE kind = ? AND marker = ? ORDER BY id LIMIT 1`, kind, marker))
	switch {
	case isNoRows(err):
		return Wake{}, false, nil
	case err != nil:
		return Wake{}, false, fmt.Errorf("store: reading the %s wake marked %s: %w", kind, marker, err)
	}
	return w, true, nil
}

// KeptWorkWakes returns every kept-work wake, acknowledged or not, oldest
// first, on TaskWakes' terms.
func (s *Store) KeptWorkWakes(ctx context.Context) ([]KeptWorkWake, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	return s.queryKeptWorkWakes(ctx, "the kept work wakes", keptWorkWakeColumns+` ORDER BY kept_work_wake.id`)
}

// QueuedKeptWorkWakes returns the queued kept-work wakes, oldest first, at
// most limit of them, handed out or not, and hands out none, on
// QueuedWakes' terms.
func (s *Store) QueuedKeptWorkWakes(ctx context.Context, limit int) ([]KeptWorkWake, error) {
	if limit < 1 {
		return nil, fmt.Errorf("store: a limit of %d queued kept work wakes is not a batch size", limit)
	}
	if err := s.live(); err != nil {
		return nil, err
	}
	return s.queryKeptWorkWakes(ctx, "the queued kept work wakes",
		keptWorkWakeColumns+` WHERE kept_work_wake.handled_at IS NULL ORDER BY kept_work_wake.id LIMIT ?`, limit)
}

// OfferedKeptWorkWakes reports how many queued kept-work wakes a drain made
// now with held and route would offer, on OfferedWakes' terms: none on a
// subordinate's route.
func (s *Store) OfferedKeptWorkWakes(ctx context.Context, held []string, route WakeRoute) (int, error) {
	return keptWorkWakeShape.offered(ctx, s, held, route)
}

// drainKeptWorkWakesTx hands out the kept-work wakes on offer inside a
// transaction the caller holds, on drainDomainWakesTx's terms.
func drainKeptWorkWakesTx(ctx context.Context, tx *sql.Tx, limit int, h WakeHandout) ([]KeptWorkWake, error) {
	ids, err := keptWorkWakeShape.handOutTx(ctx, tx, limit, h)
	if err != nil {
		return nil, err
	}
	out := make([]KeptWorkWake, 0, len(ids))
	for _, id := range ids {
		drained, err := keptWorkWakeByID(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, drained)
	}
	return out, nil
}

// UnacknowledgedKeptWorkWakes returns how many kept-work wakes are queued,
// handed out or not: the third shape's half of PRD section 5's "work is in
// flight", on UnacknowledgedDomainWakes' terms.
func (s *Store) UnacknowledgedKeptWorkWakes(ctx context.Context) (int, error) {
	return keptWorkWakeShape.unacknowledged(ctx, s)
}

const keptWorkWakeColumns = `SELECT kept_work_wake.id, kept_work_wake.kept_work, kept_work.repository_id, kept_work.branch,
	kept_work_wake.kind, kept_work_wake.marker, kept_work_wake.created_at, kept_work_wake.handled_at,
	kept_work_wake.acknowledged_by, kept_work_wake.handed_out_at, kept_work_wake.handed_out_to,
	kept_work_wake.handed_out_under, kept_work_wake.handouts
	FROM kept_work_wake JOIN kept_work ON kept_work.id = kept_work_wake.kept_work`

func keptWorkWakeByID(ctx context.Context, q querier, id int64) (KeptWorkWake, error) {
	return scanKeptWorkWake(q.QueryRowContext(ctx, keptWorkWakeColumns+` WHERE kept_work_wake.id = ?`, id))
}

func (s *Store) queryKeptWorkWakes(ctx context.Context, what, query string, args ...any) ([]KeptWorkWake, error) {
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: reading %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	var out []KeptWorkWake
	for rows.Next() {
		w, err := scanKeptWorkWake(rows)
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

func scanKeptWorkWake(sc scanner) (KeptWorkWake, error) {
	var w KeptWorkWake
	var created string
	var handled, by, handedAt, handedTo Optional[string]
	if err := sc.Scan(&w.ID, &w.KeptWork, &w.Project, &w.Branch, &w.Kind, &w.Marker, &created, &handled, &by,
		&handedAt, &handedTo, &w.HandedOutUnder, &w.Handouts); err != nil {
		return KeptWorkWake{}, err
	}
	var err error
	if w.CreatedAt, err = decodeTime(created); err != nil {
		return KeptWorkWake{}, err
	}
	if w.HandledAt, err = optionalTimeValue(handled, "handled_at"); err != nil {
		return KeptWorkWake{}, err
	}
	if w.AcknowledgedBy, err = acknowledgerValue(by, "acknowledged_by"); err != nil {
		return KeptWorkWake{}, err
	}
	if w.HandedOutAt, err = optionalTimeValue(handedAt, "handed_out_at"); err != nil {
		return KeptWorkWake{}, err
	}
	if w.HandedOutTo, err = acknowledgerValue(handedTo, "handed_out_to"); err != nil {
		return KeptWorkWake{}, err
	}
	return w, nil
}
