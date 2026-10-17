package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/config"
)

// LocalLanding is PRD section 8's local landing record: the program's own
// record that the approved fast-forward delivering a local task moved the
// person's local default branch. It is written once, by RecordLocalLanding in
// the transaction that records the approval, and never updated or deleted;
// the table is sealed against both.
//
// The task's Landed for a local task is derived from this row, by
// internal/taskstate's terminal tier, which reads it beside the merge record;
// nothing stores that fact again. The row records what the fast-forward did
// and decides nothing: whether it was allowed is internal/safety's question
// and was answered before the row was written.
type LocalLanding struct {
	// Task is the task the fast-forward delivered.
	Task string `json:"task"`
	// Commit is the commit the worker pushed and the branch was moved to.
	Commit string `json:"commit"`
	// Branch is the local default branch that moved, as a full reference
	// name.
	Branch string `json:"branch"`
	// Before is the branch's tip the fast-forward was anchored to, which is
	// the tip the program read when it offered the change for approval.
	Before string `json:"before"`
	// After is the branch's tip once the fast-forward was made, which is
	// Commit.
	After string `json:"after"`
	// RecordedAt is when the row was written.
	RecordedAt time.Time `json:"recorded_at"`
}

// RecordLocalLanding writes a task's local landing record and, with a
// non-zero closing, closes the hold the task waited on for its approval, in
// one transaction, on HoldClosure's terms: a reader never finds a landed task
// still holding for the approval that landed it, or an approval recorded on
// a task with no record of what it moved. A hold already carrying a
// resolution is left as it stands, because the record is the fact the
// transaction exists for and the fast-forward it describes has been made.
//
// It refuses a record missing any of task, commit, branch, before or after,
// and one whose After is not its Commit, before anything is written; a task
// that is not a delivery task recorded under config.DeliveryLocal with
// ErrNotLocal; and a second record for the task with
// ErrLocalLandingRecorded, decided in the same transaction as the insert.
func (s *Store) RecordLocalLanding(ctx context.Context, l LocalLanding, closing HoldClosure) (LocalLanding, error) {
	for name, v := range map[string]string{
		"task": l.Task, "commit": l.Commit, "branch": l.Branch, "before": l.Before, "after": l.After,
	} {
		if strings.TrimSpace(v) == "" {
			return LocalLanding{}, fmt.Errorf("store: a local landing record names no %s", name)
		}
	}
	if l.After != l.Commit {
		return LocalLanding{}, fmt.Errorf("store: task %s: a local landing moved the branch to %s and records %s as the commit",
			l.Task, l.After, l.Commit)
	}
	if closing.Hold.Key != "" {
		if strings.TrimSpace(closing.Hold.Subject) == "" {
			return LocalLanding{}, fmt.Errorf("store: hold %s has no subject", closing.Hold.Key)
		}
		if strings.TrimSpace(closing.Resolution) == "" {
			return LocalLanding{}, fmt.Errorf("%w: hold %s", ErrNoResolution, closing.Hold.Key)
		}
		if _, err := resolverText(closing.By); err != nil {
			return LocalLanding{}, fmt.Errorf("store: resolving hold %s: %w", closing.Hold.Key, err)
		}
	}
	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		task, err := scanTask(tx.QueryRowContext(ctx, taskColumns+` FROM task WHERE id = ?`, l.Task))
		if err != nil {
			return errNoRows(err)
		}
		if posture, ok := task.Delivery.Get(); !ok || posture != config.DeliveryLocal || task.Shape != TaskDelivery {
			return fmt.Errorf("%w: task %s is %s under %s", ErrNotLocal, l.Task, task.Shape, task.Delivery)
		}
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM local_landing WHERE task = ?`, l.Task).Scan(&one); {
		case err == nil:
			return fmt.Errorf("%w: task %s", ErrLocalLandingRecorded, l.Task)
		case !isNoRows(err):
			return err
		}
		if closing.Hold.Key != "" {
			if err := s.closeHold(ctx, tx, closing, encodeTime(now)); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO local_landing (task, commit_id, branch, before, after, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			l.Task, l.Commit, l.Branch, l.Before, l.After, encodeTime(now))
		return err
	})
	if err != nil {
		if errors.Is(err, ErrLocalLandingRecorded) || errors.Is(err, ErrNotLocal) {
			return LocalLanding{}, err
		}
		return LocalLanding{}, fmt.Errorf("store: recording the local landing of task %s: %w", l.Task, err)
	}
	got, _, err := s.LocalLanding(ctx, l.Task)
	return got, err
}

// LocalLanding returns the task's local landing record and whether there is
// one. A task no approved fast-forward delivered has none, and that is an
// answer rather than a failure.
func (s *Store) LocalLanding(ctx context.Context, task string) (LocalLanding, bool, error) {
	if err := s.live(); err != nil {
		return LocalLanding{}, false, err
	}
	l, err := localLandingOf(ctx, s.read, task)
	if isNoRows(err) {
		return LocalLanding{}, false, nil
	}
	if err != nil {
		return LocalLanding{}, false, fmt.Errorf("store: reading the local landing record of task %s: %w", task, err)
	}
	return l, true, nil
}

// localLandingOf reads one task's record through q, so the snapshot reads it
// inside its own transaction with the same scanner.
func localLandingOf(ctx context.Context, q querier, task string) (LocalLanding, error) {
	var l LocalLanding
	var recorded string
	err := q.QueryRowContext(ctx,
		`SELECT task, commit_id, branch, before, after, recorded_at FROM local_landing WHERE task = ?`, task).
		Scan(&l.Task, &l.Commit, &l.Branch, &l.Before, &l.After, &recorded)
	if err != nil {
		return LocalLanding{}, err
	}
	if l.RecordedAt, err = decodeTime(recorded); err != nil {
		return LocalLanding{}, err
	}
	return l, nil
}
