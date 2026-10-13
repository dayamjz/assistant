package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// CleanupProof is which of PRD principle P12's three proofs a cleanup check
// established before an isolated copy could be removed. It is a closed set:
// a cleanup that established none writes no record, because a refusal is a
// result to investigate and not a fact about the task.
type CleanupProof string

const (
	// CleanupProofRemoteBranch is the work is reachable from a remote branch.
	CleanupProofRemoteBranch CleanupProof = "remote-branch"
	// CleanupProofMergedPullRequest is the pull request merged with a head
	// containing the work.
	CleanupProofMergedPullRequest CleanupProof = "merged-pull-request"
	// CleanupProofDefaultBranch is the content is already present in an
	// up-to-date default branch.
	CleanupProofDefaultBranch CleanupProof = "default-branch"
)

var cleanupProofs = []CleanupProof{CleanupProofRemoteBranch, CleanupProofMergedPullRequest, CleanupProofDefaultBranch}

// CleanupProofs returns the closed set, in the order above. The result is a
// copy, so a caller cannot add to the set by writing to it.
func CleanupProofs() []CleanupProof { return slices.Clone(cleanupProofs) }

// Recognized reports whether p is one of the proofs this package defines.
func (p CleanupProof) Recognized() bool { return slices.Contains(cleanupProofs, p) }

// String renders the proof as it is stored.
func (p CleanupProof) String() string { return string(p) }

// TaskCleanup is PRD section 8's cleanup record: written once per task,
// recording the cleanup check it was made from. PRD section 6's Complete is
// derived from it, by internal/taskstate once it reads this tier; this
// package records it and decides nothing from it.
type TaskCleanup struct {
	// TaskID names the task.
	TaskID string `json:"task_id"`
	// Proof is which of P12's proofs the check established.
	Proof CleanupProof `json:"proof"`
	// Examined is the commit identifiers the check read to establish it, in
	// the order it read them. A check that examined none records an empty
	// list, which is a known fact and not an unknown one.
	Examined []string `json:"examined"`
	// RecordedAt is when the record was written.
	RecordedAt time.Time `json:"recorded_at"`
	// RemovedAt is when the isolated copy was removed on the strength of the
	// proof, and unknown when the proof was recorded and the copy left
	// standing.
	RemovedAt Optional[time.Time] `json:"removed_at"`
}

// RecordTaskCleanup writes a task's cleanup record and returns it as stored.
// The record is written once: a task that already has one is refused with
// ErrCleanupRecorded and the first record stands. A proof outside the closed
// set is refused with ErrUnknownCleanupProof, and a task that does not exist
// with ErrNotFound, wrapped. RecordedAt is assigned here; the caller supplies
// the proof, what it examined, and whether the copy was removed.
//
// Nothing in this build calls it outside its tests. The writer is the cleanup
// check (#108), and PRD section 8 fixes the record's shape independently of
// when that lands.
func (s *Store) RecordTaskCleanup(ctx context.Context, c TaskCleanup) (TaskCleanup, error) {
	if strings.TrimSpace(c.TaskID) == "" {
		return TaskCleanup{}, fmt.Errorf("store: a cleanup record names no task")
	}
	if !c.Proof.Recognized() {
		return TaskCleanup{}, fmt.Errorf("store: task %s: %w: %q", c.TaskID, ErrUnknownCleanupProof, c.Proof)
	}
	examined := c.Examined
	if examined == nil {
		examined = []string{}
	}
	encoded, err := json.Marshal(examined)
	if err != nil {
		return TaskCleanup{}, fmt.Errorf("store: task %s: encoding the examined commits: %w", c.TaskID, err)
	}
	now := nowUTC()
	var removed Optional[string]
	if at, ok := c.RemovedAt.Get(); ok {
		removed = Known(encodeTime(at))
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if err := taskExists(ctx, tx, c.TaskID); err != nil {
			return err
		}
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM task_cleanup WHERE task_id = ?`, c.TaskID).Scan(&one); {
		case err == nil:
			return ErrCleanupRecorded
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO task_cleanup (task_id, proof, examined, recorded_at, removed_at)
			VALUES (?, ?, ?, ?, ?)`,
			c.TaskID, string(c.Proof), string(encoded), encodeTime(now), removed)
		return err
	})
	if err != nil {
		return TaskCleanup{}, fmt.Errorf("store: recording the cleanup of task %s: %w", c.TaskID, err)
	}
	return s.TaskCleanup(ctx, c.TaskID)
}

// TaskCleanup returns a task's cleanup record, or ErrNotFound when none has
// been written, which is the ordinary state of every task cleanup has not
// reached.
func (s *Store) TaskCleanup(ctx context.Context, taskID string) (TaskCleanup, error) {
	if err := s.live(); err != nil {
		return TaskCleanup{}, err
	}
	c, err := taskCleanupOf(ctx, s.read, taskID)
	if err != nil {
		return TaskCleanup{}, fmt.Errorf("store: reading the cleanup record of task %s: %w", taskID, err)
	}
	return c, nil
}

const taskCleanupColumns = `SELECT task_id, proof, examined, recorded_at, removed_at`

func taskCleanupOf(ctx context.Context, q querier, taskID string) (TaskCleanup, error) {
	var c TaskCleanup
	var proof, examined, recorded string
	var removed Optional[string]
	if err := q.QueryRowContext(ctx, taskCleanupColumns+` FROM task_cleanup WHERE task_id = ?`, taskID).
		Scan(&c.TaskID, &proof, &examined, &recorded, &removed); err != nil {
		return TaskCleanup{}, errNoRows(err)
	}
	c.Proof = CleanupProof(proof)
	if !c.Proof.Recognized() {
		return TaskCleanup{}, fmt.Errorf("column proof: %w: %q", ErrUnknownCleanupProof, proof)
	}
	if err := json.Unmarshal([]byte(examined), &c.Examined); err != nil {
		return TaskCleanup{}, fmt.Errorf("column examined: %w", err)
	}
	if c.Examined == nil {
		c.Examined = []string{}
	}
	var err error
	if c.RecordedAt, err = decodeTime(recorded); err != nil {
		return TaskCleanup{}, err
	}
	if at, ok := removed.Get(); ok {
		t, err := decodeTime(at)
		if err != nil {
			return TaskCleanup{}, err
		}
		c.RemovedAt = Known(t)
	}
	return c, nil
}
