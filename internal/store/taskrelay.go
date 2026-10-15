package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FindingsRelayedEventKind is the kind of the entry RecordTaskRelay appends
// in the transaction that writes a task's findings relayed record, so the
// history entry that says the findings were relayed and the record cannot
// disagree (P8). Its detail is empty: the record is the task_relay row. As
// with ReportReadyEventKind, the store records the word and
// internal/taskstate's table is what gives it a meaning.
const FindingsRelayedEventKind = "findings-relayed"

// TaskRelay is the record of PRD section 6's "findings relayed": the
// coordinator's statement, made through the service, that it relayed a
// reported investigation's findings. It is written once, by RecordTaskRelay,
// and never revised or deleted; the table is sealed against both.
//
// It records what the coordinator said it did and proves nothing about it:
// no surface here witnesses a relay, so the row is the coordinator's word,
// accepted only for a task the records show is a reported investigation.
// internal/taskstate's terminal tier resolves an investigation Complete from
// this row, and nothing stores that fact again.
type TaskRelay struct {
	// TaskID names the task.
	TaskID string `json:"task_id"`
	// Event is the sequence of the FindingsRelayedEventKind entry appended
	// with the row.
	Event int64 `json:"event"`
	// RecordedAt is when the row was written.
	RecordedAt time.Time `json:"recorded_at"`
}

// ErrNotRelayable is returned by RecordTaskRelay for a task whose records do
// not make it a reported investigation: a task that is not an investigation
// task, one with no stored report, or one with an open hold naming it.
var ErrNotRelayable = errors.New("store: only a reported investigation's findings are recorded relayed")

// ErrTaskRelayed is returned by RecordTaskRelay when the task already has a
// findings relayed record. The first one stands.
var ErrTaskRelayed = errors.New("store: the task's findings are already recorded relayed")

// RecordTaskRelay writes the task's findings relayed record and appends a
// FindingsRelayedEventKind entry to its history, in one transaction, and
// returns both as stored.
//
// The entry is anchored: expectedSequence is the history position the
// caller resolved the task at, and a history that moved since is a
// *TaskMovedError with nothing written (P6). Inside the same transaction it
// refuses, with ErrNotRelayable wrapped, a task that is not an investigation
// task, one with no stored report, and one an open hold names; with
// ErrTaskRelayed a task that already has the record; and with ErrNotFound,
// wrapped, a task that does not exist. Those are the record facts behind
// internal/taskstate's Reported for an investigation, read in the
// transaction that writes the row. The run tier, which sits above the report
// tier, is not among them, because this package does not resolve state: an
// investigation pushes nothing, so only a run its own row names could reach
// that tier, and a relay of such a task is not refused here.
func (s *Store) RecordTaskRelay(ctx context.Context, taskID string, expectedSequence int64) (TaskRelay, TaskEvent, error) {
	if strings.TrimSpace(taskID) == "" {
		return TaskRelay{}, TaskEvent{}, fmt.Errorf("store: a findings relayed record names no task")
	}
	var event TaskEvent
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		task, err := scanTask(tx.QueryRowContext(ctx, taskColumns+` FROM task WHERE id = ?`, taskID))
		if err != nil {
			return errNoRows(err)
		}
		if task.Shape != TaskInvestigation {
			return fmt.Errorf("%w: task %s is a %s task", ErrNotRelayable, taskID, task.Shape)
		}
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM task_report WHERE task_id = ?`, taskID).Scan(&one); {
		case isNoRows(err):
			return fmt.Errorf("%w: task %s has no stored report", ErrNotRelayable, taskID)
		case err != nil:
			return err
		}
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM task_relay WHERE task_id = ?`, taskID).Scan(&one); {
		case err == nil:
			return ErrTaskRelayed
		case !isNoRows(err):
			return err
		}
		var open string
		switch err := tx.QueryRowContext(ctx,
			`SELECT key FROM hold WHERE task_id = ? AND resolution IS NULL ORDER BY opened_at, key LIMIT 1`, taskID).Scan(&open); {
		case err == nil:
			return fmt.Errorf("%w: task %s is waiting on hold %s", ErrNotRelayable, taskID, open)
		case !isNoRows(err):
			return err
		}
		event, err = appendTaskEventTx(ctx, tx, taskID, FindingsRelayedEventKind, "", Known(expectedSequence))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_relay (task_id, event, recorded_at) VALUES (?, ?, ?)`,
			taskID, event.Sequence, encodeTime(event.At))
		return err
	})
	if err != nil {
		var moved *TaskMovedError
		if errors.As(err, &moved) || errors.Is(err, ErrTaskRelayed) || errors.Is(err, ErrNotRelayable) {
			return TaskRelay{}, TaskEvent{}, err
		}
		return TaskRelay{}, TaskEvent{}, fmt.Errorf("store: recording the findings of task %s relayed: %w", taskID, err)
	}
	relay, err := taskRelayOf(ctx, s.read, taskID)
	if err != nil {
		return TaskRelay{}, TaskEvent{}, fmt.Errorf("store: reading the findings relayed record of task %s: %w", taskID, errNoRows(err))
	}
	return relay, event, nil
}

// taskRelayOf reads one task's findings relayed record through q, so the
// snapshot reads it inside its own transaction with the same scanner.
func taskRelayOf(ctx context.Context, q querier, taskID string) (TaskRelay, error) {
	var r TaskRelay
	var recorded string
	err := q.QueryRowContext(ctx, `SELECT task_id, event, recorded_at FROM task_relay WHERE task_id = ?`, taskID).
		Scan(&r.TaskID, &r.Event, &recorded)
	if err != nil {
		return TaskRelay{}, err
	}
	if r.RecordedAt, err = decodeTime(recorded); err != nil {
		return TaskRelay{}, err
	}
	return r, nil
}
