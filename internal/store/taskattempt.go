package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
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
	// It names its task and never the machine: which machine the session is
	// on is the task's Machine, read off the task's row.
	SessionRef Optional[string] `json:"session_ref"`
	// Host is the herdr server hosting the session of a worker on a remote
	// host, its identifier and start time on that host's kernel, recorded
	// with SessionRef and the one record of that server. It is zero for a
	// task on this machine, for an attempt with no session, and for one
	// recorded before the column existed.
	Host SessionHost `json:"host"`
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
	// KnowledgeCommit is the default-branch commit the launch read the
	// repository's curated knowledge document at. It is unknown until
	// SetAttemptKnowledge fills it, once, before the process starts; an
	// attempt that never reached that read keeps it unknown.
	KnowledgeCommit Optional[string] `json:"knowledge_commit"`
	// KnowledgeDigest is the SHA-256 of the document's bytes at
	// KnowledgeCommit. It is unknown when KnowledgeCommit is, and also when
	// that commit's tree held no document, which a known KnowledgeCommit
	// tells apart.
	KnowledgeDigest Optional[string] `json:"knowledge_digest"`
}

const taskAttemptColumns = `SELECT task_id, sequence, started_at, session_ref, adapter, route_reason, route_skipped, role, model, effort, ` +
	`knowledge_commit, knowledge_digest, host_pid, host_start`

func scanTaskAttempt(sc scanner) (TaskAttempt, error) {
	var a TaskAttempt
	var started string
	var skipped Optional[string]
	var hostPID, hostStart Optional[int64]
	if err := sc.Scan(&a.TaskID, &a.Sequence, &started, &a.SessionRef, &a.Adapter, &a.RouteReason, &skipped, &a.Role, &a.Model, &a.Effort,
		&a.KnowledgeCommit, &a.KnowledgeDigest, &hostPID, &hostStart); err != nil {
		return TaskAttempt{}, err
	}
	if pid, ok := hostPID.Get(); ok {
		start, known := hostStart.Get()
		if pid <= 0 || !known || start < 0 {
			return TaskAttempt{}, fmt.Errorf("columns host_pid and host_start: %d and %v name no process", pid, hostStart)
		}
		a.Host = SessionHost{PID: int(pid), Start: uint64(start)}
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

// BeginTaskAttempt records the next attempt of a task after its first,
// anchored to the highest attempt sequence the caller had seen, and returns
// it. The first attempt is the dispatch, which places the task, and is
// BeginFirstTaskAttempt's: an expectedLatest below one is refused with
// ErrFirstAttemptIsDispatch before anything is read. Every attempt this
// records runs on the machine the first one recorded, which it does not
// write.
//
// The anchor is decided in the same transaction as the insert, on the single
// writer connection, so two callers that both saw the same latest attempt
// cannot both begin the next: one is accepted and assigned expectedLatest+1,
// and the other is refused with a *TaskMovedError wrapping ErrTaskMoved that
// names where the attempts actually stand. That is the relaunch claim, and
// the claim of a dispatch retried after an attempt that never launched:
// whichever caller wins is the one the attempt belongs to.
//
// It returns ErrNotFound, wrapped, for a task that does not exist. It records
// no state and launches nothing: the record precedes the process, and the
// session the process runs in is filled in afterwards by SetAttemptSession.
func (s *Store) BeginTaskAttempt(ctx context.Context, taskID string, expectedLatest int64) (TaskAttempt, error) {
	if strings.TrimSpace(taskID) == "" {
		return TaskAttempt{}, fmt.Errorf("store: no task named")
	}
	if expectedLatest < 1 {
		return TaskAttempt{}, fmt.Errorf("store: task %s: %w", taskID, ErrFirstAttemptIsDispatch)
	}
	now := nowUTC()
	sequence := expectedLatest + 1
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := anchorTaskAttemptTx(ctx, tx, taskID, expectedLatest); err != nil {
			return err
		}
		return insertTaskAttemptTx(ctx, tx, taskID, sequence, now)
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

// anchorTaskAttemptTx refuses, with a *TaskMovedError, a task whose highest
// attempt is not expectedLatest, with ErrNotFound one that does not exist,
// and with ErrPassRetired a housekeeping pass whose retirement is recorded,
// which is every attempt's claim: dispatch, a retried dispatch, and the one
// anchored relaunch.
func anchorTaskAttemptTx(ctx context.Context, tx *sql.Tx, taskID string, expectedLatest int64) error {
	if err := taskExists(ctx, tx, taskID); err != nil {
		return err
	}
	if err := refuseRetiredTx(ctx, tx, taskID); err != nil {
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
	return nil
}

// insertTaskAttemptTx writes attempt sequence of the task, with no session.
func insertTaskAttemptTx(ctx context.Context, tx *sql.Tx, taskID string, sequence int64, at time.Time) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO task_attempt (task_id, sequence, started_at, session_ref) VALUES (?, ?, ?, NULL)`,
		taskID, sequence, encodeTime(at))
	return err
}

// SetAttemptSession records the session the attempt's process runs in and,
// for a task on a remote host, the herdr server hosting it, in one write. It
// is one revision an attempt's row takes, and it takes it once: a second call
// on the same attempt is refused with ErrAttemptSessionRecorded, because a
// process that was replaced is a new attempt, and the schema refuses a
// rewrite of either on the same terms. An empty reference is refused, and an
// attempt that does not exist is ErrNotFound, wrapped.
//
// The host goes with the task's machine, read in the same transaction: a
// task on a remote host is refused with ErrRemoteSessionWithoutHost when host
// names no process, and a task on this machine with
// ErrSessionHostOnThisMachine when it names one. The reference itself names
// no machine.
//
// A housekeeping pass whose retirement is recorded is refused with
// ErrPassRetired, wrapped, read in the same transaction. Every attempt claim
// refuses a retired pass, so the one launch that can reach this write after
// the retirement is of an attempt claimed before it, and its retirement was
// written on a reading of that attempt as having no session; this refusal
// and RetireHousekeepingPass's Session anchor give the two writes exactly
// one loser in either order, so no attempt of a retired pass has a session
// (PRD section 6's housekeeping). The caller ends the worker it started.
func (s *Store) SetAttemptSession(ctx context.Context, taskID string, sequence int64, sessionRef string, host SessionHost) error {
	if strings.TrimSpace(sessionRef) == "" {
		return fmt.Errorf("store: task %s attempt %d: the session reference is empty", taskID, sequence)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var recorded, machine Optional[string]
		err := tx.QueryRowContext(ctx,
			`SELECT a.session_ref, t.machine FROM task_attempt a JOIN task t ON t.id = a.task_id
			WHERE a.task_id = ? AND a.sequence = ?`, taskID, sequence).
			Scan(&recorded, &machine)
		if err != nil {
			return fmt.Errorf("store: task %s attempt %d: %w", taskID, sequence, errNoRows(err))
		}
		if recorded.IsKnown() {
			return fmt.Errorf("store: task %s attempt %d: %w", taskID, sequence, ErrAttemptSessionRecorded)
		}
		if err := refuseRetiredTx(ctx, tx, taskID); err != nil {
			return fmt.Errorf("store: task %s attempt %d: %w", taskID, sequence, err)
		}
		var hostPID, hostStart any
		switch name, remote := machine.Get(); {
		case remote && !host.Known():
			return fmt.Errorf("store: task %s attempt %d on %s: %w", taskID, sequence, name, ErrRemoteSessionWithoutHost)
		case !remote && host.Known():
			return fmt.Errorf("store: task %s attempt %d: %w", taskID, sequence, ErrSessionHostOnThisMachine)
		case remote:
			if host.Start > math.MaxInt64 {
				return fmt.Errorf("store: task %s attempt %d: the session host's start time %d does not fit the record", taskID, sequence, host.Start)
			}
			hostPID, hostStart = host.PID, int64(host.Start)
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE task_attempt SET session_ref = ?, host_pid = ?, host_start = ? WHERE task_id = ? AND sequence = ?`,
			sessionRef, hostPID, hostStart, taskID, sequence)
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

// AttemptKnowledge is the revision of a repository's curated knowledge
// document a launch read: the commit it was read at, and the digest of the
// document's bytes there, empty when that commit's tree held no document.
type AttemptKnowledge struct {
	Commit string
	Digest string
}

// SetAttemptKnowledge records the knowledge revision the attempt's worker is
// launched with. It is filled once, before the process starts: a second call
// is refused with ErrAttemptKnowledgeRecorded, and the database refuses a
// rewrite on the same terms. An empty commit is refused, because a launch
// that read nothing has no revision to record, and an attempt that does not
// exist is ErrNotFound, wrapped. The document's text is not stored here.
func (s *Store) SetAttemptKnowledge(ctx context.Context, taskID string, sequence int64, read AttemptKnowledge) error {
	if strings.TrimSpace(read.Commit) == "" {
		return fmt.Errorf("store: task %s attempt %d: the knowledge commit is empty", taskID, sequence)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var recorded Optional[string]
		err := tx.QueryRowContext(ctx,
			`SELECT knowledge_commit FROM task_attempt WHERE task_id = ? AND sequence = ?`, taskID, sequence).
			Scan(&recorded)
		if err != nil {
			return fmt.Errorf("store: task %s attempt %d: %w", taskID, sequence, errNoRows(err))
		}
		if recorded.IsKnown() {
			return fmt.Errorf("store: task %s attempt %d: %w", taskID, sequence, ErrAttemptKnowledgeRecorded)
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE task_attempt SET knowledge_commit = ?, knowledge_digest = ? WHERE task_id = ? AND sequence = ?`,
			read.Commit, namedOrNull(read.Digest), taskID, sequence)
		if err != nil {
			return fmt.Errorf("store: task %s attempt %d: recording its knowledge revision: %w", taskID, sequence, err)
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
