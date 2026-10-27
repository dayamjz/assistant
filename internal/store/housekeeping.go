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

// Retirement is what RetireHousekeepingPass is asked to record. Hold is the
// answered retirement hold it is retired on.
type Retirement struct {
	// Task is the pass.
	Task string
	// Hold is the open retirement hold the answer of retire closes, with
	// Note and By as the answer's note and resolver.
	Hold string
	Note string
	By   Resolver
	// Delivered is internal/taskstate's terminal tier, on Disposition's
	// terms.
	Delivered func(TaskSnapshot) bool
}

// AnswerRetire is the answer an answered retirement records on the
// retirement hold it closes.
const AnswerRetire = "retire"

func (r Retirement) validate() error {
	switch {
	case strings.TrimSpace(r.Task) == "":
		return errors.New("store: a retirement names no pass")
	case r.Delivered == nil:
		return fmt.Errorf("store: retiring pass %s: what delivers a task is decided outside this package, and it was not passed", r.Task)
	case strings.TrimSpace(r.Hold) == "":
		return fmt.Errorf("store: retiring pass %s: a retirement names the retirement hold it answers", r.Task)
	}
	if _, err := resolverText(r.By); err != nil {
		return fmt.Errorf("store: retiring pass %s: %w", r.Task, err)
	}
	return nil
}

// Retired is RetireHousekeepingPass's answer: the record as written and
// the hold it closed, the retirement hold.
type Retired struct {
	Record HousekeepingRetirement
	Closed []Hold
}

// RetireHousekeepingPass writes a pass's retirement record, once, in one
// transaction that reads the pass again first, under the store's
// serialization. It refuses, writing nothing:
//
//   - with ErrPassClosed, wrapped, a pass already retired or in the terminal
//     tier, and an error for a task that is not a pass;
//   - with ErrHoldResolved, wrapped, a retirement whose hold is no longer
//     open, and ErrNotFound for one that does not exist.
//
// It resolves the retirement hold retire and writes the record naming the
// hold. PRD section 6's "written only while nothing moves the pass", the
// anchors that hold the write to the attempt, the session and the run the
// caller read, are the link above this one.
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

// closeRetirementHoldsTx resolves an answered retirement's hold retire,
// under the answer's resolver, and returns its key.
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
	return []string{r.Hold}, nil
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
