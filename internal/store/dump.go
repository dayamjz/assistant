package store

import (
	"context"
	"fmt"
)

// Dump is one consistent read of every record PRD section 9's dump names:
// each task as TaskSnapshot, every wake of the task wake table, and every
// kept work entry. It is a window onto the store, not a substitute for it.
// Domain, kept-work and housekeeping wakes are not in it.
type Dump struct {
	Tasks    []TaskSnapshot `json:"tasks"`
	Wakes    []Wake         `json:"wakes"`
	KeptWork []KeptWork     `json:"kept_work"`
}

// Dump reads every task snapshot, every task wake and every kept work entry
// as of one point in time. It writes nothing.
func (s *Store) Dump(ctx context.Context) (Dump, error) {
	var out Dump
	err := s.snapshotTx(ctx, func(q querier) error {
		snaps, err := snapshotsOf(ctx, q)
		if err != nil {
			return err
		}
		out.Tasks = snaps
		if out.Wakes, err = wakesOf(ctx, q, ""); err != nil {
			return err
		}
		out.KeptWork, err = keptWorkRowsOf(ctx, q, "")
		return err
	})
	if err != nil {
		return Dump{}, fmt.Errorf("store: dumping the records: %w", err)
	}
	return out, nil
}

// DumpTask is Dump narrowed to one task. ErrNotFound for an unknown task.
func (s *Store) DumpTask(ctx context.Context, id string) (Dump, error) {
	var out Dump
	err := s.snapshotTx(ctx, func(q querier) error {
		row := q.QueryRowContext(ctx, taskColumns+` FROM task WHERE id = ?`, id)
		task, err := scanTask(row)
		if err != nil {
			return errNoRows(err)
		}
		snap, err := snapshotOf(ctx, q, task)
		if err != nil {
			return err
		}
		out.Tasks = []TaskSnapshot{snap}
		if out.Wakes, err = wakesOf(ctx, q, id); err != nil {
			return err
		}
		out.KeptWork, err = keptWorkRowsOf(ctx, q, id)
		return err
	})
	if err != nil {
		return Dump{}, fmt.Errorf("store: dumping the records of task %s: %w", id, err)
	}
	return out, nil
}

func snapshotsOf(ctx context.Context, q querier) ([]TaskSnapshot, error) {
	rows, err := q.QueryContext(ctx, taskColumns+` FROM task ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	out := make([]TaskSnapshot, 0, len(tasks))
	for _, task := range tasks {
		snap, err := snapshotOf(ctx, q, task)
		if err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, nil
}

func wakesOf(ctx context.Context, q querier, taskID string) ([]Wake, error) {
	query, args := wakeColumns+` FROM wake ORDER BY id`, []any(nil)
	if taskID != "" {
		query, args = wakeColumns+` FROM wake WHERE task_id = ? ORDER BY id`, []any{taskID}
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return collectWakes(rows)
}

func keptWorkRowsOf(ctx context.Context, q querier, taskID string) ([]KeptWork, error) {
	query, args := keptWorkColumns+` FROM kept_work ORDER BY id`, []any(nil)
	if taskID != "" {
		query, args = keptWorkColumns+` FROM kept_work WHERE task_id = ? ORDER BY id`, []any{taskID}
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanKeptWorkRows(rows)
}
