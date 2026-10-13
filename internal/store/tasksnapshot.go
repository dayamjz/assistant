package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// TaskSnapshot is every record a task's state is resolved from, read in one
// transaction.
//
// It is the store's whole contribution to the question of what a task's state
// is: it returns the facts and decides nothing about what they mean. One
// transaction matters because a run moving between two separate queries would
// hand the resolver a run and a log that were never both true.
type TaskSnapshot struct {
	// Task is the task record.
	Task Task
	// Runs are the runs attributed to the task, newest first: those of the
	// task's own repository and branch created at or after the task was, plus
	// the run the task names directly, if any. A task with neither a branch
	// nor a named run has none.
	Runs []Run
	// Holds are the holds, open or resolved, that name the task or one of
	// Runs, oldest first.
	Holds []Hold
	// Events are the task's whole history in append order.
	Events []TaskEvent
}

// TaskSnapshot reads one task and the records its state is resolved from, as
// of a single point in time. It returns ErrNotFound for an unknown task.
//
// Attribution by branch and creation time is a heuristic and says so: a task
// relaunched in place after an earlier run of the same branch would be
// outranked by that older run's status until a newer run exists. The creation
// time is the cutoff because nothing in this build records an attempt marker.
func (s *Store) TaskSnapshot(ctx context.Context, id string) (TaskSnapshot, error) {
	var out TaskSnapshot
	err := s.snapshotTx(ctx, func(q querier) error {
		row := q.QueryRowContext(ctx, taskColumns+` FROM task WHERE id = ?`, id)
		task, err := scanTask(row)
		if err != nil {
			return errNoRows(err)
		}
		out, err = snapshotOf(ctx, q, task)
		return err
	})
	if err != nil {
		return TaskSnapshot{}, fmt.Errorf("store: reading the records of task %s: %w", id, err)
	}
	return out, nil
}

// TaskSnapshots reads every task as TaskSnapshot does, newest task first, all
// as of one point in time.
func (s *Store) TaskSnapshots(ctx context.Context) ([]TaskSnapshot, error) {
	var out []TaskSnapshot
	err := s.snapshotTx(ctx, func(q querier) error {
		rows, err := q.QueryContext(ctx, taskColumns+` FROM task ORDER BY created_at DESC, id DESC`)
		if err != nil {
			return err
		}
		var tasks []Task
		for rows.Next() {
			task, err := scanTask(rows)
			if err != nil {
				_ = rows.Close()
				return err
			}
			tasks = append(tasks, task)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		_ = rows.Close()
		for _, task := range tasks {
			snap, err := snapshotOf(ctx, q, task)
			if err != nil {
				return err
			}
			out = append(out, snap)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: reading the records of every task: %w", err)
	}
	return out, nil
}

// querier is what a read transaction offers the snapshot queries.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// snapshotTx runs fn inside one read transaction on the reader pool, which is
// what makes the queries it issues see one state of the database.
func (s *Store) snapshotTx(ctx context.Context, fn func(q querier) error) error {
	if err := s.live(); err != nil {
		return err
	}
	tx, err := s.read.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return fn(tx)
}

func snapshotOf(ctx context.Context, q querier, task Task) (TaskSnapshot, error) {
	snap := TaskSnapshot{Task: task}

	// A NULL on either side of an equality is not true, so a task with no
	// branch matches no run by branch.
	rows, err := q.QueryContext(ctx, runColumns+` FROM run
		WHERE (repository_id = ? AND branch = ? AND created_at >= ?) OR id = ?
		ORDER BY created_at DESC, id DESC`,
		task.RepositoryID, task.Branch, encodeTime(task.CreatedAt), task.RunID)
	if err != nil {
		return TaskSnapshot{}, err
	}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			_ = rows.Close()
			return TaskSnapshot{}, err
		}
		snap.Runs = append(snap.Runs, r)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return TaskSnapshot{}, err
	}

	query := holdColumns + ` FROM hold WHERE task_id = ?`
	args := []any{task.ID}
	if len(snap.Runs) > 0 {
		marks := make([]string, len(snap.Runs))
		for i, r := range snap.Runs {
			marks[i] = "?"
			args = append(args, r.ID)
		}
		query += ` OR run_id IN (` + strings.Join(marks, ",") + `)`
	}
	rows, err = q.QueryContext(ctx, query+` ORDER BY opened_at, key`, args...)
	if err != nil {
		return TaskSnapshot{}, err
	}
	for rows.Next() {
		h, err := scanHold(rows)
		if err != nil {
			_ = rows.Close()
			return TaskSnapshot{}, err
		}
		snap.Holds = append(snap.Holds, h)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return TaskSnapshot{}, err
	}

	rows, err = q.QueryContext(ctx, `SELECT task_id, sequence, kind, detail, at
		FROM task_event WHERE task_id = ? ORDER BY sequence`, task.ID)
	if err != nil {
		return TaskSnapshot{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var e TaskEvent
		var at string
		if err := rows.Scan(&e.TaskID, &e.Sequence, &e.Kind, &e.Detail, &at); err != nil {
			return TaskSnapshot{}, err
		}
		if e.At, err = decodeTime(at); err != nil {
			return TaskSnapshot{}, err
		}
		snap.Events = append(snap.Events, e)
	}
	return snap, rows.Err()
}
