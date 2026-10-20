package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// DomainWake is one row of the queue's second shape: an actionable wake
// about a domain rather than a task, PRD section 6's subordinate gone and
// the hold the relaunch bound raises for a delegation, written by the
// supervisor loop on Wake's terms and drained, handed out and acknowledged
// on the same terms. It is owned by the delegation row (P14): Domain is
// read off that row at every read and recorded on no row here. Its
// identifiers are a sequence of their own, apart from Wake's, so a caller
// names the two shapes apart. It is the first coordinator's alone: a drain
// on a subordinate's route is offered none, since every kind here is about
// a subordinate.
type DomainWake struct {
	// ID is the row's identifier, which the store assigns in the order it
	// accepted the rows, and which an acknowledgement names.
	ID int64 `json:"id"`
	// Delegation names the delegation the wake is about.
	Delegation int64 `json:"delegation"`
	// Domain is the delegation's domain, read off its row.
	Domain string `json:"domain"`
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
func (w DomainWake) Queued() bool { return !w.HandledAt.IsKnown() }

// HandedOut reports whether a drain has handed the wake out and it has not
// been acknowledged since.
func (w DomainWake) HandedOut() bool { return w.Queued() && w.HandedOutAt.IsKnown() }

// DomainWakeRequest is the domain wake a loop asks to append, on
// WakeRequest's terms: the uniqueness the queue enforces is over the
// delegation, the kind and the marker together.
type DomainWakeRequest struct {
	// Delegation names the delegation.
	Delegation int64
	// Kind is what the wake is, in the supervisor's vocabulary, checked
	// only for being non-empty.
	Kind string
	// Marker is the detector's idempotency component.
	Marker string
}

func (w DomainWakeRequest) validate() error {
	if w.Delegation <= 0 {
		return errors.New("store: a domain wake names no delegation")
	}
	if strings.TrimSpace(w.Kind) == "" {
		return fmt.Errorf("store: delegation %d: a domain wake has no kind", w.Delegation)
	}
	if strings.TrimSpace(w.Marker) == "" {
		return fmt.Errorf("store: delegation %d: a %s domain wake has no marker", w.Delegation, w.Kind)
	}
	return nil
}

// DelegationMarkers is what the supervisor loop had observed about one
// delegation's subordinate when it last passed over it, on WakeMarkers'
// terms: one row per delegation, rewritten in place, the comparison base
// for the next pass and not the delegation's state.
type DelegationMarkers struct {
	// Delegation names the delegation.
	Delegation int64 `json:"delegation"`
	// UnobservedSince is when the subordinate's terminal first could not
	// be observed, and unknown while it is observed.
	UnobservedSince Optional[time.Time] `json:"unobserved_since"`
	// GoneSurfacedAt is when the terminal that could not be observed last
	// surfaced as gone, on WakeMarkers.GoneSurfacedAt's terms.
	GoneSurfacedAt Optional[time.Time] `json:"gone_surfaced_at"`
	// UpdatedAt is when the row was last written.
	UpdatedAt time.Time `json:"updated_at"`
}

// DelegationMarkerAdvance is what a pass hands over to become the
// delegation's markers.
type DelegationMarkerAdvance struct {
	UnobservedSince Optional[time.Time]
	GoneSurfacedAt  Optional[time.Time]
}

// DomainWakeAppend is AppendDomainWakeWithMarkers's answer, on
// WakeAppend's terms.
type DomainWakeAppend struct {
	Disposition WakeDisposition
	Wake        DomainWake
	Markers     DelegationMarkers
}

// AppendDomainWakeWithMarkers appends an actionable domain wake and
// advances the delegation's markers in one transaction, on
// AppendWakeWithMarkers's terms: fenced by the incarnation, a replayed
// detection against a queued row is WakeAlreadyQueued with the markers
// advanced, the same key against an acknowledged row is ErrWakeHandled
// with nothing written, and a delegation that does not exist is
// ErrNotFound, wrapped.
func (s *Store) AppendDomainWakeWithMarkers(ctx context.Context, inc Incarnation, w DomainWakeRequest,
	advance DelegationMarkerAdvance) (DomainWakeAppend, error) {
	if err := w.validate(); err != nil {
		return DomainWakeAppend{}, err
	}
	var out DomainWakeAppend
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := fenceIncarnation(ctx, tx, inc); err != nil {
			return err
		}
		if _, err := delegationByID(ctx, tx, w.Delegation); err != nil {
			return err
		}
		markers, err := advanceDelegationMarkers(ctx, tx, w.Delegation, advance)
		if err != nil {
			return err
		}
		out.Markers = markers
		existing, err := scanDomainWake(tx.QueryRowContext(ctx, domainWakeColumns+
			` WHERE domain_wake.delegation = ? AND domain_wake.kind = ? AND domain_wake.marker = ?`, w.Delegation, w.Kind, w.Marker))
		switch {
		case err == nil && existing.Queued():
			out.Disposition = WakeAlreadyQueued
			out.Wake = existing
			return nil
		case err == nil:
			return fmt.Errorf("%w: domain wake %d", ErrWakeHandled, existing.ID)
		case !isNoRows(err):
			return err
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO domain_wake (delegation, kind, marker, created_at, handled_at, acknowledged_by)
			VALUES (?, ?, ?, ?, NULL, NULL)`,
			w.Delegation, w.Kind, w.Marker, encodeTime(nowUTC()))
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		appended, err := domainWakeByID(ctx, tx, id)
		if err != nil {
			return err
		}
		out.Disposition = WakeAppended
		out.Wake = appended
		return nil
	})
	if err != nil {
		return DomainWakeAppend{}, fmt.Errorf("store: appending a %s domain wake for delegation %d: %w", w.Kind, w.Delegation, err)
	}
	return out, nil
}

// AdvanceDelegationMarkers rewrites a delegation's markers with no wake, on
// AdvanceWakeMarkers's terms.
func (s *Store) AdvanceDelegationMarkers(ctx context.Context, inc Incarnation, id int64, advance DelegationMarkerAdvance) (DelegationMarkers, error) {
	var out DelegationMarkers
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := fenceIncarnation(ctx, tx, inc); err != nil {
			return err
		}
		if _, err := delegationByID(ctx, tx, id); err != nil {
			return err
		}
		var err error
		out, err = advanceDelegationMarkers(ctx, tx, id, advance)
		return err
	})
	if err != nil {
		return DelegationMarkers{}, fmt.Errorf("store: advancing the markers of delegation %d: %w", id, err)
	}
	return out, nil
}

// DelegationMarkers returns a delegation's markers and whether the loop has
// written any, on WakeMarkers' terms.
func (s *Store) DelegationMarkers(ctx context.Context, id int64) (DelegationMarkers, bool, error) {
	if err := s.live(); err != nil {
		return DelegationMarkers{}, false, err
	}
	m, err := delegationMarkersOf(ctx, s.read, id)
	if isNoRows(err) {
		return DelegationMarkers{}, false, nil
	}
	if err != nil {
		return DelegationMarkers{}, false, fmt.Errorf("store: reading the markers of delegation %d: %w", id, err)
	}
	return m, true, nil
}

func advanceDelegationMarkers(ctx context.Context, tx *sql.Tx, id int64, a DelegationMarkerAdvance) (DelegationMarkers, error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO delegation_marker (delegation, unobserved_since, gone_surfaced_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(delegation) DO UPDATE SET
			unobserved_since = excluded.unobserved_since,
			gone_surfaced_at = excluded.gone_surfaced_at,
			updated_at = excluded.updated_at`,
		id, optionalTimeText(a.UnobservedSince), optionalTimeText(a.GoneSurfacedAt), encodeTime(nowUTC()),
	); err != nil {
		return DelegationMarkers{}, err
	}
	return delegationMarkersOf(ctx, tx, id)
}

func delegationMarkersOf(ctx context.Context, q querier, id int64) (DelegationMarkers, error) {
	var m DelegationMarkers
	var unobserved, goneSurfaced Optional[string]
	var updated string
	if err := q.QueryRowContext(ctx,
		`SELECT delegation, unobserved_since, gone_surfaced_at, updated_at FROM delegation_marker WHERE delegation = ?`, id).
		Scan(&m.Delegation, &unobserved, &goneSurfaced, &updated); err != nil {
		return DelegationMarkers{}, err
	}
	var err error
	if m.UnobservedSince, err = optionalTimeValue(unobserved, "unobserved_since"); err != nil {
		return DelegationMarkers{}, err
	}
	if m.GoneSurfacedAt, err = optionalTimeValue(goneSurfaced, "gone_surfaced_at"); err != nil {
		return DelegationMarkers{}, err
	}
	if m.UpdatedAt, err = decodeTime(updated); err != nil {
		return DelegationMarkers{}, err
	}
	return m, nil
}

// DelegationWakes returns every domain wake of a delegation, acknowledged
// or not, oldest first, on TaskWakes' terms.
func (s *Store) DelegationWakes(ctx context.Context, id int64) ([]DomainWake, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	return s.queryDomainWakes(ctx, fmt.Sprintf("the wakes of delegation %d", id),
		domainWakeColumns+` WHERE domain_wake.delegation = ? ORDER BY domain_wake.id`, id)
}

// QueuedDomainWakes returns the queued domain wakes, oldest first, at most
// limit of them, handed out or not, and hands out none, on QueuedWakes'
// terms.
func (s *Store) QueuedDomainWakes(ctx context.Context, limit int) ([]DomainWake, error) {
	if limit < 1 {
		return nil, fmt.Errorf("store: a limit of %d queued domain wakes is not a batch size", limit)
	}
	if err := s.live(); err != nil {
		return nil, err
	}
	return s.queryDomainWakes(ctx, "the queued domain wakes",
		domainWakeColumns+` WHERE domain_wake.handled_at IS NULL ORDER BY domain_wake.id LIMIT ?`, limit)
}

// offeredDomainClause is offeredClause for the second shape: the route's
// half is that a subordinate is offered nothing, since every domain wake is
// about a subordinate and the first coordinator alone acts on one.
func offeredDomainClause(held []string, route WakeRoute) (string, []any, bool) {
	if route.Subordinate != "" {
		return "", nil, false
	}
	clause := `domain_wake.handled_at IS NULL`
	var args []any
	if len(held) > 0 {
		marks := make([]string, len(held))
		for i, h := range held {
			marks[i] = "?"
			args = append(args, h)
		}
		clause += ` AND (domain_wake.handed_out_under IS NULL OR domain_wake.handed_out_under NOT IN (` + strings.Join(marks, ",") + `))`
	}
	return clause, args, true
}

// OfferedDomainWakes reports how many queued domain wakes a drain made now
// with held and route would offer, on OfferedWakes' terms: none on a
// subordinate's route.
func (s *Store) OfferedDomainWakes(ctx context.Context, held []string, route WakeRoute) (int, error) {
	if err := s.live(); err != nil {
		return 0, err
	}
	clause, args, offered := offeredDomainClause(held, route)
	if !offered {
		return 0, nil
	}
	var n int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM domain_wake WHERE `+clause, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: counting the offered domain wakes: %w", err)
	}
	return n, nil
}

// DrainDomainWakes is DrainWakes for the second shape: the queued domain
// wakes on offer, oldest first, at most limit of them, recorded as handed
// out to h.To under h.Under in the transaction that read them and closed by
// nothing but AcknowledgeDomainWakes. A drain on a subordinate's route is
// offered none and writes nothing.
func (s *Store) DrainDomainWakes(ctx context.Context, limit int, h WakeHandout) ([]DomainWake, error) {
	if limit < 1 {
		return nil, fmt.Errorf("store: a drain limit of %d is not a batch size", limit)
	}
	if err := h.validate(); err != nil {
		return nil, fmt.Errorf("store: draining domain wakes: %w", err)
	}
	var out []DomainWake
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = drainDomainWakesTx(ctx, tx, limit, h)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: draining domain wakes: %w", err)
	}
	return out, nil
}

// drainDomainWakesTx is DrainDomainWakes inside a transaction the caller
// holds.
func drainDomainWakesTx(ctx context.Context, tx *sql.Tx, limit int, h WakeHandout) ([]DomainWake, error) {
	clause, args, offered := offeredDomainClause(h.Held, h.Route)
	if !offered {
		return nil, nil
	}
	to, _ := acknowledgerText(h.To)
	rows, err := tx.QueryContext(ctx, `SELECT id FROM domain_wake WHERE `+clause+` ORDER BY id LIMIT ?`, append(args, limit)...)
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
	out := make([]DomainWake, 0, len(ids))
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE domain_wake SET handed_out_at = ?, handed_out_to = ?, handed_out_under = ?,
				handouts = coalesce(handouts, 0) + 1
			WHERE id = ? AND handled_at IS NULL`,
			now, to, h.Under, id); err != nil {
			return nil, err
		}
		drained, err := domainWakeByID(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, drained)
	}
	return out, nil
}

// UnacknowledgedDomainWakes returns how many domain wakes are queued, handed
// out or not: the second shape's half of PRD section 5's "work is in
// flight", which internal/supervisor's InFlightOf reads beside the per-task
// count, so a wake about a subordinate keeps the home in flight until the
// coordinator acknowledges it.
func (s *Store) UnacknowledgedDomainWakes(ctx context.Context) (int, error) {
	if err := s.live(); err != nil {
		return 0, err
	}
	var n int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM domain_wake WHERE handled_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: counting queued domain wakes: %w", err)
	}
	return n, nil
}

// AcknowledgeDomainWakes records that the named domain wakes were handled,
// by whom, on AcknowledgeWakes' terms: every row checked and then every row
// written in one transaction, a *WakeIDError refusing the whole batch for
// an unknown or an already acknowledged identifier, and the zero
// Acknowledger refused with ErrNoAcknowledger.
func (s *Store) AcknowledgeDomainWakes(ctx context.Context, ids []int64, by Acknowledger) error {
	if len(ids) == 0 {
		return errors.New("store: an acknowledgement names no domain wakes")
	}
	who, err := acknowledgerText(by)
	if err != nil {
		return fmt.Errorf("store: acknowledging domain wakes: %w", err)
	}
	now := encodeTime(nowUTC())
	return s.inTx(ctx, func(tx *sql.Tx) error { return acknowledgeDomainWakesTx(ctx, tx, ids, who, now) })
}

// acknowledgeDomainWakesTx is AcknowledgeDomainWakes inside a transaction,
// on acknowledgeWakesTx's terms.
func acknowledgeDomainWakesTx(ctx context.Context, tx *sql.Tx, ids []int64, who, now string) error {
	for _, id := range ids {
		var handled Optional[string]
		err := tx.QueryRowContext(ctx, `SELECT handled_at FROM domain_wake WHERE id = ?`, id).Scan(&handled)
		switch {
		case isNoRows(err):
			return &WakeIDError{ID: id, Reason: ErrWakeUnknown}
		case err != nil:
			return fmt.Errorf("store: acknowledging domain wake %d: %w", id, err)
		case handled.IsKnown():
			return &WakeIDError{ID: id, Reason: ErrWakeAcknowledged}
		}
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE domain_wake SET handled_at = ?, acknowledged_by = ? WHERE id = ? AND handled_at IS NULL`,
			now, who, id); err != nil {
			return fmt.Errorf("store: acknowledging domain wake %d: %w", id, err)
		}
	}
	return nil
}

const domainWakeColumns = `SELECT domain_wake.id, domain_wake.delegation, delegation.domain, domain_wake.kind,
	domain_wake.marker, domain_wake.created_at, domain_wake.handled_at, domain_wake.acknowledged_by,
	domain_wake.handed_out_at, domain_wake.handed_out_to, domain_wake.handed_out_under, domain_wake.handouts
	FROM domain_wake JOIN delegation ON delegation.id = domain_wake.delegation`

func domainWakeByID(ctx context.Context, q querier, id int64) (DomainWake, error) {
	return scanDomainWake(q.QueryRowContext(ctx, domainWakeColumns+` WHERE domain_wake.id = ?`, id))
}

func (s *Store) queryDomainWakes(ctx context.Context, what, query string, args ...any) ([]DomainWake, error) {
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: reading %s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()
	var out []DomainWake
	for rows.Next() {
		w, err := scanDomainWake(rows)
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

func scanDomainWake(sc scanner) (DomainWake, error) {
	var w DomainWake
	var created string
	var handled, by, handedAt, handedTo Optional[string]
	if err := sc.Scan(&w.ID, &w.Delegation, &w.Domain, &w.Kind, &w.Marker, &created, &handled, &by,
		&handedAt, &handedTo, &w.HandedOutUnder, &w.Handouts); err != nil {
		return DomainWake{}, err
	}
	var err error
	if w.CreatedAt, err = decodeTime(created); err != nil {
		return DomainWake{}, err
	}
	if w.HandledAt, err = optionalTimeValue(handled, "handled_at"); err != nil {
		return DomainWake{}, err
	}
	if w.AcknowledgedBy, err = acknowledgerValue(by, "acknowledged_by"); err != nil {
		return DomainWake{}, err
	}
	if w.HandedOutAt, err = optionalTimeValue(handedAt, "handed_out_at"); err != nil {
		return DomainWake{}, err
	}
	if w.HandedOutTo, err = acknowledgerValue(handedTo, "handed_out_to"); err != nil {
		return DomainWake{}, err
	}
	return w, nil
}

// RefuseDomainDrainsForTest plants a fault that makes SQLite abort every
// update of a domain wake, so a drain's second half fails the way it does
// on a store that cannot write, and returns the function that lifts it. It
// is a fault injector on RefuseNotificationClaimsForTest's terms and
// nothing else: no production path plants one, and it takes a testing.TB
// and refuses a nil one, so a production path does not reach it by
// accident; that does not stop a deliberate caller that builds a testing.T
// of its own.
func (s *Store) RefuseDomainDrainsForTest(ctx context.Context, tb testing.TB) (func() error, error) {
	if tb == nil {
		return nil, fmt.Errorf("store: RefuseDomainDrainsForTest is a test's fault injector and takes the test")
	}
	tb.Helper()
	if err := s.live(); err != nil {
		return nil, err
	}
	if _, err := s.write.ExecContext(ctx, `CREATE TRIGGER domain_drain_fault BEFORE UPDATE ON domain_wake
		BEGIN SELECT RAISE(ABORT, 'planted fault'); END`); err != nil {
		return nil, fmt.Errorf("store: planting the domain drain fault: %w", err)
	}
	return func() error {
		if _, err := s.write.ExecContext(context.WithoutCancel(ctx), `DROP TRIGGER domain_drain_fault`); err != nil {
			return fmt.Errorf("store: lifting the domain drain fault: %w", err)
		}
		return nil
	}, nil
}
