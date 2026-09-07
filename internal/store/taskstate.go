package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// TaskState is a task's current state: an authoritative record with one owner,
// which PRD principle P8 requires to be separate from the event log.
//
// It carries where the state came from as well as what it is, because a
// coordinator that has to explain itself needs both, and because a state whose
// source is unrecorded is indistinguishable from one somebody guessed.
type TaskState struct {
	// TaskID names the task.
	TaskID string `json:"task_id"`
	// State is what is true now.
	State string `json:"state"`
	// Source says what resolved it.
	Source string `json:"source"`
	// Detail is anything the source wants to add. It is unknown when the
	// source had nothing to add.
	Detail Optional[string] `json:"detail"`
	// Revision increases by one on every write, so a reader can tell whether
	// the state it holds has been superseded.
	Revision int64 `json:"revision"`
	// ResolvedAt is when this state was written.
	ResolvedAt time.Time `json:"resolved_at"`
}

// TaskEvent is one entry of a task's append-only history.
//
// It carries no state field, and that is the design rather than an omission.
// P8 says reading the last line of an event log to decide what is true now is
// always wrong, so an event is not given a shape that could answer the
// question: a caller that reads the newest event still has to call TaskState to
// learn the state. There is deliberately no accessor that returns only the last
// event.
type TaskEvent struct {
	// TaskID names the task.
	TaskID string
	// Sequence is the event's position in the task's history, counted from
	// one, with no gaps.
	Sequence int64
	// Kind is what happened, in the worker's own vocabulary.
	Kind string
	// Detail is what the worker said about it.
	Detail string
	// At is when it was appended.
	At time.Time
}

// SetTaskState writes a task's current state and returns it with the revision
// it was given. Reading the previous revision and writing the next one happen
// in one transaction on the single writer connection, so two concurrent writers
// are never handed the same revision and neither write is lost.
//
// This is the only way a task's state changes. Appending an event does not
// change it, which is what keeps the event log history rather than a second,
// competing answer to the same question.
func (s *Store) SetTaskState(ctx context.Context, taskID, state, source string, detail Optional[string]) (TaskState, error) {
	if strings.TrimSpace(taskID) == "" {
		return TaskState{}, fmt.Errorf("store: no task named")
	}
	if strings.TrimSpace(state) == "" {
		return TaskState{}, fmt.Errorf("store: task %s: no state given", taskID)
	}
	if strings.TrimSpace(source) == "" {
		return TaskState{}, fmt.Errorf("store: task %s: no source given for state %q", taskID, state)
	}

	now := nowUTC()
	var revision int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var previous Optional[int64]
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM task_state WHERE task_id = ?`, taskID).
			Scan(&previous); err != nil && !isNoRows(err) {
			return err
		}
		revision = previous.Or(0) + 1
		_, err := tx.ExecContext(ctx, `
			INSERT INTO task_state (task_id, state, source, detail, revision, resolved_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(task_id) DO UPDATE SET
				state       = excluded.state,
				source      = excluded.source,
				detail      = excluded.detail,
				revision    = excluded.revision,
				resolved_at = excluded.resolved_at`,
			taskID, state, source, detail, revision, encodeTime(now))
		return err
	})
	if err != nil {
		return TaskState{}, fmt.Errorf("store: writing the state of task %s: %w", taskID, err)
	}
	return TaskState{
		TaskID: taskID, State: state, Source: source, Detail: detail,
		Revision: revision, ResolvedAt: now,
	}, nil
}

// TaskState returns a task's current state, or ErrNotFound. It is the one
// answer to what is true now; the events are history and answer nothing.
func (s *Store) TaskState(ctx context.Context, taskID string) (TaskState, error) {
	if err := s.live(); err != nil {
		return TaskState{}, err
	}
	var st TaskState
	var resolved string
	err := s.read.QueryRowContext(ctx, `
		SELECT task_id, state, source, detail, revision, resolved_at
		FROM task_state WHERE task_id = ?`, taskID).
		Scan(&st.TaskID, &st.State, &st.Source, &st.Detail, &st.Revision, &resolved)
	if err != nil {
		return TaskState{}, fmt.Errorf("store: reading the state of task %s: %w", taskID, errNoRows(err))
	}
	if st.ResolvedAt, err = decodeTime(resolved); err != nil {
		return TaskState{}, fmt.Errorf("store: reading the state of task %s: %w", taskID, err)
	}
	return st, nil
}

// AppendTaskEvent adds one entry to a task's history and returns it with the
// sequence number it was given. Sequence numbers are dense and start at one, so
// a reader that has seen up to n can tell a gap from a quiet period.
//
// Appending changes no state. PRD section 8 says every append is a wake, and
// classifying that wake is the supervisor's job; this package records the
// entry and stops there.
func (s *Store) AppendTaskEvent(ctx context.Context, taskID, kind, detail string) (TaskEvent, error) {
	if strings.TrimSpace(taskID) == "" {
		return TaskEvent{}, fmt.Errorf("store: no task named")
	}
	if strings.TrimSpace(kind) == "" {
		return TaskEvent{}, fmt.Errorf("store: task %s: event has no kind", taskID)
	}

	now := nowUTC()
	var sequence int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sequence), 0) + 1 FROM task_event WHERE task_id = ?`, taskID).
			Scan(&sequence); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO task_event (task_id, sequence, kind, detail, at) VALUES (?, ?, ?, ?, ?)`,
			taskID, sequence, kind, detail, encodeTime(now))
		return err
	})
	if err != nil {
		return TaskEvent{}, fmt.Errorf("store: appending an event to task %s: %w", taskID, err)
	}
	return TaskEvent{TaskID: taskID, Sequence: sequence, Kind: kind, Detail: detail, At: now}, nil
}

// TaskEvents returns a task's history in append order, starting after
// afterSequence and returning at most limit entries. Pass zero for
// afterSequence to start at the beginning, and a non-positive limit for all of
// it.
//
// The whole slice comes back in append order on purpose. There is no accessor
// that returns the newest event alone, because the shape of that call is the
// shape of the P8 defect: it reads as though it answers what is true now, and
// it cannot.
func (s *Store) TaskEvents(ctx context.Context, taskID string, afterSequence int64, limit int) ([]TaskEvent, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	query := `SELECT task_id, sequence, kind, detail, at
		FROM task_event WHERE task_id = ? AND sequence > ? ORDER BY sequence`
	args := []any{taskID, afterSequence}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: reading the history of task %s: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []TaskEvent
	for rows.Next() {
		var e TaskEvent
		var at string
		if err := rows.Scan(&e.TaskID, &e.Sequence, &e.Kind, &e.Detail, &at); err != nil {
			return nil, fmt.Errorf("store: reading the history of task %s: %w", taskID, err)
		}
		if e.At, err = decodeTime(at); err != nil {
			return nil, fmt.Errorf("store: reading the history of task %s: %w", taskID, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the history of task %s: %w", taskID, err)
	}
	return out, nil
}
