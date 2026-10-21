package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The dispatch hold, PRD section 6's "kept work is answered before new task
// work starts": while a project has an open kept work entry, no new task in
// that project dispatches. This file is the one statement of which entries
// hold which task, keptWorkHoldingTx, read by KeptWorkHolding and by
// BeginDispatchAttempt's claim, so the wait a surface shows and the refusal a
// dispatch meets cannot disagree about what holds it.
//
// A task is held when all three are true in one read: it names a project
// (its repository), no attempt of it has claimed a worker launch or
// recorded a terminal session, and an entry of that project is open
// (neither answered nor closed). The second is what makes it a new task: a
// task whose worker launch was claimed once is work already under way, which
// PRD section 6 says the hold does not interrupt, so neither its relaunch,
// nor a fallback to its backup worker, nor a dispatch retried after a
// launch is held.
//
// The hold is checked at both of the moments new work begins, each inside
// the transaction that records it: BeginDispatchAttempt, the attempt claim a
// dispatch makes before it cuts a copy, and ClaimAttemptLaunch, the claim a
// launch makes immediately before it starts the worker's process. An entry
// recorded after a dispatch claimed its attempt therefore still stops the
// worker from starting. What neither closes is the instant between the
// launch claim's commit and the process start: an entry recorded there does
// not stop that launch, which by the record had already begun when the
// entry was written. An entry naming no project, an investigation's started without a
// repository, holds no task, since kept work in one project never holds
// another and that entry is in none.
//
// Nothing here is cached or appended: the hold is derived from the kept work
// record and the attempt record on every read (P8), so an entry's answer or
// its closure on a proof lifts it on the next read, and a restart reads the
// same answer the service before it did.

// ErrDispatchHeld matches every *DispatchHeldError.
var ErrDispatchHeld = errors.New("store: the task's project holds kept work nobody has decided, so the task is not dispatched")

// DispatchHeldError is the refusal of a new task's dispatch while its
// project has open kept work. BeginDispatchAttempt returns it and writes no
// attempt. It names every entry that holds the task, oldest first.
type DispatchHeldError struct {
	// Task is the task that was not dispatched.
	Task string
	// Repository is its project.
	Repository string
	// Entries is every open entry of that project, oldest first, and never
	// empty.
	Entries []KeptWork
}

// Error names the task, the project and each entry's copy and owner.
func (e *DispatchHeldError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "store: task %s is not dispatched, because its project %s holds kept work nobody has decided:", e.Task, e.Repository)
	for _, k := range e.Entries {
		owner := "run " + k.RunID.Or("")
		if task, ok := k.TaskID.Get(); ok {
			owner = "task " + task
		}
		fmt.Fprintf(&b, " entry %d, the copy of %s at %s (%s);", k.ID, owner, k.CopyPath, k.Reason)
	}
	b.WriteString(" it dispatches once every one of them is answered or closed")
	return b.String()
}

// Unwrap makes the refusal match ErrDispatchHeld.
func (e *DispatchHeldError) Unwrap() error { return ErrDispatchHeld }

// KeptWorkHolding returns the open kept work entries that hold the task's
// dispatch, oldest first, on this file's terms, and none for a task that is
// not held: one naming no project, one launched before, or one whose project
// has no open entry. A task that does not exist is ErrNotFound, wrapped. It
// is the read a dispatch and a surface decide a held task from; the claim a
// dispatch makes reads the same thing again inside its own transaction.
func (s *Store) KeptWorkHolding(ctx context.Context, taskID string) ([]KeptWork, error) {
	var out []KeptWork
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, _, err = keptWorkHoldingTx(ctx, tx, taskID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("store: reading the kept work that holds task %s: %w", taskID, err)
	}
	return out, nil
}

// OpenKeptWorkOf returns the open kept work entries whose project is the
// repository, oldest first. It is the read a run's answer names its
// project's kept work from; it holds nothing, because a run is never held
// by kept work.
func (s *Store) OpenKeptWorkOf(ctx context.Context, repositoryID string) ([]KeptWork, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, openKeptWorkOfProject, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("store: reading the open kept work of repository %s: %w", repositoryID, err)
	}
	out, err := scanKeptWorkRows(rows)
	if err != nil {
		return nil, fmt.Errorf("store: reading the open kept work of repository %s: %w", repositoryID, err)
	}
	return out, nil
}

// BeginDispatchAttempt is BeginTaskAttempt for a dispatch: the same anchored
// claim, made only when the task is not held by kept work, which it decides
// from keptWorkHoldingTx in the transaction that would write the attempt. A
// held task is refused with a *DispatchHeldError and nothing is written, so
// an entry recorded after a dispatcher's own read still holds the dispatch
// at its claim. A relaunch, which is not new task work, claims through
// BeginTaskAttempt and is never held.
func (s *Store) BeginDispatchAttempt(ctx context.Context, taskID string, expectedLatest int64) (TaskAttempt, error) {
	return s.beginTaskAttempt(ctx, taskID, expectedLatest, true)
}

// ErrAttemptLaunchClaimed is returned by ClaimAttemptLaunch for an attempt
// whose launch was already claimed. An attempt is one process; a second
// launch is a new attempt.
var ErrAttemptLaunchClaimed = errors.New("store: the attempt's launch is already claimed")

// ClaimAttemptLaunch records that the attempt's worker is about to be
// started, once, in a transaction that first asks keptWorkHoldingTx: a task
// kept work holds is refused with a *DispatchHeldError and nothing is
// written. A caller starts the worker only after this returns nil, and makes
// no further write that could refuse between the two. An attempt already
// claimed is ErrAttemptLaunchClaimed and one that does not exist is
// ErrNotFound, both wrapped. A task launched before is never held, so a
// relaunch's or a fallback's claim is refused only for being claimed twice.
func (s *Store) ClaimAttemptLaunch(ctx context.Context, taskID string, sequence int64) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var claimed Optional[string]
		if err := tx.QueryRowContext(ctx,
			`SELECT launch_claimed_at FROM task_attempt WHERE task_id = ? AND sequence = ?`, taskID, sequence).
			Scan(&claimed); err != nil {
			return fmt.Errorf("task %s attempt %d: %w", taskID, sequence, errNoRows(err))
		}
		if claimed.IsKnown() {
			return fmt.Errorf("task %s attempt %d: %w", taskID, sequence, ErrAttemptLaunchClaimed)
		}
		holding, project, err := keptWorkHoldingTx(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if len(holding) > 0 {
			return &DispatchHeldError{Task: taskID, Repository: project, Entries: holding}
		}
		_, err = tx.ExecContext(ctx, `UPDATE task_attempt SET launch_claimed_at = ? WHERE task_id = ? AND sequence = ?`,
			encodeTime(nowUTC()), taskID, sequence)
		return err
	})
	var held *DispatchHeldError
	if err != nil && !errors.As(err, &held) {
		return fmt.Errorf("store: claiming the launch of task %s attempt %d: %w", taskID, sequence, err)
	}
	return err
}

// keptWorkHoldingTx is the hold's one predicate, on this file's terms. It
// returns the entries that hold the task and the task's project, which is
// empty for a task naming none.
func keptWorkHoldingTx(ctx context.Context, tx *sql.Tx, taskID string) ([]KeptWork, string, error) {
	var repository Optional[string]
	if err := tx.QueryRowContext(ctx, `SELECT repository_id FROM task WHERE id = ?`, taskID).Scan(&repository); err != nil {
		return nil, "", fmt.Errorf("task %s: %w", taskID, errNoRows(err))
	}
	project, ok := repository.Get()
	if !ok {
		return nil, "", nil
	}
	var launched int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_attempt WHERE task_id = ? AND (session_ref IS NOT NULL OR launch_claimed_at IS NOT NULL)`,
		taskID).Scan(&launched); err != nil {
		return nil, project, err
	}
	if launched > 0 {
		return nil, project, nil
	}
	rows, err := tx.QueryContext(ctx, openKeptWorkOfProject, project)
	if err != nil {
		return nil, project, err
	}
	out, err := scanKeptWorkRows(rows)
	return out, project, err
}

// openKeptWorkOfProject is the query for one project's open entries, oldest
// first, which the hold, a run's answer and a repository's removal all read.
const openKeptWorkOfProject = keptWorkColumns + ` FROM kept_work
	WHERE repository_id = ? AND answer IS NULL AND closure IS NULL ORDER BY id`

// scanKeptWorkRows reads every row of rows as a kept work entry and closes
// rows.
func scanKeptWorkRows(rows *sql.Rows) ([]KeptWork, error) {
	defer func() { _ = rows.Close() }()
	var out []KeptWork
	for rows.Next() {
		k, err := scanKeptWork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
