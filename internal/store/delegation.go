package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
)

// Delegation is PRD section 8's delegation record: one row per domain the
// coordinator delegated to a subordinate coordinator, with the delivery
// posture the domain's tasks take and the anchor the write was decided
// against. It is written once, and its end is its one revision: a
// delegation stands until EndDelegation writes EndedAt with its reason, and
// nothing else ends one, not a restart, not a dropped connection, not a
// subordinate whose terminal is gone. Which domain's tasks it covers is
// read off each task's own row, never from here (P14).
type Delegation struct {
	// ID is the row's identifier, which the store assigns in the order it
	// accepted the rows, and which an attempt names.
	ID int64 `json:"id"`
	// Domain is the domain delegated, as home.ValidDomain accepts it.
	Domain string `json:"domain"`
	// Delivery is the posture every task the subordinate creates in the
	// domain takes, PRD section 6's "a task it creates takes its
	// delegation's". It is held to the closed set when the row is written;
	// whether it is lighter than a project's is intake's rule, applied
	// when each task is created against that task's project.
	Delivery config.Delivery `json:"delivery"`
	// Anchor is the delegation of the same domain the write was decided
	// against: the highest ID among the domain's rows the caller had seen,
	// zero for a domain never delegated before.
	Anchor int64 `json:"anchor"`
	// CreatedAt is when the row was written.
	CreatedAt time.Time `json:"created_at"`
	// EndedAt is when the delegation ended, and unknown while it stands.
	EndedAt Optional[time.Time] `json:"ended_at"`
	// EndedBy is why it ended, from the closed set DelegationEnd names, and
	// unknown while it stands. It has no encoded form outside this
	// package, on Resolver's terms, so it does not travel.
	EndedBy Optional[DelegationEnd] `json:"-"`
}

// Standing reports whether the delegation has not ended.
func (d Delegation) Standing() bool { return !d.EndedAt.IsKnown() }

// DelegationEnd says why a delegation ended. It is a closed set on
// Acknowledger's terms and for Resolver's reason: a struct with one
// unexported field and no conversion into it, so outside this package the
// only values are the one the function below returns and the zero, and
// nothing decoded from a wire, a file or an agent's output is one. It
// records and does not gate: EndDelegation treats every member identically.
type DelegationEnd struct{ name string }

// EndedByCoordinator is a delegation the coordinator ended through PRD
// section 9's domain.end.
func EndedByCoordinator() DelegationEnd { return DelegationEnd{name: "coordinator"} }

// EndedByAnswer is a delegation ended by the answer to the hold the
// relaunch bound raised for it, PRD section 11's "past the bound it holds
// for you": a stop answer seals the delegation rather than leaving it
// standing with no subordinate. Its one producer is internal/service's
// answer to that hold; who answered is the hold's own Resolver, and this
// member says only that an answer ended it.
func EndedByAnswer() DelegationEnd { return DelegationEnd{name: "answer"} }

// String renders why, or "nobody" for the zero DelegationEnd.
func (e DelegationEnd) String() string {
	if e.name == "" {
		return "nobody"
	}
	return e.name
}

// delegationEnds is the closed set, and the only place it is written down.
func delegationEnds() []DelegationEnd { return []DelegationEnd{EndedByCoordinator(), EndedByAnswer()} }

// delegationEndText returns what the column holds for e: ErrNoDelegationEnd
// for the zero value, and ErrUnknownDelegationEnd, unreachable from outside
// this package, for anything else outside the set.
func delegationEndText(e DelegationEnd) (string, error) {
	if e == (DelegationEnd{}) {
		return "", ErrNoDelegationEnd
	}
	for _, known := range delegationEnds() {
		if e == known {
			return e.name, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownDelegationEnd, e.name)
}

// delegationEndValue turns a stored column back into a DelegationEnd,
// refusing text outside the set rather than fabricating one.
func delegationEndValue(o Optional[string], column string) (Optional[DelegationEnd], error) {
	s, ok := o.Get()
	if !ok {
		return Unknown[DelegationEnd](), nil
	}
	for _, known := range delegationEnds() {
		if s == known.name {
			return Known(known), nil
		}
	}
	return Unknown[DelegationEnd](), fmt.Errorf("store: column %s: %w: %q", column, ErrUnknownDelegationEnd, s)
}

// DelegationRequest is what the coordinator's delegation asks to record.
type DelegationRequest struct {
	// Domain is the domain, as home.ValidDomain accepts it.
	Domain string
	// Delivery is the posture the domain's tasks take, from the closed set.
	Delivery config.Delivery
	// Expected is the highest Delegation.ID the caller saw among the
	// domain's rows, and zero when it saw none. The write is refused with a
	// *DelegationMovedError when the domain's rows stand elsewhere, decided
	// in the same transaction as the insert, and with ErrDomainDelegated
	// when the domain's latest delegation still stands.
	Expected int64
}

// Delegate records a new delegation on DelegationRequest's terms and returns
// the row as stored. A domain home.ValidDomain refuses and a posture outside
// config.Delivery's closed set are refused before anything is read.
func (s *Store) Delegate(ctx context.Context, req DelegationRequest) (Delegation, error) {
	if err := home.ValidDomain(req.Domain); err != nil {
		return Delegation{}, fmt.Errorf("store: delegating: %w", err)
	}
	if !req.Delivery.Recognized() {
		return Delegation{}, fmt.Errorf("store: delegating domain %s: %w: %q", req.Domain, ErrUnknownDelivery, req.Delivery)
	}
	var out Delegation
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var stands int64
		var standing int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(id), 0), COUNT(*) FILTER (WHERE ended_at IS NULL) FROM delegation WHERE domain = ?`,
			req.Domain).Scan(&stands, &standing); err != nil {
			return err
		}
		if stands != req.Expected {
			return &DelegationMovedError{Domain: req.Domain, Expected: req.Expected, Stands: stands}
		}
		if standing > 0 {
			return fmt.Errorf("%w: domain %s, delegation %d", ErrDomainDelegated, req.Domain, stands)
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO delegation (domain, delivery, anchor, created_at) VALUES (?, ?, ?, ?)`,
			req.Domain, string(req.Delivery), req.Expected, encodeTime(nowUTC()))
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, err = delegationByID(ctx, tx, id)
		return err
	})
	if err != nil {
		var moved *DelegationMovedError
		if errors.As(err, &moved) || errors.Is(err, ErrDomainDelegated) {
			return Delegation{}, err
		}
		return Delegation{}, fmt.Errorf("store: delegating domain %s: %w", req.Domain, err)
	}
	return out, nil
}

// EndDelegation writes the delegation's one revision: its end, now, with
// why. A delegation already ended is refused with ErrDelegationEnded, an
// unknown identifier is ErrNotFound, and the zero DelegationEnd is refused
// with ErrNoDelegationEnd, all with nothing written. It records who and
// does not gate on it. The holds closing names are answered in the same
// transaction on DelegationEndClosure's terms, and the keys it closed are
// returned; a closure naming keys with no resolution is ErrNoResolution and
// one naming nobody is ErrNoResolver, before anything is written.
func (s *Store) EndDelegation(ctx context.Context, id int64, by DelegationEnd, closing DelegationEndClosure) (Delegation, []string, error) {
	why, err := delegationEndText(by)
	if err != nil {
		return Delegation{}, nil, fmt.Errorf("store: ending delegation %d: %w", id, err)
	}
	var resolver string
	if len(closing.Keys) > 0 {
		if strings.TrimSpace(closing.Resolution) == "" {
			return Delegation{}, nil, fmt.Errorf("%w: ending delegation %d", ErrNoResolution, id)
		}
		if resolver, err = resolverText(closing.By); err != nil {
			return Delegation{}, nil, fmt.Errorf("store: ending delegation %d: %w", id, err)
		}
	}
	var noteCol any
	if strings.TrimSpace(closing.Note) != "" {
		noteCol = s.redact.Redact(closing.Note)
	}
	var out Delegation
	var closed []string
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		d, err := delegationByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if !d.Standing() {
			return fmt.Errorf("%w: delegation %d of domain %s", ErrDelegationEnded, id, d.Domain)
		}
		now := encodeTime(nowUTC())
		if _, err := tx.ExecContext(ctx, `UPDATE delegation SET ended_at = ?, end_reason = ? WHERE id = ?`, now, why, id); err != nil {
			return err
		}
		for _, key := range closing.Keys {
			result, err := tx.ExecContext(ctx,
				`UPDATE hold SET resolution = ?, resolved_at = ?, resolved_by = ?, resolution_note = ? WHERE key = ? AND resolution IS NULL`,
				closing.Resolution, now, resolver, noteCol, key)
			if err != nil {
				return fmt.Errorf("closing hold %s: %w", key, err)
			}
			if n, err := result.RowsAffected(); err != nil {
				return err
			} else if n == 1 {
				closed = append(closed, key)
			}
		}
		out, err = delegationByID(ctx, tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, ErrDelegationEnded) || errors.Is(err, ErrNotFound) {
			return Delegation{}, nil, err
		}
		return Delegation{}, nil, fmt.Errorf("store: ending delegation %d: %w", id, err)
	}
	return out, closed, nil
}

// DelegationEndClosure is the holds a delegation's end answers in the same
// transaction, so the end and the answer are one write and neither is
// recorded without the other. It is for a hold whose question the end
// itself answers, as internal/service's relaunch-bound hold asks whether to
// end the delegation; the resolution is the caller's, recorded under the
// Resolver it names, and nothing here decides that an end answers a hold.
// Each key still open is resolved on ResolveHoldNoted's terms and reported
// closed; a key already resolved, or registered nowhere, is left as it
// stands and not reported, since the end is the fact the transaction exists
// for. The zero value closes nothing.
type DelegationEndClosure struct {
	// Keys are the holds to close.
	Keys []string
	// Resolution is what the end decided for them, refused empty with
	// ErrNoResolution when Keys is not.
	Resolution string
	// Note is free text recorded with the resolution, redacted, and empty
	// leaves ResolutionNote unknown.
	Note string
	// By is who decided, refused as the zero Resolver when Keys is not
	// empty.
	By Resolver
}

// Delegation returns the domain's latest delegation, standing or ended, and
// whether the domain was ever delegated. It is what a caller anchors
// Delegate against and reads a standing delegation from.
func (s *Store) Delegation(ctx context.Context, domain string) (Delegation, bool, error) {
	if err := s.live(); err != nil {
		return Delegation{}, false, err
	}
	d, err := scanDelegation(s.read.QueryRowContext(ctx,
		delegationColumns+` FROM delegation WHERE domain = ? ORDER BY id DESC LIMIT 1`, domain))
	if isNoRows(err) {
		return Delegation{}, false, nil
	}
	if err != nil {
		return Delegation{}, false, fmt.Errorf("store: reading the delegation of domain %s: %w", domain, err)
	}
	return d, true, nil
}

// DelegationByID returns one delegation by identifier, or ErrNotFound.
func (s *Store) DelegationByID(ctx context.Context, id int64) (Delegation, error) {
	if err := s.live(); err != nil {
		return Delegation{}, err
	}
	d, err := delegationByID(ctx, s.read, id)
	if err != nil {
		return Delegation{}, fmt.Errorf("store: reading delegation %d: %w", id, err)
	}
	return d, nil
}

// StandingDelegations returns every delegation that stands, oldest first.
func (s *Store) StandingDelegations(ctx context.Context) ([]Delegation, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, delegationColumns+` FROM delegation WHERE ended_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: listing the standing delegations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Delegation
	for rows.Next() {
		d, err := scanDelegation(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing the standing delegations: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing the standing delegations: %w", err)
	}
	return out, nil
}

const delegationColumns = `SELECT id, domain, delivery, anchor, created_at, ended_at, end_reason`

func delegationByID(ctx context.Context, q querier, id int64) (Delegation, error) {
	d, err := scanDelegation(q.QueryRowContext(ctx, delegationColumns+` FROM delegation WHERE id = ?`, id))
	if isNoRows(err) {
		return Delegation{}, fmt.Errorf("%w: delegation %d", ErrNotFound, id)
	}
	return d, err
}

func scanDelegation(sc scanner) (Delegation, error) {
	var d Delegation
	var delivery, created string
	var ended, reason Optional[string]
	if err := sc.Scan(&d.ID, &d.Domain, &delivery, &d.Anchor, &created, &ended, &reason); err != nil {
		return Delegation{}, err
	}
	// A posture outside the set was not written by Delegate, and reading it
	// back as a posture would turn a row nobody can account for into one.
	if !config.Delivery(delivery).Recognized() {
		return Delegation{}, fmt.Errorf("column delivery: %w: %q", ErrUnknownDelivery, delivery)
	}
	d.Delivery = config.Delivery(delivery)
	var err error
	if d.CreatedAt, err = decodeTime(created); err != nil {
		return Delegation{}, err
	}
	if d.EndedAt, err = optionalTimeValue(ended, "ended_at"); err != nil {
		return Delegation{}, err
	}
	if d.EndedBy, err = delegationEndValue(reason, "end_reason"); err != nil {
		return Delegation{}, err
	}
	return d, nil
}

// DelegationAttempt is one start of a domain's subordinate coordinator, on
// TaskAttempt's terms: history, appended and never revised, with the one
// exception stated on SessionRef.
type DelegationAttempt struct {
	// Delegation names the delegation.
	Delegation int64 `json:"delegation"`
	// Sequence is the attempt's position in the delegation's attempts,
	// counted from one, with no gaps.
	Sequence int64 `json:"sequence"`
	// StartedAt is when the attempt was recorded, which precedes the
	// process it is about.
	StartedAt time.Time `json:"started_at"`
	// SessionRef is the terminal session the subordinate runs in. It is
	// unknown until SetDelegationAttemptSession fills it, once, after the
	// process starts; an attempt whose process never started keeps it
	// unknown.
	SessionRef Optional[string] `json:"session_ref"`
	// Host is the identity of the process hosting the session's terminal,
	// recorded with SessionRef, and zero for a session with none or one
	// recorded before migration 49.
	Host SessionHost `json:"host"`
}

// SessionHost is the identity of the process hosting a terminal session, a
// tmux server's, as the delegation attempt records it: the identifier and
// the start time internal/agents' Identity carries, which is what a
// placement by ancestry compares. The zero value is no host.
type SessionHost struct {
	// PID is the host process's identifier.
	PID int `json:"pid"`
	// Start is the platform's reading of its start time.
	Start uint64 `json:"start"`
}

// Known reports whether the host names a process.
func (h SessionHost) Known() bool { return h.PID > 0 }

// BeginDelegationAttempt records the next attempt of a delegation, anchored
// to the highest attempt sequence the caller had seen, on BeginTaskAttempt's
// terms: two callers that both saw the same latest attempt begin one between
// them, and the other is refused with a *DelegationMovedError. A delegation
// that does not exist is ErrNotFound, wrapped, and one that has ended is
// ErrDelegationEnded: nothing starts a subordinate for a delegation that
// no longer stands.
func (s *Store) BeginDelegationAttempt(ctx context.Context, id int64, expectedLatest int64) (DelegationAttempt, error) {
	now := nowUTC()
	var sequence int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		d, err := delegationByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if !d.Standing() {
			return fmt.Errorf("%w: delegation %d of domain %s", ErrDelegationEnded, id, d.Domain)
		}
		var stands int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sequence), 0) FROM delegation_attempt WHERE delegation = ?`, id).Scan(&stands); err != nil {
			return err
		}
		if stands != expectedLatest {
			return &DelegationMovedError{Domain: d.Domain, Attempt: true, Expected: expectedLatest, Stands: stands}
		}
		sequence = stands + 1
		_, err = tx.ExecContext(ctx,
			`INSERT INTO delegation_attempt (delegation, sequence, started_at, session_ref) VALUES (?, ?, ?, NULL)`,
			id, sequence, encodeTime(now))
		return err
	})
	if err != nil {
		var moved *DelegationMovedError
		if errors.As(err, &moved) || errors.Is(err, ErrDelegationEnded) || errors.Is(err, ErrNotFound) {
			return DelegationAttempt{}, err
		}
		return DelegationAttempt{}, fmt.Errorf("store: beginning an attempt of delegation %d: %w", id, err)
	}
	return DelegationAttempt{Delegation: id, Sequence: sequence, StartedAt: now}, nil
}

// SetDelegationAttemptSession records the session the attempt's process runs
// in, once, with the identity of the process hosting its terminal, zero for
// none, on SetAttemptSession's terms: a second call is refused with
// ErrAttemptSessionRecorded, an empty reference is refused, and an attempt
// that does not exist is ErrNotFound, wrapped. A delegation that has ended
// is ErrDelegationEnded, read in the same transaction.
func (s *Store) SetDelegationAttemptSession(ctx context.Context, id, sequence int64, sessionRef string, host SessionHost) error {
	if strings.TrimSpace(sessionRef) == "" {
		return fmt.Errorf("store: delegation %d attempt %d: the session reference is empty", id, sequence)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		// The delegation is read in the transaction that records the
		// session, so a session started for a delegation that was ended
		// while it started is refused here and never recorded as one
		// standing: the caller then ends the session it holds.
		d, err := delegationByID(ctx, tx, id)
		if err != nil {
			return err
		}
		if !d.Standing() {
			return fmt.Errorf("%w: delegation %d of domain %s, so attempt %d's session is not recorded", ErrDelegationEnded, id, d.Domain, sequence)
		}
		var recorded Optional[string]
		err = tx.QueryRowContext(ctx,
			`SELECT session_ref FROM delegation_attempt WHERE delegation = ? AND sequence = ?`, id, sequence).Scan(&recorded)
		if err != nil {
			return fmt.Errorf("store: delegation %d attempt %d: %w", id, sequence, errNoRows(err))
		}
		if recorded.IsKnown() {
			return fmt.Errorf("store: delegation %d attempt %d: %w", id, sequence, ErrAttemptSessionRecorded)
		}
		var hostPID, hostStart any
		if host.Known() {
			hostPID, hostStart = host.PID, int64(host.Start)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE delegation_attempt SET session_ref = ?, host_pid = ?, host_start = ? WHERE delegation = ? AND sequence = ?`,
			sessionRef, hostPID, hostStart, id, sequence); err != nil {
			return fmt.Errorf("store: delegation %d attempt %d: recording its session: %w", id, sequence, err)
		}
		return nil
	})
}

// DelegationAttempts returns a delegation's attempts in sequence order, and
// nothing for one that has none or does not exist.
func (s *Store) DelegationAttempts(ctx context.Context, id int64) ([]DelegationAttempt, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx,
		`SELECT delegation, sequence, started_at, session_ref, host_pid, host_start FROM delegation_attempt WHERE delegation = ? ORDER BY sequence`, id)
	if err != nil {
		return nil, fmt.Errorf("store: reading the attempts of delegation %d: %w", id, err)
	}
	defer func() { _ = rows.Close() }()
	var out []DelegationAttempt
	for rows.Next() {
		var a DelegationAttempt
		var started string
		var hostPID, hostStart Optional[int64]
		if err := rows.Scan(&a.Delegation, &a.Sequence, &started, &a.SessionRef, &hostPID, &hostStart); err != nil {
			return nil, fmt.Errorf("store: reading the attempts of delegation %d: %w", id, err)
		}
		if pid, ok := hostPID.Get(); ok && pid > 0 {
			a.Host = SessionHost{PID: int(pid), Start: uint64(hostStart.Or(0))}
		}
		if a.StartedAt, err = decodeTime(started); err != nil {
			return nil, fmt.Errorf("store: reading the attempts of delegation %d: %w", id, err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the attempts of delegation %d: %w", id, err)
	}
	return out, nil
}

// Escalation is PRD section 8's escalation record: one decision a
// subordinate coordinator handed up to the first coordinator about a task
// in its domain, written once and sealed. The supervisor loop raises it as
// a wake of its own kind, reading this table and never the history (P8).
type Escalation struct {
	// ID is the row's identifier, which the store assigns and a wake's
	// marker names.
	ID int64 `json:"id"`
	// TaskID names the task the decision is about.
	TaskID string `json:"task_id"`
	// Text is what was handed up, with credentials removed by the redactor
	// the store was opened with, before the row was written.
	Text string `json:"text"`
	// CreatedAt is when the row was written.
	CreatedAt time.Time `json:"created_at"`
}

// RecordEscalation writes one escalation about the task, with the text
// passed through the store's redactor, and returns it as stored. Empty text
// is refused, and a task that does not exist is ErrNotFound, wrapped. Which
// domain the escalation belongs to is the task's row and is not recorded
// again here (P14).
func (s *Store) RecordEscalation(ctx context.Context, taskID, text string) (Escalation, error) {
	if strings.TrimSpace(text) == "" {
		return Escalation{}, fmt.Errorf("store: an escalation about task %s carries no text", taskID)
	}
	redacted := s.redact.Redact(text)
	var out Escalation
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := taskExists(ctx, tx, taskID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO escalation (task_id, text, created_at) VALUES (?, ?, ?)`,
			taskID, redacted, encodeTime(nowUTC()))
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, err = escalationByID(ctx, tx, id)
		return err
	})
	if err != nil {
		return Escalation{}, fmt.Errorf("store: recording an escalation about task %s: %w", taskID, err)
	}
	return out, nil
}

// TaskEscalations returns every escalation about a task, oldest first.
func (s *Store) TaskEscalations(ctx context.Context, taskID string) ([]Escalation, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, escalationColumns+` FROM escalation WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: reading the escalations about task %s: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Escalation
	for rows.Next() {
		e, err := scanEscalation(rows)
		if err != nil {
			return nil, fmt.Errorf("store: reading the escalations about task %s: %w", taskID, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the escalations about task %s: %w", taskID, err)
	}
	return out, nil
}

const escalationColumns = `SELECT id, task_id, text, created_at`

func escalationByID(ctx context.Context, q querier, id int64) (Escalation, error) {
	return scanEscalation(q.QueryRowContext(ctx, escalationColumns+` FROM escalation WHERE id = ?`, id))
}

func scanEscalation(sc scanner) (Escalation, error) {
	var e Escalation
	var created string
	if err := sc.Scan(&e.ID, &e.TaskID, &e.Text, &created); err != nil {
		return Escalation{}, err
	}
	var err error
	if e.CreatedAt, err = decodeTime(created); err != nil {
		return Escalation{}, err
	}
	return e, nil
}
