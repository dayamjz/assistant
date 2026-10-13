package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// StageExecution is one execution of one stage's body in one run: when it
// started and when it ended. It is history, appended once the body has
// returned and never revised, so a stage that ran several times, as a stage
// taking fix rounds does, has one row per execution.
//
// An execution a hard kill interrupted has no row, because the row is written
// only once both times are known. The record then understates how often a
// stage ran rather than holding an end nobody observed.
type StageExecution struct {
	// Seq is the row's position in the record, assigned on append. A caller
	// never supplies it.
	Seq int64 `json:"seq"`
	// Run is the run the stage executed in.
	Run string `json:"run"`
	// Stage is the stage that executed.
	Stage string `json:"stage"`
	// Started is when the body was called.
	Started time.Time `json:"started"`
	// Ended is when the body returned, whether with a report or an error.
	Ended time.Time `json:"ended"`
}

// Duration is how long the execution took.
func (e StageExecution) Duration() time.Duration { return e.Ended.Sub(e.Started) }

// AppendStageExecution appends one execution and returns it with its
// sequence. It refuses a row naming no run or no stage, a time that is zero,
// and an end before its start, because each would be a record of an execution
// nobody observed.
func (s *Store) AppendStageExecution(ctx context.Context, e StageExecution) (StageExecution, error) {
	if strings.TrimSpace(e.Run) == "" || strings.TrimSpace(e.Stage) == "" {
		return StageExecution{}, fmt.Errorf("store: a stage execution names no run or no stage")
	}
	if e.Started.IsZero() || e.Ended.IsZero() {
		return StageExecution{}, fmt.Errorf("store: stage execution %s/%s has no start or no end", e.Run, e.Stage)
	}
	if e.Ended.Before(e.Started) {
		return StageExecution{}, fmt.Errorf("store: stage execution %s/%s ends before it starts", e.Run, e.Stage)
	}
	e.Started, e.Ended = e.Started.UTC(), e.Ended.UTC()
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO stage_execution (run, stage, started_at, ended_at) VALUES (?, ?, ?, ?)`,
			e.Run, e.Stage, encodeTime(e.Started), encodeTime(e.Ended))
		if err != nil {
			return err
		}
		e.Seq, err = result.LastInsertId()
		return err
	})
	if err != nil {
		return StageExecution{}, fmt.Errorf("store: appending a stage execution: %w", err)
	}
	return e, nil
}

// StageExecutions returns one run's executions in the order they were
// appended, which is the order they ended.
func (s *Store) StageExecutions(ctx context.Context, run string) ([]StageExecution, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT seq, run, stage, started_at, ended_at FROM stage_execution WHERE run = ? ORDER BY seq`, run)
	if err != nil {
		return nil, fmt.Errorf("store: reading the stage executions of run %s: %w", run, err)
	}
	defer func() { _ = rows.Close() }()

	var out []StageExecution
	for rows.Next() {
		var e StageExecution
		var started, ended string
		if err := rows.Scan(&e.Seq, &e.Run, &e.Stage, &started, &ended); err != nil {
			return nil, fmt.Errorf("store: reading the stage executions of run %s: %w", run, err)
		}
		if e.Started, err = decodeTime(started); err != nil {
			return nil, fmt.Errorf("store: stage execution %d: %w", e.Seq, err)
		}
		if e.Ended, err = decodeTime(ended); err != nil {
			return nil, fmt.Errorf("store: stage execution %d: %w", e.Seq, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the stage executions of run %s: %w", run, err)
	}
	return out, nil
}
