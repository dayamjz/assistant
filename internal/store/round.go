package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Round is history: one row per stage execution, appended and never revised.
// PRD section 8 makes it what the pull request narrative is generated from, so
// it keeps the payloads a later reader would otherwise have to reconstruct.
//
// The payload fields are opaque bytes. This package does not parse a finding
// set; internal/findings owns that vocabulary, and a store that also understood
// it would be a second owner of the same contract.
type Round struct {
	// ID is assigned on append and orders the history.
	ID int64
	// RunID names the run.
	RunID string
	// Stage names the stage that executed.
	Stage string
	// Number is which execution of that stage this was, counted from one.
	Number int
	// Findings is the finding set the stage produced, serialized.
	Findings []byte
	// Selected is the subset chosen for fixing, serialized.
	Selected []byte
	// SelectedBy says who chose them, which P3 and P5 both need a reader to be
	// able to tell apart: a person selecting a finding and the pipeline
	// selecting one are different claims about the same change.
	SelectedBy string
	// FixerPayload is exactly what was sent to the fixer. P4 keeps reviewing
	// and fixing in separate memory, so what crossed between them is recorded
	// here rather than being recoverable only from a session.
	FixerPayload []byte
	// Summary is the one-line summary of the round.
	Summary string
	// RecordedAt is when the round was appended.
	RecordedAt time.Time
}

// AppendRound adds one round to a run's history and returns it with its
// assigned identifier. There is no accessor that updates or deletes a round:
// history is append-only, and a correction is a later round rather than an
// edit of an earlier one.
func (s *Store) AppendRound(ctx context.Context, r Round) (Round, error) {
	if strings.TrimSpace(r.RunID) == "" {
		return Round{}, fmt.Errorf("store: round has no run")
	}
	if strings.TrimSpace(r.Stage) == "" {
		return Round{}, fmt.Errorf("store: round of run %s has no stage", r.RunID)
	}
	if r.Number < 1 {
		return Round{}, fmt.Errorf("store: round of run %s has number %d, which is not a round", r.RunID, r.Number)
	}

	now := nowUTC()
	var id int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO round (run_id, stage, number, findings, selected, selected_by, fixer_payload, summary, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.RunID, r.Stage, r.Number, bytesOrEmpty(r.Findings), bytesOrEmpty(r.Selected),
			r.SelectedBy, bytesOrEmpty(r.FixerPayload), r.Summary, encodeTime(now))
		if err != nil {
			return err
		}
		id, err = result.LastInsertId()
		return err
	})
	if err != nil {
		return Round{}, fmt.Errorf("store: appending a round to run %s: %w", r.RunID, err)
	}
	r.ID = id
	r.RecordedAt = now
	r.Findings = bytesOrEmpty(r.Findings)
	r.Selected = bytesOrEmpty(r.Selected)
	r.FixerPayload = bytesOrEmpty(r.FixerPayload)
	return r, nil
}

// Rounds returns a run's rounds in the order they were appended.
func (s *Store) Rounds(ctx context.Context, runID string) ([]Round, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT id, run_id, stage, number, findings, selected, selected_by, fixer_payload, summary, recorded_at
		FROM round WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: listing rounds of run %s: %w", runID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Round
	for rows.Next() {
		var r Round
		var recorded string
		if err := rows.Scan(&r.ID, &r.RunID, &r.Stage, &r.Number, &r.Findings, &r.Selected,
			&r.SelectedBy, &r.FixerPayload, &r.Summary, &recorded); err != nil {
			return nil, fmt.Errorf("store: listing rounds of run %s: %w", runID, err)
		}
		if r.RecordedAt, err = decodeTime(recorded); err != nil {
			return nil, fmt.Errorf("store: listing rounds of run %s: %w", runID, err)
		}
		// A driver may hand back a nil slice for a zero-length blob. The
		// columns are NOT NULL, so there is nothing for a nil to mean here,
		// and a caller should not have to decide what one means.
		r.Findings = bytesOrEmpty(r.Findings)
		r.Selected = bytesOrEmpty(r.Selected)
		r.FixerPayload = bytesOrEmpty(r.FixerPayload)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing rounds of run %s: %w", runID, err)
	}
	return out, nil
}

// bytesOrEmpty keeps a nil payload out of the database. A nil slice would store
// as SQL NULL, which in this package means unknown, and "no findings" is not
// the same fact as "nobody recorded whether there were findings".
func bytesOrEmpty(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}
