package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// HousekeepingRetirement is PRD section 8's housekeeping retirement record:
// written once per housekeeping pass that ends without a terminal record,
// naming the retirement hold a person's answer of retire closed, and when.
// It is the one record that ends a pass's claim on its repository other
// than the terminal tier, and a person's answer is the one thing that
// writes it: the program retires no pass on its own. The state resolver
// does not read it, so a retired pass still resolves the state its own
// records give; the one-open-pass rule, the due check and every attempt of
// the pass read it.
type HousekeepingRetirement struct {
	// Task is the retired pass.
	Task string `json:"task"`
	// Hold is the retirement hold the answer of retire closed.
	Hold string `json:"hold"`
	// RecordedAt is when the record was written.
	RecordedAt time.Time `json:"recorded_at"`
}

const retirementColumns = `SELECT task_id, hold_key, recorded_at FROM housekeeping_retirement`

func scanRetirement(sc scanner) (HousekeepingRetirement, error) {
	var r HousekeepingRetirement
	var recorded string
	if err := sc.Scan(&r.Task, &r.Hold, &recorded); err != nil {
		return HousekeepingRetirement{}, err
	}
	at, err := decodeTime(recorded)
	if err != nil {
		return HousekeepingRetirement{}, err
	}
	r.RecordedAt = at
	return r, nil
}

// retirementOf reads the retirement record of a task, and false when it has
// none.
func retirementOf(ctx context.Context, q querier, task string) (HousekeepingRetirement, bool, error) {
	r, err := scanRetirement(q.QueryRowContext(ctx, retirementColumns+` WHERE task_id = ?`, task))
	switch {
	case isNoRows(err):
		return HousekeepingRetirement{}, false, nil
	case err != nil:
		return HousekeepingRetirement{}, false, fmt.Errorf("reading the retirement record of task %s: %w", task, err)
	}
	return r, true, nil
}

// HousekeepingPasses reads every housekeeping pass of the repository as
// TaskSnapshot reads one, newest first, as of one point in time.
func (s *Store) HousekeepingPasses(ctx context.Context, repository string) ([]TaskSnapshot, error) {
	var out []TaskSnapshot
	err := s.snapshotTx(ctx, func(q querier) error {
		var err error
		out, err = passesOf(ctx, q, repository)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: reading the housekeeping passes of repository %s: %w", repository, err)
	}
	return out, nil
}

// passesOf reads the repository's passes inside the caller's transaction,
// newest first.
func passesOf(ctx context.Context, q querier, repository string) ([]TaskSnapshot, error) {
	rows, err := q.QueryContext(ctx, taskColumns+` FROM task WHERE repository_id = ? AND housekeeping = 1
		ORDER BY created_at DESC, rowid DESC`, repository)
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		tasks = append(tasks, t)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]TaskSnapshot, 0, len(tasks))
	for _, t := range tasks {
		snap, err := snapshotOf(ctx, q, t)
		if err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, nil
}

// PassOpen reports whether a housekeeping pass's snapshot leaves it open on
// PRD section 6's terms: neither retired nor, by delivered, in the terminal
// tier. It is the one statement of the rule, for CreateTask's transaction
// and every reader alike.
func PassOpen(snap TaskSnapshot, delivered func(TaskSnapshot) bool) bool {
	return snap.Task.Housekeeping && !snap.Retirement.IsKnown() && !delivered(snap)
}

// openPassTx names the newest open pass of the repository, read inside the
// creating transaction, or unknown when every pass is closed.
func openPassTx(ctx context.Context, tx *sql.Tx, repository string, delivered func(TaskSnapshot) bool) (Optional[string], error) {
	passes, err := passesOf(ctx, tx, repository)
	if err != nil {
		return Optional[string]{}, fmt.Errorf("reading the housekeeping passes of repository %s: %w", repository, err)
	}
	for _, p := range passes {
		if PassOpen(p, delivered) {
			return Known(p.Task.ID), nil
		}
	}
	return Optional[string]{}, nil
}

// refuseRetiredTx refuses, with ErrPassRetired, a task whose retirement is
// recorded.
func refuseRetiredTx(ctx context.Context, tx *sql.Tx, task string) error {
	_, retired, err := retirementOf(ctx, tx, task)
	if err != nil {
		return err
	}
	if retired {
		return fmt.Errorf("%w: task %s", ErrPassRetired, task)
	}
	return nil
}

// Retirement is what RetireHousekeepingPass is asked to record, and what the
// caller read when it decided nothing moves the pass: the pass's latest
// attempt, that attempt's session and the newest attributed run as they
// stood. Hold is the answered retirement hold it is retired on.
type Retirement struct {
	// Task is the pass.
	Task string
	// Attempt is the pass's latest attempt sequence as the caller read it,
	// and zero for a pass never dispatched. A pass whose latest attempt has
	// moved since was relaunched, which moves it.
	Attempt int64
	// Session is that attempt's session reference as the caller read it,
	// and empty for an attempt with none or a pass never dispatched. An
	// attempt's session is recorded once, by its launch after the worker's
	// process has started, so a session recorded since the caller read none
	// is a launch under way at that reading, which moves the pass.
	Session string
	// Run is the newest run attributed to the pass as the caller read it,
	// and empty for none. A newer one has started since, which moves it.
	Run string
	// Hold is the open retirement hold the answer of retire closes, with
	// Note and By as the answer's note and resolver.
	Hold string
	Note string
	By   Resolver
	// Relaunches reports whether an open hold key offers to relaunch the
	// pass's worker; the retirement closes each with AnswerStop under By.
	// Its owner is internal/service, which keys those holds.
	Relaunches func(key string) bool
	// Ended reports whether a run status is one a run has ended in, which
	// internal/runs' move table owns.
	Ended func(RunStatus) bool
	// Delivered is internal/taskstate's terminal tier, on Disposition's
	// terms.
	Delivered func(TaskSnapshot) bool
}

// AnswerStop is the answer RetireHousekeepingPass records on every open
// hold of the pass whose retry would relaunch its worker, PRD section 6's
// "a retire that writes the record also answers stop", the word the hold
// answer of every such hold reads as no relaunch.
const AnswerStop = "stop"

// AnswerRetire is the answer an answered retirement records on the
// retirement hold it closes.
const AnswerRetire = "retire"

func (r Retirement) validate() error {
	switch {
	case strings.TrimSpace(r.Task) == "":
		return errors.New("store: a retirement names no pass")
	case r.Relaunches == nil || r.Ended == nil || r.Delivered == nil:
		return fmt.Errorf("store: retiring pass %s: what relaunches a worker, what ends a run and what delivers a task are decided outside this package, and one was not passed", r.Task)
	case strings.TrimSpace(r.Hold) == "":
		return fmt.Errorf("store: retiring pass %s: a retirement names the retirement hold it answers", r.Task)
	}
	if _, err := resolverText(r.By); err != nil {
		return fmt.Errorf("store: retiring pass %s: %w", r.Task, err)
	}
	return nil
}

// Retired is RetireHousekeepingPass's answer: the record as written and
// every hold it closed, the retirement hold first.
type Retired struct {
	Record HousekeepingRetirement
	Closed []Hold
}

// RetireHousekeepingPass writes a pass's retirement record, once, in one
// transaction that reads the pass again first, PRD section 6's "written only
// while nothing moves the pass ... decided under the store's
// serialization". It refuses, writing nothing:
//
//   - with ErrPassClosed, wrapped, a pass already retired or in the terminal
//     tier, and an error for a task that is not a pass;
//   - with ErrPassMoves, wrapped, a pass whose latest attempt, that
//     attempt's session, or newest attributed run is not the one the caller
//     read, or with a run attributed to it that has not ended;
//   - with ErrHoldResolved, wrapped, a retirement whose hold is no longer
//     open, and ErrNotFound for one that does not exist;
//   - with ErrNotRetirementHold, wrapped, a hold that is not the pass's
//     retirement hold as OfferRetirementHold registered and marked it, so
//     only that hold's answer releases the pass's claim.
//
// It resolves the retirement hold retire, then every other open hold of
// the pass Relaunches names stop, each under the same resolver, and writes
// the record naming the hold. Whether the worker is dead is
// the probe's, which no transaction can take; the attempt anchor is what
// keeps a relaunch since the caller's probe from slipping past, and the
// session anchor what keeps a launch under way at the caller's reading from
// doing so: a session recorded since the caller read none refuses here, and
// SetAttemptSession refuses once the record is written, so the two writes
// have exactly one loser in either order: no session is recorded for an
// attempt of a retired pass after its retirement, and a retired pass's
// latest attempt either has no session or has the one its retirement was
// decided against, which the caller had read dead.
func (s *Store) RetireHousekeepingPass(ctx context.Context, r Retirement) (Retired, error) {
	if err := r.validate(); err != nil {
		return Retired{}, err
	}
	var noteCol any
	if strings.TrimSpace(r.Note) != "" {
		noteCol = s.redact.Redact(r.Note)
	}
	var out Retired
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		task, err := scanTask(tx.QueryRowContext(ctx, taskColumns+` FROM task WHERE id = ?`, r.Task))
		if err != nil {
			return errNoRows(err)
		}
		if !task.Housekeeping {
			return fmt.Errorf("task %s is not a housekeeping pass, so it has no retirement", r.Task)
		}
		snap, err := snapshotOf(ctx, tx, task)
		if err != nil {
			return err
		}
		if !PassOpen(snap, r.Delivered) {
			return ErrPassClosed
		}
		if err := passStillTx(snap, r); err != nil {
			return err
		}
		now := encodeTime(nowUTC())
		resolver, _ := resolverText(r.By)
		closed, err := closeRetirementHoldsTx(ctx, tx, snap, r, resolver, noteCol, now)
		if err != nil {
			return err
		}
		for _, key := range closed {
			h, err := scanHold(tx.QueryRowContext(ctx, holdColumns+` FROM hold WHERE key = ?`, key))
			if err != nil {
				return err
			}
			out.Closed = append(out.Closed, h)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO housekeeping_retirement (task_id, hold_key, recorded_at) VALUES (?, ?, ?)`,
			r.Task, r.Hold, now); err != nil {
			return err
		}
		record, _, err := retirementOf(ctx, tx, r.Task)
		out.Record = record
		return err
	})
	if err != nil {
		return Retired{}, fmt.Errorf("store: retiring housekeeping pass %s: %w", r.Task, err)
	}
	return out, nil
}

// passStillTx refuses, with ErrPassMoves, a pass that something moves as
// the retirement's transaction reads it, on RetireHousekeepingPass's terms.
func passStillTx(snap TaskSnapshot, r Retirement) error {
	var latest int64
	if n := len(snap.Attempts); n > 0 {
		latest = snap.Attempts[n-1].Sequence
	}
	if latest != r.Attempt {
		return fmt.Errorf("%w: its latest attempt is %d where the retirement was decided against %d, so its worker was begun again since",
			ErrPassMoves, latest, r.Attempt)
	}
	if n := len(snap.Attempts); n > 0 {
		if session := snap.Attempts[n-1].SessionRef.Or(""); session != r.Session {
			return fmt.Errorf("%w: its latest attempt %d runs in session %q where the retirement was decided against %q, so a launch of it was under way",
				ErrPassMoves, latest, session, r.Session)
		}
	}
	newest := ""
	if len(snap.Runs) > 0 {
		newest = snap.Runs[0].ID
	}
	if newest != r.Run {
		return fmt.Errorf("%w: run %s is the newest attributed to it where the retirement was decided against %q, so a run started since",
			ErrPassMoves, newest, r.Run)
	}
	for _, run := range snap.Runs {
		if !r.Ended(run.Status) {
			return fmt.Errorf("%w: run %s attributed to it is %s and has not ended", ErrPassMoves, run.ID, run.Status)
		}
	}
	return nil
}

// closeRetirementHoldsTx resolves an answered retirement's hold retire and
// every other open hold of the pass that relaunches its worker stop, under
// one resolver, and returns their keys in that order.
func closeRetirementHoldsTx(ctx context.Context, tx *sql.Tx, snap TaskSnapshot, r Retirement, resolver string, note any, now string) ([]string, error) {
	resolve := func(key, answer string, note any) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE hold SET resolution = ?, resolved_at = ?, resolved_by = ?, resolution_note = ? WHERE key = ? AND resolution IS NULL`,
			answer, now, resolver, note, key)
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
			return fmt.Errorf("hold %s: %w", key, ErrNotFound)
		case err != nil:
			return err
		}
		return fmt.Errorf("hold %s: %w with %s", key, ErrHoldResolved, existing.Or(""))
	}
	// The hold answered retire is the pass's own retirement hold, the one
	// OfferRetirementHold marked: no other hold of the pass, a report's
	// decision or a relaunch's, releases its claim on the repository.
	var owner Optional[string]
	var marked Optional[int64]
	switch err := tx.QueryRowContext(ctx, `SELECT task_id, housekeeping_retirement FROM hold WHERE key = ?`, r.Hold).Scan(&owner, &marked); {
	case isNoRows(err):
		return nil, fmt.Errorf("hold %s: %w", r.Hold, ErrNotFound)
	case err != nil:
		return nil, err
	}
	if owner.Or("") != snap.Task.ID || marked.Or(0) != 1 {
		return nil, fmt.Errorf("%w: hold %s is not the retirement hold OfferRetirementHold registered for pass %s", ErrNotRetirementHold, r.Hold, snap.Task.ID)
	}
	if err := resolve(r.Hold, AnswerRetire, note); err != nil {
		return nil, err
	}
	closed := []string{r.Hold}
	for _, h := range snap.Holds {
		if !h.Open() || h.Key == r.Hold || h.TaskID.Or("") != snap.Task.ID || !r.Relaunches(h.Key) {
			continue
		}
		if err := resolve(h.Key, AnswerStop, "the retire answer to "+r.Hold+" is also the decision not to begin the work again"); err != nil {
			return nil, err
		}
		closed = append(closed, h.Key)
	}
	return closed, nil
}

// RetirementOffer is a retirement hold offered to a housekeeping pass, with
// what its caller read when it decided the pass open and that nothing moves
// it: the pass's latest attempt and newest
// attributed run, as RetireHousekeepingPass's Retirement carries them.
type RetirementOffer struct {
	// Hold is the hold to register. It names the pass and no run.
	Hold Hold
	// Attempt is the pass's latest attempt sequence as the caller read it,
	// and zero for a pass never dispatched.
	Attempt int64
	// Session is that attempt's session reference as the caller read it, on
	// Retirement's terms: empty for none.
	Session string
	// Run is the newest run attributed to the pass as the caller read it,
	// and empty for none.
	Run string
	// Ended and Delivered are Retirement's.
	Ended     func(RunStatus) bool
	Delivered func(TaskSnapshot) bool
}

// OfferRetirementHold registers a housekeeping pass's retirement hold, on
// RegisterHold's terms, fenced by the supervisor loop's incarnation, in one
// transaction that reads the pass again first, so the hold is registered
// only while what the caller decided it on still holds: PRD section 6's "an
// open pass ... that nothing moves gets a hold". It refuses, writing
// nothing:
//
//   - ErrStaleIncarnation, wrapped, for a loop the watchdog replaced;
//   - a *TaskMovedError for a pass whose latest attempt is not Attempt,
//     begun again since the caller read it;
//   - ErrPassRetired, wrapped, for a pass already retired, whose claim is
//     released;
//   - ErrPassClosed, wrapped, for a pass whose records put it in the
//     terminal tier by Delivered, a findings relayed record or a merge
//     among them;
//   - ErrPassMoves, wrapped, for a pass with a run attributed to it newer
//     than Run or one that has not ended, or whose latest attempt's session
//     is not Session, on RetireHousekeepingPass's terms;
//
// and an error for a task that is not a pass. A retire answer to a hold
// registered past any of those could only be refused. Whether the worker is
// dead is the probe's, which no transaction can take; the attempt and
// session anchors are what keep a relaunch, or a launch under way, since the
// loop's probe from slipping past, as they are for the retirement itself.
// The hold is marked as the retirement hold in the same transaction, and
// every attempt claim, the launch's claim of its start (ClaimAttemptLaunch)
// and the record of its session (SetAttemptSession) refuse a pass with one
// open (ErrPassHeld). So an attempt claimed after the offer begins nothing;
// an offer after the claim and before the launch's claim leaves the launch
// refused before any worker starts; an offer after that and before the
// session's record leaves the record refused, and its caller ends the worker
// it started; and an offer after the record is refused on its session
// anchor. No worker is recorded running in a pass that resolves Held from
// this hold, which is not in flight (P10); between a refused record and its
// caller ending the worker, that worker runs with no record naming it.
func (s *Store) OfferRetirementHold(ctx context.Context, inc Incarnation, o RetirementOffer) (Hold, error) {
	h := o.Hold
	if err := validateHold(h); err != nil {
		return Hold{}, err
	}
	task, ok := h.TaskID.Get()
	if !ok || h.RunID.IsKnown() {
		return Hold{}, fmt.Errorf("store: hold %s: a retirement hold names a pass and no run", h.Key)
	}
	if o.Ended == nil || o.Delivered == nil {
		return Hold{}, fmt.Errorf("store: offering hold %s: what ends a run and what delivers a task are decided outside this package, and one was not passed", h.Key)
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := fenceIncarnation(ctx, tx, inc); err != nil {
			return err
		}
		var stands int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) FROM task_attempt WHERE task_id = ?`, task).
			Scan(&stands); err != nil {
			return err
		}
		if stands != o.Attempt {
			return &TaskMovedError{Task: task, Anchor: AnchoredAttempt, Expected: o.Attempt, Stands: stands}
		}
		record, err := scanTask(tx.QueryRowContext(ctx, taskColumns+` FROM task WHERE id = ?`, task))
		if err != nil {
			return errNoRows(err)
		}
		if !record.Housekeeping {
			return fmt.Errorf("task %s is not a housekeeping pass, so it has no retirement hold", task)
		}
		// The pass is read in the registering transaction, so a retirement,
		// a terminal record and a run's creation are each wholly ordered
		// with the registration.
		snap, err := snapshotOf(ctx, tx, record)
		if err != nil {
			return err
		}
		if snap.Retirement.IsKnown() {
			return fmt.Errorf("%w: task %s", ErrPassRetired, task)
		}
		if !PassOpen(snap, o.Delivered) {
			return fmt.Errorf("%w: task %s is in the terminal tier", ErrPassClosed, task)
		}
		if err := passStillTx(snap, Retirement{Task: task, Attempt: o.Attempt, Session: o.Session, Run: o.Run, Ended: o.Ended}); err != nil {
			return err
		}
		if err := insertHold(ctx, tx, h); err != nil {
			return err
		}
		// The mark every attempt claim reads (anchorTaskAttemptTx), written
		// in the registering transaction, so the offer and a claim have
		// exactly one loser in either order.
		_, err = tx.ExecContext(ctx, `UPDATE hold SET housekeeping_retirement = 1 WHERE key = ?`, h.Key)
		return err
	})
	if err != nil {
		var moved *TaskMovedError
		if errors.As(err, &moved) {
			return Hold{}, err
		}
		return Hold{}, fmt.Errorf("store: offering retirement hold %s: %w", h.Key, err)
	}
	return s.Hold(ctx, h.Key)
}

// RetirementOf reads a task's housekeeping retirement record, and false
// when it has none, which is every task that is not a retired pass.
func (s *Store) RetirementOf(ctx context.Context, task string) (HousekeepingRetirement, bool, error) {
	if err := s.live(); err != nil {
		return HousekeepingRetirement{}, false, err
	}
	r, found, err := retirementOf(ctx, s.read, task)
	if err != nil {
		return HousekeepingRetirement{}, false, fmt.Errorf("store: %w", err)
	}
	return r, found, nil
}
