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
	ID string `json:"id"`
	// Shape is which of the two shapes this task is.
	Shape TaskShape `json:"shape"`
	// Project is what the task is for.
	Project string `json:"project"`
	// Mode is how the worker was launched, in the caller's own notation.
	Mode string `json:"mode"`
	// WorktreePath is the isolated copy the worker was given. PRD principle
	// P11 says isolation is asserted at launch rather than inferred from this
	// field; recording it is what lets a cleanup proof name what it refused to
	// remove.
	WorktreePath string `json:"worktree_path"`
	// SessionRef points at the terminal session, if one exists yet.
	SessionRef Optional[string] `json:"session_ref"`
	// RunID names the validation run the task produced, if it has produced
	// one.
	RunID Optional[string] `json:"run_id"`
	// PullRequest names the pull request the task produced, if any.
	PullRequest Optional[string] `json:"pull_request"`
	// RepositoryID and Branch say which branch of which repository the task
	// works on, and are what attributes validation runs to it: a run of that
	// repository and branch, created at or after the task, is the task's.
	// They are unknown for a task recorded without them, which is then
	// attributed by RunID alone.
	RepositoryID Optional[string] `json:"repository_id"`
	Branch       Optional[string] `json:"branch"`
	// CreatedAt is when the task was recorded.
	CreatedAt time.Time `json:"created_at"`
}

// CreateTask records a new task, its opening "queued" event, and its opening
// state, in one transaction. A task always has a resolvable current state, so
// there is no window in which TaskState reports ErrNotFound for a task that
// exists, and the caller does not choose that state: decide does, from the
// same snapshot every later settle reads.
func (s *Store) CreateTask(ctx context.Context, t Task, decide TaskDecision) (Task, error) {
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
	if decide == nil {
		return Task{}, fmt.Errorf("store: task %s has no way to decide its opening state", t.ID)
	}

	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task (id, shape, project, mode, worktree_path, session_ref, run_id, pull_request, created_at, repository_id, branch)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, string(t.Shape), t.Project, t.Mode, t.WorktreePath,
			t.SessionRef, t.RunID, t.PullRequest, encodeTime(now), t.RepositoryID, t.Branch); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_event (task_id, sequence, kind, detail, at) VALUES (?, 1, ?, '', ?)`,
			t.ID, OpeningTaskEvent, encodeTime(now)); err != nil {
			return err
		}
		_, err := settleInTx(ctx, tx, t.ID, decide, now)
		return err
	})
	if err != nil {
		return Task{}, fmt.Errorf("store: creating task %s: %w", t.ID, err)
	}
	return s.Task(ctx, t.ID)
}

// OpeningTaskEvent is the kind of the first entry of every task's history,
// appended by CreateTask. The store records it and assigns it no meaning: what
// "queued" says about a task is the resolver's to decide.
const OpeningTaskEvent = "queued"

// taskColumns is the column list every task read selects, so a column added to
// Task is added to the reads in one place.
const taskColumns = `SELECT id, shape, project, mode, worktree_path, session_ref, run_id, pull_request, created_at, repository_id, branch`

func scanTask(sc scanner) (Task, error) {
	var t Task
	var shape, created string
	if err := sc.Scan(&t.ID, &shape, &t.Project, &t.Mode, &t.WorktreePath, &t.SessionRef,
		&t.RunID, &t.PullRequest, &created, &t.RepositoryID, &t.Branch); err != nil {
		return Task{}, err
	}
	t.Shape = TaskShape(shape)
	var err error
	if t.CreatedAt, err = decodeTime(created); err != nil {
		return Task{}, err
	}
	return t, nil
}

// Task returns the task with the given identifier, or ErrNotFound.
func (s *Store) Task(ctx context.Context, id string) (Task, error) {
	if err := s.live(); err != nil {
		return Task{}, err
	}
	t, err := scanTask(s.read.QueryRowContext(ctx, taskColumns+` FROM task WHERE id = ?`, id))
	if err != nil {
		return Task{}, fmt.Errorf("store: reading task %s: %w", id, errNoRows(err))
	}
	return t, nil
}

// Tasks returns every task, newest first. PRD section 9's fleet view lists
// them with their resolved current state, which is TaskState's to answer for
// each one: this returns the records and says nothing about where any of them
// stands, per P8.
func (s *Store) Tasks(ctx context.Context) ([]Task, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, taskColumns+` FROM task ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: listing tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing tasks: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing tasks: %w", err)
	}
	return out, nil
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
