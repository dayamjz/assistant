package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// StageStatus is where one stage of one run stands.
type StageStatus string

// The stage statuses.
const (
	StagePending StageStatus = "pending"
	StageRunning StageStatus = "running"
	StagePassed  StageStatus = "passed"
	StageFailed  StageStatus = "failed"
	StageSkipped StageStatus = "skipped"
)

// StageResult is the authoritative verdict for one stage of one run. There is
// one row per stage, rewritten as the stage progresses; the per-execution
// history is Round.
type StageResult struct {
	// RunID names the run.
	RunID string
	// Stage names the stage. PRD principle P2 fixes the stage order elsewhere;
	// this package records results and does not judge which stages exist.
	Stage string
	// Status is where the stage stands.
	Status StageStatus
	// FindingCount is how many findings the stage produced.
	FindingCount int
	// Duration is how long the stage took. It is unknown until the stage ends,
	// which is a different fact from a stage that took no measurable time.
	Duration Optional[time.Duration]
	// LogPath is the per-stage log, which PRD section 8 makes the authoritative
	// full output.
	LogPath string
	// LastActivity is when the stage last produced output. It is unknown for a
	// stage that has produced none.
	LastActivity Optional[time.Time]
	// EffectiveFixLimit is the fix round limit that actually applied, after
	// configuration merged. It is unknown for a stage where no limit applies.
	EffectiveFixLimit Optional[int]
	// UpdatedAt is when the row last changed.
	UpdatedAt time.Time
}

// UpsertStageResult writes the verdict for one stage of one run and returns it
// as stored. Writing the same run and stage again replaces the row, because the
// current verdict is one fact with one owner; the history of how it got there
// lives in the rounds.
func (s *Store) UpsertStageResult(ctx context.Context, r StageResult) (StageResult, error) {
	if strings.TrimSpace(r.RunID) == "" {
		return StageResult{}, fmt.Errorf("store: stage result has no run")
	}
	if strings.TrimSpace(r.Stage) == "" {
		return StageResult{}, fmt.Errorf("store: stage result of run %s has no stage", r.RunID)
	}
	if r.Status == "" {
		return StageResult{}, fmt.Errorf("store: stage result %s/%s has no status", r.RunID, r.Stage)
	}
	if r.FindingCount < 0 {
		return StageResult{}, fmt.Errorf("store: stage result %s/%s has a negative finding count", r.RunID, r.Stage)
	}

	now := nowUTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO stage_result (
				run_id, stage, status, finding_count, duration_ns, log_path,
				last_activity, effective_fix_limit, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id, stage) DO UPDATE SET
				status              = excluded.status,
				finding_count       = excluded.finding_count,
				duration_ns         = excluded.duration_ns,
				log_path            = excluded.log_path,
				last_activity       = excluded.last_activity,
				effective_fix_limit = excluded.effective_fix_limit,
				updated_at          = excluded.updated_at`,
			r.RunID, r.Stage, string(r.Status), r.FindingCount,
			optionalDurationNanos(r.Duration), r.LogPath,
			optionalTimeText(r.LastActivity), optionalInt64(r.EffectiveFixLimit),
			encodeTime(now))
		return err
	})
	if err != nil {
		return StageResult{}, fmt.Errorf("store: writing stage result %s/%s: %w", r.RunID, r.Stage, err)
	}
	return s.StageResult(ctx, r.RunID, r.Stage)
}

// StageResult returns the verdict for one stage of one run, or ErrNotFound.
func (s *Store) StageResult(ctx context.Context, runID, stage string) (StageResult, error) {
	if err := s.live(); err != nil {
		return StageResult{}, err
	}
	row := s.read.QueryRowContext(ctx, stageColumns+` FROM stage_result WHERE run_id = ? AND stage = ?`, runID, stage)
	r, err := scanStageResult(row)
	if err != nil {
		return StageResult{}, fmt.Errorf("store: reading stage result %s/%s: %w", runID, stage, errNoRows(err))
	}
	return r, nil
}

// StageResults returns every stage verdict of one run, ordered by stage name.
func (s *Store) StageResults(ctx context.Context, runID string) ([]StageResult, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, stageColumns+` FROM stage_result WHERE run_id = ? ORDER BY stage`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: listing stage results of run %s: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []StageResult
	for rows.Next() {
		r, err := scanStageResult(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing stage results of run %s: %w", runID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing stage results of run %s: %w", runID, err)
	}
	return out, nil
}

const stageColumns = `SELECT
	run_id, stage, status, finding_count, duration_ns, log_path,
	last_activity, effective_fix_limit, updated_at`

func scanStageResult(sc scanner) (StageResult, error) {
	var r StageResult
	var status, updated string
	var durationNanos Optional[int64]
	var lastActivity Optional[string]
	var fixLimit Optional[int64]
	if err := sc.Scan(&r.RunID, &r.Stage, &status, &r.FindingCount, &durationNanos,
		&r.LogPath, &lastActivity, &fixLimit, &updated); err != nil {
		return StageResult{}, err
	}
	r.Status = StageStatus(status)
	r.Duration = optionalDurationValue(durationNanos)
	r.EffectiveFixLimit = optionalIntValue(fixLimit)
	var err error
	if r.LastActivity, err = optionalTimeValue(lastActivity, "last_activity"); err != nil {
		return StageResult{}, err
	}
	if r.UpdatedAt, err = decodeTime(updated); err != nil {
		return StageResult{}, err
	}
	return r, nil
}
