package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrTaskFallbackRecorded refuses changing a task's already selected backup.
var ErrTaskFallbackRecorded = errors.New("store: the task's backup is already recorded")

// TaskFallback is a task's written-once handoff decision. It is current
// selection, not a conclusion drawn from the last event in its history.
type TaskFallback struct {
	Primary string
	Backup  string
	Reason  string
	Attempt int64
}

// TaskFallback reads a task's handoff decision. false means none was recorded;
// a read failure is never reported as absence.
func (s *Store) TaskFallback(ctx context.Context, id string) (TaskFallback, bool, error) {
	var f TaskFallback
	err := s.read.QueryRowContext(ctx, `SELECT primary_agent, backup_agent, reason, attempt FROM task_agent_fallback WHERE task_id=?`, id).Scan(&f.Primary, &f.Backup, &f.Reason, &f.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return f, false, nil
	}
	if err != nil {
		return f, false, fmt.Errorf("store: reading task fallback: %w", err)
	}
	return f, true, nil
}

// RecordTaskFallback seals a handoff against the latest attempt the caller
// observed. A stale observer cannot change the next attempt's selection.
func (s *Store) RecordTaskFallback(ctx context.Context, id string, f TaskFallback) error {
	if f.Attempt <= 0 || strings.TrimSpace(f.Primary) == "" || strings.TrimSpace(f.Backup) == "" || f.Primary == f.Backup || (f.Reason != "quota" && f.Reason != "unavailable") {
		return errors.New("store: invalid task fallback")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkLatestTaskAttempt(ctx, tx, id, f.Attempt); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_agent_fallback WHERE task_id=?`, id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return ErrTaskFallbackRecorded
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO task_agent_fallback(task_id,primary_agent,backup_agent,reason,attempt) VALUES(?,?,?,?,?)`, id, f.Primary, f.Backup, f.Reason, f.Attempt)
		return err
	})
}

func checkLatestTaskAttempt(ctx context.Context, tx *sql.Tx, id string, attempt int64) error {
	var latest int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM task_attempt WHERE task_id=?`, id).Scan(&latest); err != nil {
		return err
	}
	if attempt <= 0 || latest != attempt {
		return &TaskMovedError{Task: id, Anchor: AnchoredAttempt, Expected: attempt, Stands: latest}
	}
	return nil
}
