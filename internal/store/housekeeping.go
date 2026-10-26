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
// naming how it was retired and when. It is the one record that ends a
// pass's claim on its repository other than the terminal tier. The state
// resolver does not read it, so a retired pass still resolves the state its
// own records give; the one-open-pass rule, the due check and every attempt
// of the pass read it.
type HousekeepingRetirement struct {
	// Task is the retired pass.
	Task string `json:"task"`
	// Run is the run whose recorded position found nothing left to change,
	// for a pass the program retired on that proof, and unknown otherwise.
	Run Optional[string] `json:"run"`
	// Hold is the retirement hold an answer of retire closed, for a pass
	// retired on a person's decision, and unknown otherwise. Exactly one of
	// Run and Hold is known.
	Hold Optional[string] `json:"hold"`
	// RecordedAt is when the record was written.
	RecordedAt time.Time `json:"recorded_at"`
}

// OnProof reports whether the program retired the pass on its run's proof
// rather than on an answer.
func (r HousekeepingRetirement) OnProof() bool { return r.Run.IsKnown() }

const retirementColumns = `SELECT task_id, run_id, hold_key, recorded_at FROM housekeeping_retirement`

func scanRetirement(sc scanner) (HousekeepingRetirement, error) {
	var r HousekeepingRetirement
	var recorded string
	if err := sc.Scan(&r.Task, &r.Run, &r.Hold, &recorded); err != nil {
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

// Retirement is what RetireHousekeepingPass is asked to record, and what the
// caller read when it decided nothing moves the pass: the pass's latest
// attempt and newest attributed run as they stood. Exactly one of OnProof
// and Hold says how it is retired.
type Retirement struct {
	// Task is the pass.
	Task string
	// Attempt is the pass's latest attempt sequence as the caller read it,
	// and zero for a pass never dispatched. A pass whose latest attempt has
	// moved since was relaunched, which moves it.
	Attempt int64
	// Run is the newest run attributed to the pass as the caller read it,
	// and empty for none. A newer one has started since, which moves it.
	Run string
	// OnProof is a retirement the program makes on Run's recorded position:
	// Run must be known and have passed, and no open hold of the pass may
	// offer to relaunch its worker.
	OnProof bool
	// Hold is the open retirement hold an answer of retire closes, with
	// Note and By as its answer's note and resolver.
	Hold string
	Note string
	By   Resolver
	// Relaunches reports whether an open hold key offers to relaunch the
	// pass's worker. An answered retirement closes each with AnswerStop
	// under By. An on-proof retirement is refused while one it names is
	// open, so for one the caller names every hold that waits on a person
	// about the pass's end, its retirement hold among them: the program
	// never answers such a hold. Its owner is internal/service, which keys
	// those holds.
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
	case r.OnProof == (r.Hold != ""):
		return fmt.Errorf("store: retiring pass %s: a retirement is on a run's proof or on an answered hold, exactly one", r.Task)
	case r.OnProof && r.Run == "":
		return fmt.Errorf("store: retiring pass %s on proof names no run", r.Task)
	}
	if r.Hold != "" {
		if _, err := resolverText(r.By); err != nil {
			return fmt.Errorf("store: retiring pass %s: %w", r.Task, err)
		}
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
//   - with ErrPassMoves, wrapped, a pass whose latest attempt or newest
//     attributed run is not the one the caller read, or with a run
//     attributed to it that has not ended, or, on proof, whose newest run
//     did not pass or which has an open hold that offers to relaunch its
//     worker;
//   - with ErrHoldResolved, wrapped, an answered retirement whose hold is no
//     longer open, and ErrNotFound for one that does not exist.
//
// An answered retirement resolves its hold retire, then every other open
// hold of the pass Relaunches names stop, each under the same resolver, and
// writes the record naming the hold. An on-proof retirement writes the
// record naming the run and closes nothing. Whether the worker is dead is
// the probe's, which no transaction can take; the attempt anchor is what
// keeps a relaunch since the caller's probe from slipping past.
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
		var run, hold any
		if r.OnProof {
			run = r.Run
		} else {
			hold = r.Hold
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
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO housekeeping_retirement (task_id, run_id, hold_key, recorded_at) VALUES (?, ?, ?, ?)`,
			r.Task, run, hold, now); err != nil {
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

// CheckRetirement answers, writing nothing, what RetireHousekeepingPass
// would refuse r for as the records stand now, read in one snapshot: nil, or
// the same refusals, wrapped: r malformed, a task that is not a pass,
// ErrPassClosed, ErrPassMoves, and, for an answered retirement, ErrNotFound
// for a hold that does not exist and ErrHoldResolved for one already
// answered. It
// is the check an on-proof retirement makes before it ends the pass's worker,
// so a proof that is no longer current ends nothing (P6); it decides
// nothing by itself, because a write may land between it and the
// retirement's own transaction, which reads the pass again.
func (s *Store) CheckRetirement(ctx context.Context, r Retirement) error {
	if err := r.validate(); err != nil {
		return err
	}
	err := s.snapshotTx(ctx, func(q querier) error {
		task, err := scanTask(q.QueryRowContext(ctx, taskColumns+` FROM task WHERE id = ?`, r.Task))
		if err != nil {
			return errNoRows(err)
		}
		if !task.Housekeeping {
			return fmt.Errorf("task %s is not a housekeeping pass, so it has no retirement", r.Task)
		}
		snap, err := snapshotOf(ctx, q, task)
		if err != nil {
			return err
		}
		if !PassOpen(snap, r.Delivered) {
			return ErrPassClosed
		}
		if err := passStillTx(snap, r); err != nil {
			return err
		}
		if r.OnProof {
			return nil
		}
		var resolution Optional[string]
		switch err := q.QueryRowContext(ctx, `SELECT resolution FROM hold WHERE key = ?`, r.Hold).Scan(&resolution); {
		case isNoRows(err):
			return fmt.Errorf("hold %s: %w", r.Hold, ErrNotFound)
		case err != nil:
			return err
		case resolution.IsKnown():
			return fmt.Errorf("hold %s: %w with %s", r.Hold, ErrHoldResolved, resolution.Or(""))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: checking the retirement of housekeeping pass %s: %w", r.Task, err)
	}
	return nil
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
	if !r.OnProof {
		return nil
	}
	if snap.Runs[0].Status != RunPassed {
		return fmt.Errorf("%w: run %s ended %s, which is no proof that nothing was left to change", ErrPassMoves, r.Run, snap.Runs[0].Status)
	}
	for _, h := range snap.Holds {
		if h.Open() && h.TaskID.Or("") == snap.Task.ID && r.Relaunches(h.Key) {
			return fmt.Errorf("%w: hold %s offers to relaunch its worker and waits on a person, whom the program never answers for",
				ErrPassMoves, h.Key)
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

// RegisterHoldUnder registers a hold naming a task, on RegisterHold's terms,
// fenced by the supervisor loop's incarnation and anchored to the task's
// latest attempt, attempt, which is zero for a task never dispatched: a loop
// the watchdog replaced registers nothing, and a hold decided against an
// attempt the task has moved past is refused with a *TaskMovedError, nothing
// written, and so is a retired housekeeping pass, with ErrPassRetired. It is
// how the loop offers a housekeeping pass's retirement hold.
func (s *Store) RegisterHoldUnder(ctx context.Context, inc Incarnation, attempt int64, h Hold) (Hold, error) {
	if err := validateHold(h); err != nil {
		return Hold{}, err
	}
	task, ok := h.TaskID.Get()
	if !ok || h.RunID.IsKnown() {
		return Hold{}, fmt.Errorf("store: hold %s: a hold the loop registers names a task and no run", h.Key)
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
		if stands != attempt {
			return &TaskMovedError{Task: task, Anchor: AnchoredAttempt, Expected: attempt, Stands: stands}
		}
		// A retired pass's claim is released, so a hold offering to retire
		// it could only be refused; the read is in the registering
		// transaction, so a retirement and a registration are wholly
		// ordered.
		if err := refuseRetiredTx(ctx, tx, task); err != nil {
			return err
		}
		return insertHold(ctx, tx, h)
	})
	if err != nil {
		var moved *TaskMovedError
		if errors.As(err, &moved) {
			return Hold{}, err
		}
		return Hold{}, fmt.Errorf("store: registering hold %s: %w", h.Key, err)
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
