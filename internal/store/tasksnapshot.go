package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
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
	// Attempts are the task's attempts in sequence order, and empty for a
	// task never launched. The last one is the attempt whose start is the
	// attribution cutoff and whose session is the one a terminal probe asks
	// about.
	Attempts []TaskAttempt
	// Runs are the runs attributed to the task, newest first: those of the
	// task's own repository and branch created at or after its latest attempt
	// began, plus the run the task names directly, if any. A task with no
	// attempt is attributed nothing by its branch, so a task queued behind
	// another on the same branch does not inherit the blocker's runs. A task
	// with neither a branch nor a named run has none.
	Runs []Run
	// Holds are the holds, open or resolved, that name the task or one of
	// Runs, oldest first.
	Holds []Hold
	// Events are the task's whole history in append order.
	Events []TaskEvent
	// MergeRecords are the merge records of Runs, in Runs' order, for every
	// attributed run whose pull request's read ended it; a run with none is
	// absent. They are read inside the snapshot's own transaction through
	// the scanner Store.MergeRecord shares, because Store.MergeRecord reads
	// outside any transaction and a record read there could belong to a run
	// the snapshot did not see. They are carried for the resolver's terminal
	// tier, internal/taskstate, which derives Landed from one of them.
	MergeRecords []MergeRecord
	// Cleanup is the task's cleanup record, and unknown while none has been
	// written. It is carried for the same tier, which derives Complete from
	// it.
	Cleanup Optional[TaskCleanup]
	// LocalLanding is the task's local landing record, and unknown while no
	// approved fast-forward has delivered it. It is carried for the same
	// tier, which derives a local task's Landed from it.
	LocalLanding Optional[LocalLanding]
	// ReportStoredAt is when the task's report row was written, read from
	// the task_report row StoreTaskReport writes, and unknown while the task
	// has no stored report. Only the instant is carried, not the report: the
	// row's existence is the fact internal/taskstate's report tier resolves
	// Reported from, and the body is evidence a reader asks TaskReport for.
	// It is the row and never the ReportReadyEventKind entry appended in the
	// same transaction, so the answer rests on the record and not on a line
	// of the history (P8).
	ReportStoredAt Optional[time.Time]
	// Relay is the task's findings relayed record, and unknown while none
	// has been written. It is carried for internal/taskstate's terminal
	// tier, which derives an investigation's Complete from it, and it is
	// the row and never the FindingsRelayedEventKind entry appended beside
	// it (P8).
	Relay Optional[TaskRelay]
	// Retirement is the task's housekeeping retirement record, and unknown
	// for a pass not retired and for every task that is not a pass. It is
	// carried for the readers of whether a pass is open and for the attempt
	// anchor; internal/taskstate's resolver does not read it, so a retired
	// pass resolves what its own records give (PRD section 8).
	Retirement Optional[HousekeepingRetirement]
}

// TaskSnapshot reads one task and the records its state is resolved from, as
// of a single point in time. It returns ErrNotFound for an unknown task.
//
// Attribution by branch is a heuristic and says so. The cutoff is the start
// of the task's latest attempt, and a run is kept only when no other task of
// the same repository and branch has an attempt that started after that
// cutoff and at or before the run. That keeps a queued task from inheriting
// the runs of the task it waits behind, a relaunched task from being
// outranked by a run from before the relaunch, and a task whose attempt was
// refused from reading a later task's run on the same branch. A task with no
// attempt is attributed nothing by its branch.
//
// A task recorded before migration 14 has no attempt row, so after it such a
// task is attributed nothing by its branch where it was attributed by its
// creation time before: only the run it names directly remains its. No task
// in this repository's production paths had been created when the cutoff
// moved, so no record changes meaning; a database that did hold one would
// read the task's runs as not its until an attempt is begun for it.
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

	attempts, err := taskAttemptsOf(ctx, q, task.ID)
	if err != nil {
		return TaskSnapshot{}, err
	}
	snap.Attempts = attempts

	// A task with no attempt is attributed nothing by its branch, so only the
	// run it names is read. With one, a NULL on either side of an equality is
	// not true, so a task with no branch still matches no run by branch.
	query, args := runColumns+` FROM run WHERE id = ?`, []any{task.RunID}
	if n := len(attempts); n > 0 {
		cutoff := encodeTime(attempts[n-1].StartedAt)
		query = runColumns + ` FROM run WHERE (repository_id = ? AND branch = ? AND created_at >= ?
			AND NOT EXISTS (
				SELECT 1 FROM task_attempt later
				JOIN task other ON other.id = later.task_id
				WHERE other.repository_id = run.repository_id AND other.branch = run.branch
					AND other.id <> ? AND later.started_at > ? AND later.started_at <= run.created_at
			)) OR id = ?`
		args = []any{task.RepositoryID, task.Branch, cutoff, task.ID, cutoff, task.RunID}
	}
	rows, err := q.QueryContext(ctx, query+` ORDER BY created_at DESC, id DESC`, args...)
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

	// One read per attributed run, skipping a run with no record, inside the
	// same transaction as the runs and through the column list and scanner
	// Store.MergeRecord reads with. A run's record is keyed by the run, so
	// which records are the task's follows from which runs are.
	for _, r := range snap.Runs {
		record, err := scanMergeRecord(q.QueryRowContext(ctx, mergeRecordColumns+` FROM merge_record WHERE run = ?`, r.ID))
		switch {
		case isNoRows(err):
			continue
		case err != nil:
			return TaskSnapshot{}, fmt.Errorf("reading the merge record of run %s: %w", r.ID, err)
		}
		snap.MergeRecords = append(snap.MergeRecords, record)
	}

	switch cleanup, err := taskCleanupOf(ctx, q, task.ID); {
	case err == nil:
		snap.Cleanup = Known(cleanup)
	case !errors.Is(err, ErrNotFound):
		return TaskSnapshot{}, err
	}

	switch landing, err := localLandingOf(ctx, q, task.ID); {
	case err == nil:
		snap.LocalLanding = Known(landing)
	case !isNoRows(err):
		return TaskSnapshot{}, fmt.Errorf("reading the local landing record of task %s: %w", task.ID, err)
	}

	var reported string
	switch err := q.QueryRowContext(ctx, `SELECT recorded_at FROM task_report WHERE task_id = ?`, task.ID).Scan(&reported); {
	case err == nil:
		at, err := decodeTime(reported)
		if err != nil {
			return TaskSnapshot{}, fmt.Errorf("reading the report of task %s: %w", task.ID, err)
		}
		snap.ReportStoredAt = Known(at)
	case !isNoRows(err):
		return TaskSnapshot{}, fmt.Errorf("reading the report of task %s: %w", task.ID, err)
	}

	switch relay, err := taskRelayOf(ctx, q, task.ID); {
	case err == nil:
		snap.Relay = Known(relay)
	case !isNoRows(err):
		return TaskSnapshot{}, fmt.Errorf("reading the findings relayed record of task %s: %w", task.ID, err)
	}

	switch retirement, retired, err := retirementOf(ctx, q, task.ID); {
	case err != nil:
		return TaskSnapshot{}, err
	case retired:
		snap.Retirement = Known(retirement)
	}

	query = holdColumns + ` FROM hold WHERE task_id = ?`
	args = []any{task.ID}
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
