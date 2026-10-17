package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
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
//
// It carries no session reference. Which session an attempt of the task had is
// the attempt's record (TaskAttempt.SessionRef), and the session_ref column
// migration 1 gave this table is neither written nor read by this build: a
// migration may not remove a column, so it stays declared and holds NULL for
// every task created here. One record of that fact is P14's rule, and the
// attempt is the one that knows when the session began.
type Task struct {
	// ID is the caller's identifier for the task.
	ID string `json:"id"`
	// Row is the position the store inserted the record at. The store
	// assigns it, it increases with every task created in a store, and it is
	// never reused because nothing here removes a task. It is what a caller
	// anchors a CreateTask against: the highest Row it saw among the tasks of
	// the same repository and branch.
	Row int64 `json:"row"`
	// Shape is which of the two shapes this task is.
	Shape TaskShape `json:"shape"`
	// Project is what the task is for.
	Project string `json:"project"`
	// Mode is how the worker was launched, in the caller's own notation:
	// PRD section 8's launch notation. It is not the delivery posture, which
	// is Delivery.
	Mode string `json:"mode"`
	// Role is the role name intake recorded. Empty means the task names no
	// role, which a launch reads as the worker role, and is also what a task
	// recorded before the column existed holds. It is written once.
	Role string `json:"role,omitempty"`
	// Delivery is the delivery posture the task was dispatched under, PRD
	// section 6, in a field of its own and never revised: this record, not a
	// later read of the delivery key, decides the task's path. It is
	// recorded at intake for a delivery task, and by TransitionTaskShape for
	// a task promoted to delivery, in the write that changes its shape. It
	// is unknown for an investigation task, which has no posture, and for a
	// delivery task recorded before the column existed or promoted before
	// promotion recorded one; internal/service follows an unknown posture as
	// config.DeliveryGated, the full gate. CreateTask refuses a known value
	// outside config.Delivery's closed set and a known value on an
	// investigation task, and the schema refuses any later write of it but
	// promotion's.
	Delivery Optional[config.Delivery] `json:"delivery"`
	// DeliveryIntent is what the delivery is to do, as the coordinator asked
	// for it when it promoted the task, PRD section 5: written by
	// TransitionTaskShape in the write that changes the shape, and never
	// revised. It is unknown for an investigation task, for a task intake
	// recorded as a delivery task, whose intent is in the instructions intake
	// wrote, and for a task promoted before the column existed. CreateTask
	// refuses a known value, and the schema refuses every write of it but
	// promotion's. It is stored as given, on doc.go's terms for every column
	// but the repository URLs, so a reader that carries it further redacts
	// it on the way out.
	DeliveryIntent Optional[string] `json:"delivery_intent"`
	// WorktreePath is the isolated copy the worker was given. PRD principle
	// P11 says isolation is asserted at launch rather than inferred from this
	// field; recording it is what lets a cleanup proof name what it refused to
	// remove.
	WorktreePath string `json:"worktree_path"`
	// RunID names the validation run the task produced, if it has produced
	// one.
	RunID Optional[string] `json:"run_id"`
	// PullRequest names the pull request the task produced, if any.
	PullRequest Optional[string] `json:"pull_request"`
	// RepositoryID names the repository the task works in. It is unknown for
	// a task recorded before the column existed and for an investigation task
	// that names none; an investigation task may name one with no branch.
	RepositoryID Optional[string] `json:"repository_id"`
	// Branch is the branch the task works on, and a task that names one names
	// RepositoryID too. Together they are how a validation run is attributed
	// to the task: a run of that branch created at or after the task's latest
	// attempt began is this task's, and a task with no attempt is attributed
	// no run by its branch. It is set when the task is created and never
	// moves, and it is unknown for an investigation task and for a task
	// recorded before the column existed.
	Branch Optional[string] `json:"branch"`
	// BlockerTask names the task this one was queued behind at intake, when
	// it was: PRD section 6's "overlaps live work". It is unknown for an
	// independent task and for a task recorded before the column existed, so
	// the record understates rather than fabricating independence. It is set
	// at creation and never moves; whether the blocker has cleared is
	// resolved from the blocker's own records, never stored here.
	BlockerTask Optional[string] `json:"blocker_task"`
	// CreatedAt is when the task was recorded.
	CreatedAt time.Time `json:"created_at"`
	// StartSnapshot is what the task's isolated copy held when its latest
	// dispatch proved the copy isolated, and unknown until a dispatch has.
	// SetTaskStartSnapshot writes it; CopySnapshot says what it holds and
	// what it does not claim.
	StartSnapshot Optional[CopySnapshot] `json:"start_snapshot"`
}

// OpeningEventKind is the kind of the event CreateTask appends as a task's
// first entry. The store records the word and decides nothing about it: what a
// kind means is internal/taskstate's table, which has a row for this one.
const OpeningEventKind = "queued"

// DispatchedEventKind is the kind of the event CreateTask appends second, for
// a task created with no blocker: PRD section 6's "Intake --> Dispatched:
// independent". As with OpeningEventKind, the store records the word and
// internal/taskstate's table is what gives it a meaning.
const DispatchedEventKind = "dispatched"

// Disposition is what an intake decided about a task it is creating: whether
// it is queued behind another task, and what the intake had seen when it
// decided that.
type Disposition struct {
	// Blocker is the task this one is queued behind, or unknown for an
	// independent task. A known blocker must be an existing task other than
	// the one being created.
	Blocker Optional[string]
	// Observed is the highest Task.Row the caller saw among the tasks of the
	// same repository and branch, and zero when it saw none. CreateTask is
	// refused with ErrTaskMoved when a newer task of that branch exists,
	// because the decision Blocker records was made without seeing it. A
	// task with no branch has no such set, and for it Observed must be zero.
	Observed int64
}

const taskColumns = `SELECT rowid, id, shape, project, mode, worktree_path, run_id, pull_request,
	repository_id, branch, blocker_task, created_at, start_snapshot, delivery, delivery_intent, role`

func scanTask(sc scanner) (Task, error) {
	var t Task
	var shape, created string
	var snapshot, delivery, role Optional[string]
	if err := sc.Scan(&t.Row, &t.ID, &shape, &t.Project, &t.Mode, &t.WorktreePath, &t.RunID,
		&t.PullRequest, &t.RepositoryID, &t.Branch, &t.BlockerTask, &created, &snapshot, &delivery, &t.DeliveryIntent, &role); err != nil {
		return Task{}, err
	}
	t.Shape = TaskShape(shape)
	if text, ok := role.Get(); ok {
		t.Role = text
	}
	if text, ok := delivery.Get(); ok {
		// A word outside the set was not written by CreateTask, and reading
		// it back as gated or as unknown would turn a row nobody can account
		// for into a posture.
		if d := config.Delivery(text); !d.Recognized() {
			return Task{}, fmt.Errorf("column delivery: %w: %q", ErrUnknownDelivery, text)
		}
		t.Delivery = Known(config.Delivery(text))
	}
	var err error
	if t.CreatedAt, err = decodeTime(created); err != nil {
		return Task{}, err
	}
	if encoded, ok := snapshot.Get(); ok {
		var decoded CopySnapshot
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
			return Task{}, fmt.Errorf("column start_snapshot: %w", err)
		}
		t.StartSnapshot = Known(decoded)
	}
	return t, nil
}

// CreateTask records a new task with the disposition its intake decided, in
// one transaction: the task row, its opening event of kind OpeningEventKind,
// and, for a task with no blocker, a second event of kind
// DispatchedEventKind. A task is therefore never recorded without its
// disposition, and a failure anywhere records nothing.
//
// It records no state: what a task's state is gets resolved from the records
// that own the facts, by internal/taskstate, on every read (P8, P14). The
// events are history, and are what let a task that nothing else has touched
// resolve from the event log rather than from nothing.
//
// Two rules are checked before anything is written. A task naming a branch
// must name its repository, because attribution needs both; a task may name a
// repository alone, which is an investigation task working in one with no
// branch of its own. And the disposition is anchored: d.Observed is the
// highest Row the caller saw among the tasks of the same repository and
// branch, and the creation is refused with a *TaskMovedError wrapping
// ErrTaskMoved when a newer task of that branch exists, decided in the same
// transaction as the insert, so an intake that read the branch and then
// decided cannot be overtaken between the two. A known blocker must exist and
// must not be the task itself.
//
// The identifier is checked by internal/home's ValidTaskID, the one validator
// of a task identifier, because the identifier names the task's directory
// under the home and this record is where it is first written; a refusal
// wraps home.ErrTaskID. A non-zero d.Observed on a task that names no branch
// is a caller's mistake rather than a move, since such a task has no set of
// branch tasks to have observed, and it is refused before anything is read.
func (s *Store) CreateTask(ctx context.Context, t Task, d Disposition) (Task, error) {
	if err := home.ValidTaskID(t.ID); err != nil {
		return Task{}, fmt.Errorf("store: task %q: %w", t.ID, err)
	}
	if !t.Branch.IsKnown() && d.Observed != 0 {
		return Task{}, fmt.Errorf("store: task %s names no branch, so it cannot have observed branch task %d", t.ID, d.Observed)
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
	if t.Branch.IsKnown() && !t.RepositoryID.IsKnown() {
		return Task{}, fmt.Errorf("store: task %s names a branch without a repository", t.ID)
	}
	if blocker, ok := d.Blocker.Get(); ok && blocker == t.ID {
		return Task{}, fmt.Errorf("store: task %s is named as its own blocker", t.ID)
	}
	if t.DeliveryIntent.IsKnown() {
		return Task{}, fmt.Errorf("store: task %s: a delivery intent is recorded by promotion, never at intake", t.ID)
	}
	var delivery Optional[string]
	if posture, ok := t.Delivery.Get(); ok {
		if !posture.Recognized() {
			return Task{}, fmt.Errorf("store: task %s: %w: %q", t.ID, ErrUnknownDelivery, posture)
		}
		if t.Shape != TaskDelivery {
			return Task{}, fmt.Errorf("store: task %s is an investigation task and has no delivery posture to record", t.ID)
		}
		delivery = Known(string(posture))
	}

	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if blocker, ok := d.Blocker.Get(); ok {
			if err := taskExists(ctx, tx, blocker); err != nil {
				return fmt.Errorf("blocker %s: %w", blocker, err)
			}
		}
		var stands int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(rowid), 0) FROM task WHERE repository_id = ? AND branch = ?`,
			t.RepositoryID, t.Branch).Scan(&stands); err != nil {
			return err
		}
		if stands != d.Observed {
			return &TaskMovedError{Task: t.ID, Anchor: AnchoredBranch, Expected: d.Observed, Stands: stands}
		}
		var role any
		if t.Role != "" {
			role = t.Role
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task (id, shape, project, mode, worktree_path, run_id, pull_request,
				repository_id, branch, blocker_task, created_at, delivery, role)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.ID, string(t.Shape), t.Project, t.Mode, t.WorktreePath,
			t.RunID, t.PullRequest, t.RepositoryID, t.Branch, d.Blocker, encodeTime(now), delivery, role); err != nil {
			return err
		}
		kinds := []string{OpeningEventKind}
		if !d.Blocker.IsKnown() {
			kinds = append(kinds, DispatchedEventKind)
		}
		for i, kind := range kinds {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO task_event (task_id, sequence, kind, detail, at) VALUES (?, ?, ?, ?, ?)`,
				t.ID, int64(i+1), kind, "recorded", encodeTime(now)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		var moved *TaskMovedError
		if errors.As(err, &moved) {
			return Task{}, err
		}
		return Task{}, fmt.Errorf("store: creating task %s: %w", t.ID, err)
	}
	return s.Task(ctx, t.ID)
}

// taskExists answers ErrNotFound for a task identifier no row carries.
func taskExists(ctx context.Context, q querier, id string) error {
	var one int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM task WHERE id = ?`, id).Scan(&one); err != nil {
		return errNoRows(err)
	}
	return nil
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
// them with their resolved current state, which is internal/taskstate's to
// answer for each one: this returns the records and says nothing about where any of them
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
