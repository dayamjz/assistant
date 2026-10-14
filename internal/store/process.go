package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrProcessRecorded is RecordProcess's refusal of a second record for a
// run and process group the table already holds: the first record stands,
// because replacing it would forget a launch whose end nobody recorded.
var ErrProcessRecorded = errors.New("store: the run already has a process record for this group")

// ProcessRecord is PRD section 11's record of an agent process a run's
// validation stage started: the group the agent leads, when it was started,
// and the platform's PID-reuse identity of its leader. internal/service
// writes one where it registers the group with its containment registry
// and clears it when the invocation's tree has been terminated; what is
// left after a service died is what its recovery reaps.
//
// It records what was started and never whether anything runs now (P8). A
// row says an agent was launched and its end was not recorded; whether the
// process is still there is answered by internal/agents' presence read over
// Identity, never by this table, and no accessor here reads it as liveness.
type ProcessRecord struct {
	// RunID names the run the invocation advanced.
	RunID string `json:"run_id"`
	// Stage is the invocation's purpose, which is what the containment
	// registry records as the stage: the same word for every stage that
	// launches an agent for that purpose.
	Stage string `json:"stage"`
	// Group is the process group identifier, which is the leader's process
	// identifier where internal/agents starts the agent as a group leader.
	Group int `json:"group"`
	// StartedAt is when the record was written, which is when the process
	// had started.
	StartedAt time.Time `json:"started_at"`
	// Identity is the platform's start-time reading of the leader as
	// agents.Identity.Start, carried here so a recovering service can tell
	// the process from a later one given the same identifier. It is unknown
	// where the platform had no read or the process was gone before it was
	// looked at, and a record with none cannot be verified.
	Identity Optional[uint64] `json:"identity"`
}

// RecordProcess writes the record of one started process and returns it as
// stored. It refuses an empty run, a stage with no text, a group that is not
// a positive identifier, a run that does not exist with ErrNotFound,
// wrapped, and a run and group already recorded with ErrProcessRecorded,
// under which the first record stands. StartedAt is assigned here.
func (s *Store) RecordProcess(ctx context.Context, r ProcessRecord) (ProcessRecord, error) {
	switch {
	case strings.TrimSpace(r.RunID) == "":
		return ProcessRecord{}, fmt.Errorf("store: a process record names no run")
	case strings.TrimSpace(r.Stage) == "":
		return ProcessRecord{}, fmt.Errorf("store: a process record for run %s names no stage", r.RunID)
	case r.Group <= 0:
		return ProcessRecord{}, fmt.Errorf("store: a process record for run %s names group %d, which is not a process group", r.RunID, r.Group)
	}
	var identity Optional[string]
	if start, ok := r.Identity.Get(); ok {
		identity = Known(strconv.FormatUint(start, 10))
	}
	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM run WHERE id = ?`, r.RunID).Scan(&one); err != nil {
			return errNoRows(err)
		}
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM run_process WHERE run_id = ? AND group_id = ?`,
			r.RunID, r.Group).Scan(&one); {
		case err == nil:
			return ErrProcessRecorded
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO run_process (run_id, stage, group_id, started_at, identity)
			VALUES (?, ?, ?, ?, ?)`,
			r.RunID, r.Stage, r.Group, encodeTime(now), identity)
		return err
	})
	if err != nil {
		return ProcessRecord{}, fmt.Errorf("store: recording process group %d of run %s: %w", r.Group, r.RunID, err)
	}
	return s.processRecord(ctx, r.RunID, r.Group)
}

// ClearProcess removes the record of one started process, which is what a
// caller does once the invocation's tree has been terminated and reaped, or
// once recovery has ended the group or established that the identifier no
// longer names it. It refuses a run and group with no record with
// ErrNotFound, wrapped, so a caller that cleared nothing is told so rather
// than reading a clear of nothing as a clear.
func (s *Store) ClearProcess(ctx context.Context, runID string, group int) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM run_process WHERE run_id = ? AND group_id = ?`, runID, group)
		if err != nil {
			return fmt.Errorf("store: clearing process group %d of run %s: %w", group, runID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: clearing process group %d of run %s: %w", group, runID, err)
		}
		if n == 0 {
			return fmt.Errorf("store: clearing process group %d of run %s: %w", group, runID, ErrNotFound)
		}
		return nil
	})
}

// ProcessRecords returns every process record this home holds, oldest
// first and then by run and group, which is what a recovering service walks.
// An empty list is the ordinary state of a home whose last service ended
// every invocation it started.
func (s *Store) ProcessRecords(ctx context.Context) ([]ProcessRecord, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, processColumns+` FROM run_process ORDER BY started_at, run_id, group_id`)
	if err != nil {
		return nil, fmt.Errorf("store: listing process records: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ProcessRecord
	for rows.Next() {
		r, err := scanProcess(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing process records: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing process records: %w", err)
	}
	return out, nil
}

const processColumns = `SELECT run_id, stage, group_id, started_at, identity`

// processRecord reads one record back, with ErrNotFound wrapped when there
// is none.
func (s *Store) processRecord(ctx context.Context, runID string, group int) (ProcessRecord, error) {
	r, err := scanProcess(s.read.QueryRowContext(ctx, processColumns+` FROM run_process WHERE run_id = ? AND group_id = ?`, runID, group))
	if err != nil {
		return ProcessRecord{}, fmt.Errorf("store: reading process group %d of run %s: %w", group, runID, errNoRows(err))
	}
	return r, nil
}

func scanProcess(row scanner) (ProcessRecord, error) {
	var r ProcessRecord
	var started string
	var identity Optional[string]
	if err := row.Scan(&r.RunID, &r.Stage, &r.Group, &started, &identity); err != nil {
		return ProcessRecord{}, err
	}
	var err error
	if r.StartedAt, err = decodeTime(started); err != nil {
		return ProcessRecord{}, err
	}
	if text, ok := identity.Get(); ok {
		start, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			return ProcessRecord{}, fmt.Errorf("column identity: %q is not a start time: %w", text, err)
		}
		r.Identity = Known(start)
	}
	return r, nil
}
