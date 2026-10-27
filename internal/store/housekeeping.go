package store

import (
	"context"
	"database/sql"
	"fmt"
)

// HousekeepingPasses reads every housekeeping pass of the repository as
// TaskSnapshot reads one, newest first, as of one point in time.
func (s *Store) HousekeepingPasses(ctx context.Context, repository string) ([]TaskSnapshot, error) {
	var out []TaskSnapshot
	err := s.snapshotTx(ctx, func(q querier) error {
		var err error
		out, err = passesOf(ctx, q, repository)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: reading the housekeeping passes of repository %s: %w", repository, err)
	}
	return out, nil
}

// passesOf reads the repository's passes inside the caller's transaction,
// newest first.
func passesOf(ctx context.Context, q querier, repository string) ([]TaskSnapshot, error) {
	rows, err := q.QueryContext(ctx, taskColumns+` FROM task WHERE repository_id = ? AND housekeeping = 1
		ORDER BY created_at DESC, rowid DESC`, repository)
	if err != nil {
		return nil, err
	}
	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		tasks = append(tasks, t)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]TaskSnapshot, 0, len(tasks))
	for _, t := range tasks {
		snap, err := snapshotOf(ctx, q, t)
		if err != nil {
			return nil, err
		}
		out = append(out, snap)
	}
	return out, nil
}

// PassOpen reports whether a housekeeping pass's snapshot leaves it open on
// PRD section 6's terms: not, by delivered, in the terminal tier. It is the
// one statement of the rule, for CreateTask's transaction and every reader
// alike.
func PassOpen(snap TaskSnapshot, delivered func(TaskSnapshot) bool) bool {
	return snap.Task.Housekeeping && !delivered(snap)
}

// openPassTx names the newest open pass of the repository, read inside the
// creating transaction, or unknown when every pass is closed.
func openPassTx(ctx context.Context, tx *sql.Tx, repository string, delivered func(TaskSnapshot) bool) (Optional[string], error) {
	passes, err := passesOf(ctx, tx, repository)
	if err != nil {
		return Optional[string]{}, fmt.Errorf("reading the housekeeping passes of repository %s: %w", repository, err)
	}
	for _, p := range passes {
		if PassOpen(p, delivered) {
			return Known(p.Task.ID), nil
		}
	}
	return Optional[string]{}, nil
}
