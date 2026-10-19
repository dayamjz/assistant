package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/home"
)

// DelegationRecovery is whether the parent may take a delegated task back
// onto a local attempt. It is a closed set. None never does. RelaunchIfDead
// does only when the latest attempt has a delegation_attempt_end row, which
// is a record that the attempt was proven dead and not a reading of a
// connection.
type DelegationRecovery string

const (
	// RecoveryNone never relaunches a delegated task on the parent. A dead
	// child and a parent restart both leave the task where the delegation
	// put it.
	RecoveryNone DelegationRecovery = "none"
	// RecoveryRelaunchIfDead relaunches on the parent only when the latest
	// attempt has a delegation_attempt_end row. An unobserved child is not
	// that row.
	RecoveryRelaunchIfDead DelegationRecovery = "relaunch-if-dead"
)

// Recognized reports whether r is one of the two policies a delegation row
// can hold. Anything else is refused at the accessor and never stored.
func (r DelegationRecovery) Recognized() bool {
	return r == RecoveryNone || r == RecoveryRelaunchIfDead
}

// Delegation is the parent's record that one of its tasks runs in a child
// home. The parent store is the owner of the task. The row does not deliver
// the task and does not say whether the child is running.
type Delegation struct {
	TaskID     string             `json:"task_id"`
	ChildHome  string             `json:"child_home"`
	Recovery   DelegationRecovery `json:"recovery"`
	RecordedAt time.Time          `json:"recorded_at"`
}

// DelegationGrant is the one grant a subordinate home holds: which parent
// started it, which tasks it may run, which of the parent's working copies
// it may not bind, and the kernel identity of the parent process that wrote
// it. Merge is not a field, and neither is permission to bind the parent's
// branches. A caller cannot set either, because there is nowhere to set it.
type DelegationGrant struct {
	ParentHome     string    `json:"parent_home"`
	Tasks          []string  `json:"tasks"`
	ParentBranches []string  `json:"parent_branches"`
	ParentPID      int       `json:"parent_pid"`
	ParentUID      int       `json:"parent_uid"`
	ParentGID      int       `json:"parent_gid"`
	RecordedAt     time.Time `json:"recorded_at"`
}

// DelegationDelivery is the parent's once-only record that delivery of a
// delegated task was accepted. It is not a merge record and not a local
// landing. internal/taskstate does not read it, so it resolves nothing.
type DelegationDelivery struct {
	TaskID     string    `json:"task_id"`
	RecordedAt time.Time `json:"recorded_at"`
}

// ErrGrantRecorded is returned when a home already has a subordinate grant.
// The first grant stands. A different task list is not a replacement.
var ErrGrantRecorded = errors.New("store: a subordinate grant is written once")

// ErrDelegationRecorded is returned when the task already has a delegation.
// The first child home stands.
var ErrDelegationRecorded = errors.New("store: the task is already delegated")

// ErrDelegationDelivered is returned when delivery of the delegated task was
// already recorded. The first record stands.
var ErrDelegationDelivered = errors.New("store: delivery of this delegated task is already recorded")

// ErrDelegationAttemptEnded is returned when that attempt already has an end
// record.
var ErrDelegationAttemptEnded = errors.New("store: the delegated attempt is already recorded dead")

// ErrParentGate is returned when a working copy is the parent's home or one
// of the working copies the grant names as the parent's. The comparison is
// the cleaned path. A symlink that resolves into one of those paths is not
// what this looks at.
var ErrParentGate = errors.New("store: a subordinate grant does not include the parent's branches")

// RecordDelegationGrant writes the home's one subordinate grant. A second
// call returns ErrGrantRecorded and leaves the first row in place, including
// a second call whose task list is longer. tasks must be non-empty valid
// task identifiers. parentHome and every parent branch must be absolute.
// The parent identity must be a real process and user: a non-positive pid
// or a negative uid or gid is refused, which is what a platform with no
// user identity looks like, and no row is written.
func (s *Store) RecordDelegationGrant(ctx context.Context, g DelegationGrant) (DelegationGrant, error) {
	parent, err := cleanAbsolute(g.ParentHome)
	if err != nil {
		return DelegationGrant{}, fmt.Errorf("store: a subordinate grant's parent home: %w", err)
	}
	tasks, taskText, err := canonTasks(g.Tasks)
	if err != nil {
		return DelegationGrant{}, err
	}
	branches, branchText, err := canonPaths(g.ParentBranches)
	if err != nil {
		return DelegationGrant{}, err
	}
	if g.ParentPID <= 0 || g.ParentUID < 0 || g.ParentGID < 0 {
		return DelegationGrant{}, fmt.Errorf("store: a subordinate grant needs the parent's pid, uid and gid")
	}
	now := nowUTC()
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM delegation_grant WHERE singleton = 1`).Scan(&one); {
		case err == nil:
			return ErrGrantRecorded
		case !isNoRows(err):
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO delegation_grant
				(singleton, parent_home, tasks, parent_branches, parent_pid, parent_uid, parent_gid, recorded_at)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?)`,
			parent, taskText, branchText, g.ParentPID, g.ParentUID, g.ParentGID, encodeTime(now))
		return err
	})
	if err != nil {
		if errors.Is(err, ErrGrantRecorded) {
			return DelegationGrant{}, err
		}
		return DelegationGrant{}, fmt.Errorf("store: recording a subordinate grant: %w", err)
	}
	return DelegationGrant{
		ParentHome: parent, Tasks: tasks, ParentBranches: branches,
		ParentPID: g.ParentPID, ParentUID: g.ParentUID, ParentGID: g.ParentGID,
		RecordedAt: now,
	}, nil
}

// DelegationGrant reads the home's subordinate grant. A home with no row
// returns ErrNotFound, wrapped: that is a parent, or a home nothing has
// delegated to, and it is not a grant that permits everything.
func (s *Store) DelegationGrant(ctx context.Context) (DelegationGrant, error) {
	if err := s.live(); err != nil {
		return DelegationGrant{}, err
	}
	g, err := delegationGrantOf(ctx, s.read)
	if err != nil {
		return DelegationGrant{}, fmt.Errorf("store: reading the subordinate grant: %w", errNoRows(err))
	}
	return g, nil
}

// RefuseGate reports whether workingPath is the parent home or a working
// copy the grant names as the parent's, after filepath.Clean. A relative
// path is refused. A path that is not one of those is allowed, which is
// the child's own copy and nothing this function creates.
func (g DelegationGrant) RefuseGate(workingPath string) error {
	cleaned, err := cleanAbsolute(workingPath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrParentGate, err)
	}
	if pathUnder(cleaned, g.ParentHome) {
		return fmt.Errorf("%w: %s is inside the parent home", ErrParentGate, cleaned)
	}
	for _, branch := range g.ParentBranches {
		if pathUnder(cleaned, branch) {
			return fmt.Errorf("%w: %s is a parent branch", ErrParentGate, cleaned)
		}
	}
	return nil
}

// RecordDelegation writes the parent's delegation of one task to childHome
// under recovery. The task must exist. A second call for the same task
// returns ErrDelegationRecorded. childHome must be absolute and must not be
// empty. recovery must be one of the two named policies.
func (s *Store) RecordDelegation(ctx context.Context, taskID, childHome string, recovery DelegationRecovery) (Delegation, error) {
	if err := home.ValidTaskID(taskID); err != nil {
		return Delegation{}, fmt.Errorf("store: delegation of %q: %w", taskID, err)
	}
	child, err := cleanAbsolute(childHome)
	if err != nil {
		return Delegation{}, fmt.Errorf("store: delegation of %s: %w", taskID, err)
	}
	if !recovery.Recognized() {
		return Delegation{}, fmt.Errorf("store: delegation of %s has recovery %q, which is neither %q nor %q",
			taskID, recovery, RecoveryNone, RecoveryRelaunchIfDead)
	}
	now := nowUTC()
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if err := taskExists(ctx, tx, taskID); err != nil {
			return err
		}
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM delegation WHERE task_id = ?`, taskID).Scan(&one); {
		case err == nil:
			return ErrDelegationRecorded
		case !isNoRows(err):
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO delegation (task_id, child_home, recovery, recorded_at) VALUES (?, ?, ?, ?)`,
			taskID, child, string(recovery), encodeTime(now))
		return err
	})
	if err != nil {
		if errors.Is(err, ErrDelegationRecorded) || errors.Is(err, ErrNotFound) {
			return Delegation{}, err
		}
		return Delegation{}, fmt.Errorf("store: recording the delegation of %s: %w", taskID, err)
	}
	return Delegation{TaskID: taskID, ChildHome: child, Recovery: recovery, RecordedAt: now}, nil
}

// Delegation reads one task's delegation. A task with no row returns
// ErrNotFound, wrapped.
func (s *Store) Delegation(ctx context.Context, taskID string) (Delegation, error) {
	if err := s.live(); err != nil {
		return Delegation{}, err
	}
	var d Delegation
	var recorded string
	err := s.read.QueryRowContext(ctx, `
		SELECT task_id, child_home, recovery, recorded_at FROM delegation WHERE task_id = ?`, taskID).
		Scan(&d.TaskID, &d.ChildHome, &d.Recovery, &recorded)
	if err != nil {
		return Delegation{}, fmt.Errorf("store: reading the delegation of %s: %w", taskID, errNoRows(err))
	}
	if d.RecordedAt, err = decodeTime(recorded); err != nil {
		return Delegation{}, err
	}
	return d, nil
}

// RecordDelegationDelivery writes the parent's once-only delivery record for
// a task that has a delegation row. A task that is not delegated, or that
// does not exist, is ErrNotFound. A second call is ErrDelegationDelivered.
func (s *Store) RecordDelegationDelivery(ctx context.Context, taskID string) (DelegationDelivery, error) {
	if err := home.ValidTaskID(taskID); err != nil {
		return DelegationDelivery{}, fmt.Errorf("store: delivery of %q: %w", taskID, err)
	}
	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM delegation WHERE task_id = ?`, taskID).Scan(&one); {
		case isNoRows(err):
			return ErrNotFound
		case err != nil:
			return err
		}
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM delegation_delivery WHERE task_id = ?`, taskID).Scan(&one); {
		case err == nil:
			return ErrDelegationDelivered
		case !isNoRows(err):
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO delegation_delivery (task_id, recorded_at) VALUES (?, ?)`,
			taskID, encodeTime(now))
		return err
	})
	if err != nil {
		if errors.Is(err, ErrDelegationDelivered) || errors.Is(err, ErrNotFound) {
			return DelegationDelivery{}, err
		}
		return DelegationDelivery{}, fmt.Errorf("store: recording delivery of delegated task %s: %w", taskID, err)
	}
	return DelegationDelivery{TaskID: taskID, RecordedAt: now}, nil
}

// DelegationDelivery reads the delivery record. A task with none returns
// ErrNotFound, wrapped.
func (s *Store) DelegationDelivery(ctx context.Context, taskID string) (DelegationDelivery, error) {
	if err := s.live(); err != nil {
		return DelegationDelivery{}, err
	}
	var d DelegationDelivery
	var recorded string
	err := s.read.QueryRowContext(ctx, `
		SELECT task_id, recorded_at FROM delegation_delivery WHERE task_id = ?`, taskID).
		Scan(&d.TaskID, &recorded)
	if err != nil {
		return DelegationDelivery{}, fmt.Errorf("store: reading delivery of delegated task %s: %w", taskID, errNoRows(err))
	}
	if d.RecordedAt, err = decodeTime(recorded); err != nil {
		return DelegationDelivery{}, err
	}
	return d, nil
}

// RecordDelegationAttemptEnd writes the record that the named attempt was
// proven dead. The attempt must exist. A second call for the same attempt
// is ErrDelegationAttemptEnded. This is not written because a connection
// dropped; the caller is whoever established that the attempt is dead.
func (s *Store) RecordDelegationAttemptEnd(ctx context.Context, taskID string, sequence int64) error {
	if sequence < 1 {
		return fmt.Errorf("store: task %s attempt %d is not an attempt", taskID, sequence)
	}
	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var one int
		switch err := tx.QueryRowContext(ctx, `
			SELECT 1 FROM task_attempt WHERE task_id = ? AND sequence = ?`, taskID, sequence).Scan(&one); {
		case isNoRows(err):
			return ErrNotFound
		case err != nil:
			return err
		}
		switch err := tx.QueryRowContext(ctx, `
			SELECT 1 FROM delegation_attempt_end WHERE task_id = ? AND sequence = ?`, taskID, sequence).Scan(&one); {
		case err == nil:
			return ErrDelegationAttemptEnded
		case !isNoRows(err):
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO delegation_attempt_end (task_id, sequence, recorded_at) VALUES (?, ?, ?)`,
			taskID, sequence, encodeTime(now))
		return err
	})
	if err != nil {
		if errors.Is(err, ErrDelegationAttemptEnded) || errors.Is(err, ErrNotFound) {
			return err
		}
		return fmt.Errorf("store: recording the end of task %s attempt %d: %w", taskID, sequence, err)
	}
	return nil
}

// DelegationAttemptDead reports whether the named attempt has an end record.
// A missing attempt is false and a nil error: absence is not death, and a
// caller that has not looked up the attempt learns nothing more here. An
// end record is the only true answer.
func (s *Store) DelegationAttemptDead(ctx context.Context, taskID string, sequence int64) (bool, error) {
	if err := s.live(); err != nil {
		return false, err
	}
	var one int
	switch err := s.read.QueryRowContext(ctx, `
		SELECT 1 FROM delegation_attempt_end WHERE task_id = ? AND sequence = ?`, taskID, sequence).Scan(&one); {
	case isNoRows(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("store: reading the end of task %s attempt %d: %w", taskID, sequence, err)
	}
	return true, nil
}

func delegationGrantOf(ctx context.Context, q querier) (DelegationGrant, error) {
	var g DelegationGrant
	var tasks, branches, recorded string
	err := q.QueryRowContext(ctx, `
		SELECT parent_home, tasks, parent_branches, parent_pid, parent_uid, parent_gid, recorded_at
		FROM delegation_grant WHERE singleton = 1`).
		Scan(&g.ParentHome, &tasks, &branches, &g.ParentPID, &g.ParentUID, &g.ParentGID, &recorded)
	if err != nil {
		return DelegationGrant{}, err
	}
	g.Tasks = splitCanon(tasks)
	g.ParentBranches = splitCanon(branches)
	if g.RecordedAt, err = decodeTime(recorded); err != nil {
		return DelegationGrant{}, err
	}
	return g, nil
}

func canonTasks(tasks []string) ([]string, string, error) {
	if len(tasks) == 0 {
		return nil, "", fmt.Errorf("store: a subordinate grant names no task")
	}
	out := make([]string, 0, len(tasks))
	seen := make(map[string]bool, len(tasks))
	for _, id := range tasks {
		if err := home.ValidTaskID(id); err != nil {
			return nil, "", fmt.Errorf("store: a subordinate grant's task: %w", err)
		}
		if seen[id] {
			return nil, "", fmt.Errorf("store: task %s is listed twice", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out, strings.Join(out, "\n"), nil
}

func canonPaths(paths []string) ([]string, string, error) {
	if len(paths) == 0 {
		return nil, "", nil
	}
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		cleaned, err := cleanAbsolute(p)
		if err != nil {
			return nil, "", fmt.Errorf("store: a parent branch: %w", err)
		}
		if seen[cleaned] {
			return nil, "", fmt.Errorf("store: %s is listed twice", cleaned)
		}
		seen[cleaned] = true
		out = append(out, cleaned)
	}
	sort.Strings(out)
	return out, strings.Join(out, "\n"), nil
}

func splitCanon(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func cleanAbsolute(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("%q is not an absolute path", path)
	}
	return filepath.Clean(path), nil
}

func pathUnder(path, root string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
