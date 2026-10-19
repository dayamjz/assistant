package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TaskAttempt is one attempt to run a task: its first launch, or a relaunch
// after it. It is history, appended and never revised, with one exception
// stated on SessionRef.
//
// The attempt is the boundary a run is attributed across. A run of the task's
// branch created at or after the latest attempt's StartedAt is the task's
// only when no other task of that branch has an attempt that started later
// and still at or before the run. A run from before the cutoff belongs to
// whatever ran the branch earlier. A task with no attempt is attributed no
// run by its branch at all, only the run it names directly.
type TaskAttempt struct {
	// TaskID names the task.
	TaskID string `json:"task_id"`
	// Sequence is the attempt's position in the task's attempts, counted
	// from one, with no gaps.
	Sequence int64 `json:"sequence"`
	// StartedAt is when the attempt was recorded, which precedes the process
	// it is about: a record is written before anything is launched against
	// it.
	StartedAt time.Time `json:"started_at"`
	// SessionRef is the terminal session the attempt's process runs in. It
	// is unknown until SetAttemptSession fills it, which happens once, after
	// the process starts; an attempt whose process never started keeps it
	// unknown, and that is the record of a relaunch that did not come up.
	SessionRef Optional[string] `json:"session_ref"`
	// Adapter is the coding-agent name agents.Route selected for this
	// attempt's worker. It is unknown until SetAttemptRoute fills it, which
	// happens once, before the process starts; an attempt that never reached
	// routing keeps it unknown.
	Adapter Optional[string] `json:"adapter"`
	// RouteReason is the stable reason code agents.Route returned with the
	// selection (override or ordered). It is filled with Adapter by
	// SetAttemptRoute and unknown on the same terms.
	RouteReason Optional[string] `json:"route_reason"`
	// RouteSkipped is every entry routing passed over before it chose
	// Adapter, in the order it considered them, with the stable reason for
	// each. It is filled with Adapter by SetAttemptRoute, so a known empty
	// list is a route whose first candidate covered the need, and it is
	// unknown on Adapter's terms and for an attempt routed before the
	// column existed.
	RouteSkipped Optional[[]SkippedAgent] `json:"route_skipped"`
	// Role is the role name the launch resolved. It is unknown when the task
	// named no role. It is filled with the route.
	Role Optional[string] `json:"role"`
	// Model is the model identifier the adapter was asked to start. It is
	// unknown when the role named no model. It is filled with the route.
	Model Optional[string] `json:"model"`
	// Effort is the effort the role named. It is unknown when the role named
	// none. It is filled with the route.
	Effort Optional[string] `json:"effort"`
}

const taskAttemptColumns = `SELECT task_id, sequence, started_at, session_ref, adapter, route_reason, route_skipped, role, model, effort`

func scanTaskAttempt(sc scanner) (TaskAttempt, error) {
	var a TaskAttempt
	var started string
	var skipped Optional[string]
	if err := sc.Scan(&a.TaskID, &a.Sequence, &started, &a.SessionRef, &a.Adapter, &a.RouteReason, &skipped, &a.Role, &a.Model, &a.Effort); err != nil {
		return TaskAttempt{}, err
	}
	var err error
	if a.RouteSkipped, err = skippedValue("route_skipped", skipped); err != nil {
		return TaskAttempt{}, err
	}
	if a.StartedAt, err = decodeTime(started); err != nil {
		return TaskAttempt{}, err
	}
	return a, nil
}

// BeginTaskAttempt records the next attempt of a task, anchored to the
// highest attempt sequence the caller had seen, and returns it.
//
// expectedLatest is zero for a task the caller saw no attempt of. The anchor
// is decided in the same transaction as the insert, on the single writer
// connection, so two callers that both saw the same latest attempt cannot
// both begin the next: one is accepted and assigned expectedLatest+1, and the
// other is refused with a *TaskMovedError wrapping ErrTaskMoved that names
// where the attempts actually stand. That is the dispatch claim and the
// relaunch claim: whichever caller wins is the one the attempt belongs to.
//
// It returns ErrNotFound, wrapped, for a task that does not exist. It records
// no state and launches nothing: the record precedes the process, and the
// session the process runs in is filled in afterwards by SetAttemptSession.
func (s *Store) BeginTaskAttempt(ctx context.Context, taskID string, expectedLatest int64) (TaskAttempt, error) {
	if strings.TrimSpace(taskID) == "" {
		return TaskAttempt{}, fmt.Errorf("store: no task named")
	}
	now := nowUTC()
	var sequence int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := taskExists(ctx, tx, taskID); err != nil {
			return err
		}
		var stands int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sequence), 0) FROM task_attempt WHERE task_id = ?`, taskID).
			Scan(&stands); err != nil {
			return err
		}
		if stands != expectedLatest {
			return &TaskMovedError{Task: taskID, Anchor: AnchoredAttempt, Expected: expectedLatest, Stands: stands}
		}
		sequence = stands + 1
		_, err := tx.ExecContext(ctx,
			`INSERT INTO task_attempt (task_id, sequence, started_at, session_ref) VALUES (?, ?, ?, NULL)`,
			taskID, sequence, encodeTime(now))
		return err
	})
	if err != nil {
		var moved *TaskMovedError
		if errors.As(err, &moved) {
			return TaskAttempt{}, err
		}
		return TaskAttempt{}, fmt.Errorf("store: beginning an attempt of task %s: %w", taskID, err)
	}
	return TaskAttempt{TaskID: taskID, Sequence: sequence, StartedAt: now}, nil
}

// SetAttemptSession records the session the attempt's process runs in. It is
// one revision an attempt's row takes, and it takes it once: a second call on
// the same attempt is refused with ErrAttemptSessionRecorded, because a
// process that was replaced is a new attempt. An empty reference is refused,
// and an attempt that does not exist is ErrNotFound, wrapped.
func (s *Store) SetAttemptSession(ctx context.Context, taskID string, sequence int64, sessionRef string) error {
	if strings.TrimSpace(sessionRef) == "" {
		return fmt.Errorf("store: task %s attempt %d: the session reference is empty", taskID, sequence)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var recorded Optional[string]
		err := tx.QueryRowContext(ctx,
			`SELECT session_ref FROM task_attempt WHERE task_id = ? AND sequence = ?`, taskID, sequence).
			Scan(&recorded)
		if err != nil {
			return fmt.Errorf("store: task %s attempt %d: %w", taskID, sequence, errNoRows(err))
		}
		if recorded.IsKnown() {
			return fmt.Errorf("store: task %s attempt %d: %w", taskID, sequence, ErrAttemptSessionRecorded)
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE task_attempt SET session_ref = ? WHERE task_id = ? AND sequence = ?`,
			sessionRef, taskID, sequence)
		if err != nil {
			return fmt.Errorf("store: task %s attempt %d: recording its session: %w", taskID, sequence, err)
		}
		return nil
	})
}

// AttemptLaunch is what the launch asked for beyond the adapter, and what
// routing passed over to reach it. An empty Role, Model, or Effort was not
// named and is stored unknown, never as one of the named values. Skipped is
// always known once a route is recorded: a nil list is stored as the empty
// one, because a route that passed over nothing is a fact.
type AttemptLaunch struct {
	Role    string
	Model   string
	Effort  string
	Skipped []SkippedAgent
}

// SetAttemptRoute records the adapter agents.Route selected for the attempt,
// the reason code it returned, the entries it passed over before it, and the
// role, model identifier, and effort the launch asked for. It is filled once,
// before the process starts: a second call is refused with
// ErrAttemptRouteRecorded, and a trigger holds the same rule against a direct
// rewrite. An empty adapter or reason, and a skipped entry naming no reason,
// are refused, and an attempt that does not exist is ErrNotFound, wrapped.
func (s *Store) SetAttemptRoute(ctx context.Context, taskID string, sequence int64, adapter, reason string, asked AttemptLaunch) error {
	if strings.TrimSpace(adapter) == "" {
		return fmt.Errorf("store: task %s attempt %d: the routed adapter is empty", taskID, sequence)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("store: task %s attempt %d: the route reason is empty", taskID, sequence)
	}
	skipped, err := skippedText("route_skipped", asked.Skipped)
	if err != nil {
		return fmt.Errorf("store: task %s attempt %d: %w", taskID, sequence, err)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var recordedAdapter, recordedReason Optional[string]
		err := tx.QueryRowContext(ctx,
			`SELECT adapter, route_reason FROM task_attempt WHERE task_id = ? AND sequence = ?`, taskID, sequence).
			Scan(&recordedAdapter, &recordedReason)
		if err != nil {
			return fmt.Errorf("store: task %s attempt %d: %w", taskID, sequence, errNoRows(err))
		}
		if recordedAdapter.IsKnown() || recordedReason.IsKnown() {
			return fmt.Errorf("store: task %s attempt %d: %w", taskID, sequence, ErrAttemptRouteRecorded)
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE task_attempt SET adapter = ?, route_reason = ?, route_skipped = ?, role = ?, model = ?, effort = ? WHERE task_id = ? AND sequence = ?`,
			adapter, reason, skipped, namedOrNull(asked.Role), namedOrNull(asked.Model), namedOrNull(asked.Effort), taskID, sequence)
		if err != nil {
			return fmt.Errorf("store: task %s attempt %d: recording its route: %w", taskID, sequence, err)
		}
		return nil
	})
}

func namedOrNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TaskAttempts returns a task's attempts in sequence order, and nothing for a
// task that has none or does not exist. The whole list comes back: which
// attempt is the latest is a question TaskSnapshot answers alongside the runs
// it decides the attribution of, and a caller anchoring BeginTaskAttempt
// passes the highest sequence it read here.
func (s *Store) TaskAttempts(ctx context.Context, taskID string) ([]TaskAttempt, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	out, err := taskAttemptsOf(ctx, s.read, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: reading the attempts of task %s: %w", taskID, err)
	}
	return out, nil
}

func taskAttemptsOf(ctx context.Context, q querier, taskID string) ([]TaskAttempt, error) {
	rows, err := q.QueryContext(ctx, taskAttemptColumns+` FROM task_attempt WHERE task_id = ? ORDER BY sequence`, taskID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []TaskAttempt
	for rows.Next() {
		a, err := scanTaskAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
