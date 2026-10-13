package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// TaskSnapshot is everything a task's current state may be decided from, read
// in one transaction so the inputs agree with each other.
type TaskSnapshot struct {
	// Task is the task record.
	Task Task
	// Runs are the validation runs attributed to the task, newest first: the
	// run it is linked to, and every run of its repository and branch created
	// at or after the task. A task with neither a link nor a branch has none.
	Runs []Run
	// Holds are the holds still open that belong to the task or to one of
	// Runs. A resolved hold is not here, because it is not waiting on anyone.
	Holds []Hold
	// Events is the task's whole history in append order. It is the whole
	// history and never its tail, per P8.
	Events []TaskEvent
	// Stored is the state record as it stood when the snapshot was read, and
	// unknown when the task has none yet.
	Stored Optional[TaskState]
}

// TaskStateWrite is the answer a TaskDecision returns: the record's content
// without the revision and time, which this package assigns. Superseded and
// Basis are stored as the opaque text given.
type TaskStateWrite struct {
	State      string
	Source     string
	Detail     Optional[string]
	Superseded Optional[string]
	Basis      Optional[string]
}

// TaskDecision decides a task's state from a snapshot of its inputs. It runs
// inside the transaction that then writes its answer, so the answer is about
// the inputs it was given and no others. It must not call back into the Store,
// whose single writer connection is held while it runs.
type TaskDecision func(TaskSnapshot) (TaskStateWrite, error)

// TaskSnapshot reads the inputs of a task's state without writing anything,
// for a caller that wants to check a stored answer against them. It returns
// ErrNotFound for an unknown task.
func (s *Store) TaskSnapshot(ctx context.Context, taskID string) (TaskSnapshot, error) {
	if err := s.live(); err != nil {
		return TaskSnapshot{}, err
	}
	snap, err := readSnapshot(ctx, s.read, taskID)
	if err != nil {
		return TaskSnapshot{}, fmt.Errorf("store: reading the inputs of task %s: %w", taskID, err)
	}
	return snap, nil
}

// SettleTaskState decides a task's state from the snapshot of its inputs and
// writes the answer, in one transaction on the single writer connection, so
// nothing can change an input between the read and the write. With CreateTask
// and AppendTaskEventAndSettle, which take the same decision, it is the only
// way a task's state record changes.
//
// When the answer's state, source, detail and superseded set equal the stored
// ones, the revision and time do not move, and only a changed basis is
// refreshed. So Revision counts changes of the answer rather than settles. A
// decision that errors, or that returns an empty state or source, writes
// nothing and the error is returned.
//
// Which decision a caller passes is that caller's; this package cannot check
// that it is the resolver's. Taking the state from a function is what this
// buys, and a package that passes its own function is outside it.
func (s *Store) SettleTaskState(ctx context.Context, taskID string, decide TaskDecision) (TaskState, error) {
	if decide == nil {
		return TaskState{}, fmt.Errorf("store: settling task %s: no decision given", taskID)
	}
	var out TaskState
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = settleInTx(ctx, tx, taskID, decide, nowUTC())
		return err
	})
	if err != nil {
		return TaskState{}, fmt.Errorf("store: settling the state of task %s: %w", taskID, err)
	}
	return out, nil
}

// AppendTaskEventAndSettle appends an event and settles the task's state in
// one transaction, so there is no window in which the history holds the event
// and the state record has not seen it.
func (s *Store) AppendTaskEventAndSettle(ctx context.Context, taskID, kind, detail string, decide TaskDecision) (TaskEvent, TaskState, error) {
	if decide == nil {
		return TaskEvent{}, TaskState{}, fmt.Errorf("store: appending to task %s: no decision given", taskID)
	}
	var event TaskEvent
	var state TaskState
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		now := nowUTC()
		var err error
		if event, err = appendEventInTx(ctx, tx, taskID, kind, detail, now); err != nil {
			return err
		}
		state, err = settleInTx(ctx, tx, taskID, decide, now)
		return err
	})
	if err != nil {
		return TaskEvent{}, TaskState{}, fmt.Errorf("store: appending an event to task %s and settling it: %w", taskID, err)
	}
	return event, state, nil
}

// TasksForRun returns the identifiers of the tasks a run is attributed to by
// the same rule a snapshot uses: the task is linked to the run, or the run is
// of the task's repository and branch and was created at or after the task.
func (s *Store) TasksForRun(ctx context.Context, runID string) ([]string, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT t.id FROM task t, run r
		WHERE r.id = ? AND (t.run_id = r.id
			OR (t.repository_id = r.repository_id AND t.branch = r.branch AND r.created_at >= t.created_at))
		ORDER BY t.id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: finding the tasks of run %s: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store: finding the tasks of run %s: %w", runID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: finding the tasks of run %s: %w", runID, err)
	}
	return ids, nil
}

// querier is what a snapshot is read through: the writer's transaction when it
// is read to be written, and the reader pool when it is read to be checked.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func settleInTx(ctx context.Context, tx *sql.Tx, taskID string, decide TaskDecision, now time.Time) (TaskState, error) {
	snap, err := readSnapshot(ctx, tx, taskID)
	if err != nil {
		return TaskState{}, err
	}
	w, err := decide(snap)
	if err != nil {
		return TaskState{}, fmt.Errorf("deciding: %w", err)
	}
	if strings.TrimSpace(w.State) == "" {
		return TaskState{}, fmt.Errorf("the decision gave no state")
	}
	if strings.TrimSpace(w.Source) == "" {
		return TaskState{}, fmt.Errorf("the decision gave no source for state %q", w.State)
	}

	next := TaskState{
		TaskID: taskID, State: w.State, Source: w.Source, Detail: w.Detail,
		Superseded: w.Superseded, Basis: w.Basis, Revision: 1, ResolvedAt: now,
	}
	if prev, ok := snap.Stored.Get(); ok {
		if prev.State == w.State && prev.Source == w.Source && prev.Detail == w.Detail &&
			prev.Superseded == w.Superseded {
			if prev.Basis != w.Basis {
				if _, err := tx.ExecContext(ctx, `UPDATE task_state SET basis = ? WHERE task_id = ?`,
					w.Basis, taskID); err != nil {
					return TaskState{}, err
				}
				prev.Basis = w.Basis
			}
			return prev, nil
		}
		next.Revision = prev.Revision + 1
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO task_state (task_id, state, source, detail, superseded, basis, revision, resolved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			state       = excluded.state,
			source      = excluded.source,
			detail      = excluded.detail,
			superseded  = excluded.superseded,
			basis       = excluded.basis,
			revision    = excluded.revision,
			resolved_at = excluded.resolved_at`,
		taskID, next.State, next.Source, next.Detail, next.Superseded, next.Basis,
		next.Revision, encodeTime(now))
	if err != nil {
		return TaskState{}, err
	}
	return next, nil
}

func readSnapshot(ctx context.Context, q querier, taskID string) (TaskSnapshot, error) {
	var snap TaskSnapshot
	var err error
	if snap.Task, err = scanTask(q.QueryRowContext(ctx, taskColumns+` FROM task WHERE id = ?`, taskID)); err != nil {
		return TaskSnapshot{}, errNoRows(err)
	}

	runRows, err := q.QueryContext(ctx, runColumns+` FROM run
		WHERE id = ? OR (repository_id = ? AND branch = ? AND created_at >= ?)
		ORDER BY created_at DESC, id DESC`,
		snap.Task.RunID, snap.Task.RepositoryID, snap.Task.Branch, encodeTime(snap.Task.CreatedAt))
	if err != nil {
		return TaskSnapshot{}, err
	}
	for runRows.Next() {
		r, err := scanRun(runRows)
		if err != nil {
			_ = runRows.Close()
			return TaskSnapshot{}, err
		}
		snap.Runs = append(snap.Runs, r)
	}
	if err := closeRows(runRows); err != nil {
		return TaskSnapshot{}, err
	}

	holdRows, err := q.QueryContext(ctx, holdColumns+` FROM hold WHERE resolution IS NULL ORDER BY opened_at, key`)
	if err != nil {
		return TaskSnapshot{}, err
	}
	for holdRows.Next() {
		h, err := scanHold(holdRows)
		if err != nil {
			_ = holdRows.Close()
			return TaskSnapshot{}, err
		}
		if holdBelongs(h, snap) {
			snap.Holds = append(snap.Holds, h)
		}
	}
	if err := closeRows(holdRows); err != nil {
		return TaskSnapshot{}, err
	}

	eventRows, err := q.QueryContext(ctx, `SELECT task_id, sequence, kind, detail, at
		FROM task_event WHERE task_id = ? ORDER BY sequence`, taskID)
	if err != nil {
		return TaskSnapshot{}, err
	}
	for eventRows.Next() {
		var e TaskEvent
		var at string
		if err := eventRows.Scan(&e.TaskID, &e.Sequence, &e.Kind, &e.Detail, &at); err != nil {
			_ = eventRows.Close()
			return TaskSnapshot{}, err
		}
		if e.At, err = decodeTime(at); err != nil {
			_ = eventRows.Close()
			return TaskSnapshot{}, err
		}
		snap.Events = append(snap.Events, e)
	}
	if err := closeRows(eventRows); err != nil {
		return TaskSnapshot{}, err
	}

	var st TaskState
	var resolved string
	err = q.QueryRowContext(ctx, `
		SELECT task_id, state, source, detail, superseded, basis, revision, resolved_at
		FROM task_state WHERE task_id = ?`, taskID).
		Scan(&st.TaskID, &st.State, &st.Source, &st.Detail, &st.Superseded, &st.Basis, &st.Revision, &resolved)
	switch {
	case isNoRows(err):
	case err != nil:
		return TaskSnapshot{}, err
	default:
		if st.ResolvedAt, err = decodeTime(resolved); err != nil {
			return TaskSnapshot{}, err
		}
		snap.Stored = Known(st)
	}
	return snap, nil
}

// holdBelongs reports whether a hold is the task's own or belongs to one of
// the runs attributed to it.
func holdBelongs(h Hold, snap TaskSnapshot) bool {
	if id, ok := h.TaskID.Get(); ok && id == snap.Task.ID {
		return true
	}
	runID, ok := h.RunID.Get()
	if !ok {
		return false
	}
	for _, r := range snap.Runs {
		if r.ID == runID {
			return true
		}
	}
	return false
}

func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return err
}
