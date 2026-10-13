package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TaskEvent is one entry of a task's append-only history.
//
// It carries no state field, and that is the design rather than an omission.
// P8 says reading the last line of an event log to decide what is true now is
// always wrong, so an event is not given a shape that could answer the
// question: a caller that reads the newest event still has to resolve the state through
// internal/taskstate to learn it. There is deliberately no accessor that returns only the last
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

// AppendTaskEvent adds one entry to a task's history and returns it with the
// sequence number it was given. Sequence numbers are dense and start at one, so
// a reader that has seen up to n can tell a gap from a quiet period.
//
// Appending changes no state, because the store holds none: a task's state is
// resolved from the records that own it. PRD section 8 says every append is a wake, and
// classifying that wake is the supervisor's job; this package records the
// entry and stops there.
//
// It takes no anchor, so it is for a caller recording that something happened
// whatever else has happened since: a worker's own progress, or a note the
// program appends about an event it observed. A caller whose append is a
// decision made against the history it read - an intake queueing or
// dispatching, a relaunch, a supervisor acting on a wake - uses
// AppendTaskEventAfter, because for that caller an entry it did not see is a
// reason to decide again. It returns ErrNotFound, wrapped, for a task that
// does not exist.
func (s *Store) AppendTaskEvent(ctx context.Context, taskID, kind, detail string) (TaskEvent, error) {
	return s.appendTaskEvent(ctx, taskID, kind, detail, Unknown[int64]())
}

// AppendTaskEventAfter is AppendTaskEvent anchored to the history the caller
// read: expectedSequence is the highest sequence it had seen, and the append
// is accepted only while that is still the highest. It is decided in the same
// transaction as the insert, on the single writer connection, so two callers
// that read the same tip cannot both append after it: one is assigned
// expectedSequence+1 and the other is refused with a *TaskMovedError wrapping
// ErrTaskMoved naming where the history actually stands. A refused caller
// reads the history again and decides again; retrying the same append would
// record a decision made without the entry that outran it (P6).
//
// Every task has at least its opening event, so an anchor of zero names a
// position no task's history stands at and is refused the same way.
func (s *Store) AppendTaskEventAfter(ctx context.Context, taskID string, expectedSequence int64, kind, detail string) (TaskEvent, error) {
	return s.appendTaskEvent(ctx, taskID, kind, detail, Known(expectedSequence))
}

// appendTaskEvent is the one transaction behind both appends. An unknown
// anchor accepts whatever the tip is; a known one must equal it.
func (s *Store) appendTaskEvent(ctx context.Context, taskID, kind, detail string, anchor Optional[int64]) (TaskEvent, error) {
	if strings.TrimSpace(taskID) == "" {
		return TaskEvent{}, fmt.Errorf("store: no task named")
	}
	if strings.TrimSpace(kind) == "" {
		return TaskEvent{}, fmt.Errorf("store: task %s: event has no kind", taskID)
	}

	now := nowUTC()
	var sequence int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := taskExists(ctx, tx, taskID); err != nil {
			return err
		}
		var stands int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sequence), 0) FROM task_event WHERE task_id = ?`, taskID).
			Scan(&stands); err != nil {
			return err
		}
		if expected, anchored := anchor.Get(); anchored && expected != stands {
			return &TaskMovedError{Task: taskID, Anchor: AnchoredEvent, Expected: expected, Stands: stands}
		}
		sequence = stands + 1
		_, err := tx.ExecContext(ctx,
			`INSERT INTO task_event (task_id, sequence, kind, detail, at) VALUES (?, ?, ?, ?, ?)`,
			taskID, sequence, kind, detail, encodeTime(now))
		return err
	})
	if err != nil {
		var moved *TaskMovedError
		if errors.As(err, &moved) {
			return TaskEvent{}, err
		}
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
