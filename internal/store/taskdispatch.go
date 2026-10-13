package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// SnapshotRef is one reference and the commit it pointed at when a snapshot
// was taken.
type SnapshotRef struct {
	// Name is the full reference name, such as refs/heads/main.
	Name string `json:"name"`
	// Commit is the commit the reference reached, annotated tags followed.
	Commit string `json:"commit"`
}

// CopySnapshot is what a task's isolated copy held at the moment its dispatch
// proved it isolated: HEAD's commit, the branch HEAD was on, and every local
// reference. It is the start of the inventory PRD section 6's cleanup proof
// compares a copy's references against, so a reference the worker added or
// moved afterwards can be told from one the copy was cut with.
//
// It describes the copy as observed then and claims nothing about it now.
// Nothing resolves a task's state from it.
type CopySnapshot struct {
	// Head is the commit HEAD resolved to.
	Head string `json:"head"`
	// Branch is the short name of the branch HEAD was on, and empty when
	// HEAD was detached, which is an investigation copy's shape.
	Branch string `json:"branch"`
	// Refs is every reference in the copy, in the order the repository
	// listed them. An empty list is a known fact and not an unknown one.
	Refs []SnapshotRef `json:"refs"`
}

// PrimarySnapshot is what the person's primary checkout looked like when a
// dispatch read it to cut a task's copy: HEAD's commit, the branch HEAD was
// on if any, and every local branch with its commit. It is read through
// internal/vcs's read-only operations and the read changes nothing in the
// checkout.
//
// Its claim is exactly "what the primary looked like at dispatch". A person
// moves their own checkout legitimately, so a difference read later is
// evidence that the checkout changed and never evidence of who changed it; a
// consumer that reported a worker from this record would be claiming more
// than the record holds.
type PrimarySnapshot struct {
	// TaskID names the task whose dispatch read the checkout.
	TaskID string `json:"task_id"`
	// Attempt is the attempt the dispatch was claimed as, which is the row's
	// key beside the task: one snapshot per attempt, written once.
	Attempt int64 `json:"attempt"`
	// Head is the commit HEAD resolved to.
	Head string `json:"head"`
	// HeadBranch is the short name of the branch HEAD was on, and unknown
	// when HEAD was detached.
	HeadBranch Optional[string] `json:"head_branch"`
	// Branches is every local branch, refs/heads/..., with its commit, in
	// the order the repository listed them.
	Branches []SnapshotRef `json:"branches"`
	// RecordedAt is when the record was written.
	RecordedAt time.Time `json:"recorded_at"`
}

// SetTaskStartSnapshot records what the task's copy held when its dispatch
// proved it isolated. It is rewritten by each dispatch that reaches proven,
// so the column holds the latest; an attempt that was refused writes nothing
// and the previous snapshot, if any, stands. A task that does not exist is
// ErrNotFound, wrapped, and a snapshot with no head is refused, because a
// copy with no HEAD was not observed.
func (s *Store) SetTaskStartSnapshot(ctx context.Context, id string, snapshot CopySnapshot) error {
	if strings.TrimSpace(snapshot.Head) == "" {
		return fmt.Errorf("store: task %s: the start snapshot has no head", id)
	}
	if snapshot.Refs == nil {
		snapshot.Refs = []SnapshotRef{}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("store: task %s: encoding the start snapshot: %w", id, err)
	}
	return s.updateTask(ctx, id, "start_snapshot", string(encoded))
}

// RecordPrimarySnapshot writes what the primary checkout looked like when the
// attempt's dispatch read it, once per attempt: a second write for the same
// task and attempt is refused with ErrPrimarySnapshotRecorded and the first
// stands, because the record is of one read and a rewrite would say the
// checkout looked some other way at that moment. The attempt must exist,
// which the row's foreign key holds and which a missing one reports as
// ErrNotFound, wrapped. RecordedAt is assigned here.
func (s *Store) RecordPrimarySnapshot(ctx context.Context, p PrimarySnapshot) (PrimarySnapshot, error) {
	if strings.TrimSpace(p.TaskID) == "" {
		return PrimarySnapshot{}, fmt.Errorf("store: a primary snapshot names no task")
	}
	if p.Attempt < 1 {
		return PrimarySnapshot{}, fmt.Errorf("store: task %s: a primary snapshot names attempt %d, and attempts count from one", p.TaskID, p.Attempt)
	}
	if strings.TrimSpace(p.Head) == "" {
		return PrimarySnapshot{}, fmt.Errorf("store: task %s: the primary snapshot has no head", p.TaskID)
	}
	branches := p.Branches
	if branches == nil {
		branches = []SnapshotRef{}
	}
	encoded, err := json.Marshal(branches)
	if err != nil {
		return PrimarySnapshot{}, fmt.Errorf("store: task %s: encoding the primary's branches: %w", p.TaskID, err)
	}
	now := nowUTC()
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM task_attempt WHERE task_id = ? AND sequence = ?`,
			p.TaskID, p.Attempt).Scan(&one); err != nil {
			return fmt.Errorf("attempt %d: %w", p.Attempt, errNoRows(err))
		}
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM task_primary_snapshot WHERE task_id = ? AND attempt = ?`,
			p.TaskID, p.Attempt).Scan(&one); {
		case err == nil:
			return ErrPrimarySnapshotRecorded
		case !isNoRows(err):
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO task_primary_snapshot (task_id, attempt, head, head_branch, branches, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			p.TaskID, p.Attempt, p.Head, p.HeadBranch, string(encoded), encodeTime(now))
		return err
	})
	if err != nil {
		return PrimarySnapshot{}, fmt.Errorf("store: recording the primary snapshot of task %s: %w", p.TaskID, err)
	}
	snapshots, err := s.PrimarySnapshots(ctx, p.TaskID)
	if err != nil {
		return PrimarySnapshot{}, err
	}
	for _, got := range snapshots {
		if got.Attempt == p.Attempt {
			return got, nil
		}
	}
	return PrimarySnapshot{}, fmt.Errorf("store: the primary snapshot of task %s attempt %d was not read back", p.TaskID, p.Attempt)
}

// PrimarySnapshots returns a task's primary snapshots in attempt order, and
// nothing for a task that has none or does not exist.
func (s *Store) PrimarySnapshots(ctx context.Context, taskID string) ([]PrimarySnapshot, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT task_id, attempt, head, head_branch, branches, recorded_at
		FROM task_primary_snapshot WHERE task_id = ? ORDER BY attempt`, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: reading the primary snapshots of task %s: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []PrimarySnapshot
	for rows.Next() {
		var p PrimarySnapshot
		var branches, recorded string
		if err := rows.Scan(&p.TaskID, &p.Attempt, &p.Head, &p.HeadBranch, &branches, &recorded); err != nil {
			return nil, fmt.Errorf("store: reading the primary snapshots of task %s: %w", taskID, err)
		}
		if err := json.Unmarshal([]byte(branches), &p.Branches); err != nil {
			return nil, fmt.Errorf("store: reading the primary snapshots of task %s: column branches: %w", taskID, err)
		}
		if p.Branches == nil {
			p.Branches = []SnapshotRef{}
		}
		if p.RecordedAt, err = decodeTime(recorded); err != nil {
			return nil, fmt.Errorf("store: reading the primary snapshots of task %s: %w", taskID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the primary snapshots of task %s: %w", taskID, err)
	}
	return out, nil
}
