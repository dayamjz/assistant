package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// TaskShape is which of the two task shapes a task is. PRD section 12 gives a
// delivery task a run and a pull request, and an investigation task neither.
type TaskShape string

// The task shapes.
const (
	TaskDelivery      TaskShape = "delivery"
	TaskInvestigation TaskShape = "investigation"
)

// Task is the authoritative record of one worker's assignment.
type Task struct {
	// ID is the caller's identifier for the task.
	ID string
	// Shape is which of the two shapes this task is.
	Shape TaskShape
	// Project is what the task is for.
	Project string
	// Mode is how the worker was launched, in the caller's own notation.
	Mode string
	// WorktreePath is the isolated copy the worker was given. PRD principle
	// P11 says isolation is asserted at launch rather than inferred from this
	// field; recording it is what lets a cleanup proof name what it refused to
	// remove.
	WorktreePath string
	// SessionRef points at the terminal session, if one exists yet.
	SessionRef Optional[string]
	// RunID names the validation run the task produced, if it has produced
	// one.
	RunID Optional[string]
	// PullRequest names the pull request the task produced, if any.
	PullRequest Optional[string]
	// CreatedAt is when the task was recorded.
	CreatedAt time.Time
}

// CreateTask records a new task and its opening state, in one transaction. A
// task always has a resolvable current state, so there is no window in which
// TaskState reports ErrNotFound for a task that exists.
func (s *Store) CreateTask(ctx context.Context, t Task, state, source string) (Task, error) {
	if strings.TrimSpace(t.ID) == "" {
		return Task{}, fmt.Errorf("store: task has no identifier")
	}
	switch t.Shape {
	case TaskDelivery, TaskInvestigation:
	default:
		return Task{}, fmt.Errorf("store: task %s has shape %q, which is neither %q nor %q",
			t.ID, t.Shape, TaskDelivery, TaskInvestigation)
	}
	if strings.TrimSpace(t.WorktreePath) == "" {
		return Task{}, fmt.Errorf("store: task %s has no isolated copy path", t.ID)
	}
	if strings.TrimSpace(state) == "" {
		return Task{}, fmt.Errorf("store: task %s has no opening state", t.ID)
	}
	if strings.TrimSpace(source) == "" {
		return Task{}, fmt.Errorf("store: task %s does not say where its opening state came from", t.ID)
	}

	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task (id, shape, project, mode, worktree_path, session_ref, run_id, pull_request, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, string(t.Shape), t.Project, t.Mode, t.WorktreePath,
			t.SessionRef, t.RunID, t.PullRequest, encodeTime(now)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO task_state (task_id, state, source, detail, revision, resolved_at)
			VALUES (?, ?, ?, ?, 1, ?)`,
			t.ID, state, source, Unknown[string](), encodeTime(now))
		return err
	})
	if err != nil {
		return Task{}, fmt.Errorf("store: creating task %s: %w", t.ID, err)
	}
	return s.Task(ctx, t.ID)
}

// Task returns the task with the given identifier, or ErrNotFound.
func (s *Store) Task(ctx context.Context, id string) (Task, error) {
	if err := s.live(); err != nil {
		return Task{}, err
	}
	var t Task
	var shape, created string
	err := s.read.QueryRowContext(ctx, `
		SELECT id, shape, project, mode, worktree_path, session_ref, run_id, pull_request, created_at
		FROM task WHERE id = ?`, id).
		Scan(&t.ID, &shape, &t.Project, &t.Mode, &t.WorktreePath, &t.SessionRef, &t.RunID, &t.PullRequest, &created)
	if err != nil {
		return Task{}, fmt.Errorf("store: reading task %s: %w", id, errNoRows(err))
	}
	t.Shape = TaskShape(shape)
	if t.CreatedAt, err = decodeTime(created); err != nil {
		return Task{}, fmt.Errorf("store: reading task %s: %w", id, err)
	}
	return t, nil
}

// SetTaskSession records the terminal session the task is attached to.
func (s *Store) SetTaskSession(ctx context.Context, id, sessionRef string) error {
	return s.updateTask(ctx, id, "session_ref", sessionRef)
}

// SetTaskRun links the task to the validation run it produced.
func (s *Store) SetTaskRun(ctx context.Context, id, runID string) error {
	return s.updateTask(ctx, id, "run_id", runID)
}

// SetTaskPullRequest links the task to the pull request it produced.
func (s *Store) SetTaskPullRequest(ctx context.Context, id, pullRequest string) error {
	return s.updateTask(ctx, id, "pull_request", pullRequest)
}

// updateTask writes one column of one task. The column name comes from the
// fixed set above and never from a caller.
func (s *Store) updateTask(ctx context.Context, id, column, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("store: task %s: %s is empty", id, column)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE task SET `+column+` = ? WHERE id = ?`, value, id)
		if err != nil {
			return fmt.Errorf("store: updating task %s: %w", id, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: updating task %s: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("store: updating task %s: %w", id, ErrNotFound)
		}
		return nil
	})
}
