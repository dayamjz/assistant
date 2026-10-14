package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// SweepOutcome is what the latest sweep of a finished run's isolated copy came
// to, as internal/service reads agents.Sweeper's answer. The set is closed.
type SweepOutcome string

const (
	// SweepCleared is a sweep that listed the processes and cleared the copy:
	// everything it found there was ended or had already gone, and nothing it
	// could not read could be standing in it.
	SweepCleared SweepOutcome = "cleared"
	// SweepNothingStanding is no copy standing at the run's copy path, so
	// there was nothing to sweep.
	SweepNothingStanding SweepOutcome = "nothing_standing"
	// SweepUnsupported is a platform with no process sweep: nothing was
	// looked for, and a process the run left may still be running there.
	SweepUnsupported SweepOutcome = "unsupported"
	// SweepFailed is a sweep that could not be made: the listing failed, or
	// the path stands as something that is not the copy.
	SweepFailed SweepOutcome = "failed"
	// SweepNotCleared is a sweep that listed the processes and left the copy
	// uncleared: some survived, could not be verified, or could not be read
	// and could be standing in it. Remaining counts them.
	SweepNotCleared SweepOutcome = "not_cleared"
)

var sweepOutcomes = []SweepOutcome{SweepCleared, SweepNothingStanding, SweepUnsupported, SweepFailed, SweepNotCleared}

// SweepOutcomes returns the closed set, in the order above. The result is a
// copy, so a caller cannot add to the set by writing to it.
func SweepOutcomes() []SweepOutcome { return slices.Clone(sweepOutcomes) }

// Recognized reports whether o is one of the outcomes this package defines.
func (o SweepOutcome) Recognized() bool { return slices.Contains(sweepOutcomes, o) }

// Listed reports whether an outcome is one a sweep reaches only by listing
// the processes, which is exactly when the record's counts are known.
func (o SweepOutcome) Listed() bool { return o == SweepCleared || o == SweepNotCleared }

// SweepOccasion is which path asked the sweep. The set is closed.
type SweepOccasion string

const (
	// SweptAtFinish is the sweep the move that finished the run asks, from
	// inside that move.
	SweptAtFinish SweepOccasion = "at_finish"
	// SweptAtRecovery is the sweep a service's recovery asks of a finished
	// run's copy that still stands when the service opens.
	SweptAtRecovery SweepOccasion = "at_recovery"
)

var sweepOccasions = []SweepOccasion{SweptAtFinish, SweptAtRecovery}

// SweepOccasions returns the closed set, in the order above, as a copy.
func SweepOccasions() []SweepOccasion { return slices.Clone(sweepOccasions) }

// Recognized reports whether o is one of the occasions this package defines.
func (o SweepOccasion) Recognized() bool { return slices.Contains(sweepOccasions, o) }

// ErrUnknownSweep is what RecordRunCopySweep refuses, and what a read returns,
// for a sweep record whose outcome or occasion is outside its closed set, or
// whose counts do not match its outcome: a row outside the set was not
// written by this package's accessors, and reading it back as some outcome it
// is not would turn a row nobody can account for into a fact about what a
// sweep found.
var ErrUnknownSweep = errors.New("store: run copy sweep record is outside the closed set")

// RunCopySweep is the record of the latest sweep of a finished run's isolated
// copy: what it came to, how many processes it ended and how many kept it
// from clearing, which path asked it, and when it was recorded. It is keyed by
// the run, one row per run rewritten in place, so the row is the latest sweep
// and no reader derives it from a history of sweeps (P8).
//
// It records what a sweep found at the moment it ran and never whether
// anything runs in the copy now. A cleared sweep says nothing about a process
// started there afterwards, and an uncleared one says nothing about whether
// the survivor has since exited; whether a process is there is answered by
// looking for it, never by reading this table. Nothing gates on it: the copy's
// reclaim is internal/tasks' P12 decision and does not read it.
type RunCopySweep struct {
	// Run names the run.
	Run string `json:"run"`
	// Outcome is what the sweep came to.
	Outcome SweepOutcome `json:"outcome"`
	// Ended is how many processes the sweep ended, known exactly when the
	// outcome is one a listing reaches.
	Ended Optional[int] `json:"ended"`
	// Remaining is how many processes kept the sweep from clearing the copy:
	// survivors, unverified processes, and unreadable ones that could be
	// standing in it. It is known exactly when Ended is, zero for a cleared
	// sweep and the survivor count for one that did not clear.
	Remaining Optional[int] `json:"remaining"`
	// Occasion is which path asked the sweep.
	Occasion SweepOccasion `json:"occasion"`
	// Detail is the sweep's own account, as evidence: its result as
	// agents.SweepResult.String renders it, or the error it answered. Nothing
	// here reads it.
	Detail string `json:"detail"`
	// SweptAt is when the record was written, which is just after the sweep
	// returned.
	SweptAt time.Time `json:"swept_at"`
}

// valid answers ErrUnknownSweep, wrapped, for a record outside the closed
// sets or whose counts do not match its outcome.
func (w RunCopySweep) valid() error {
	if !w.Outcome.Recognized() {
		return fmt.Errorf("%w: outcome %q", ErrUnknownSweep, w.Outcome)
	}
	if !w.Occasion.Recognized() {
		return fmt.Errorf("%w: occasion %q", ErrUnknownSweep, w.Occasion)
	}
	ended, endedKnown := w.Ended.Get()
	remaining, remainingKnown := w.Remaining.Get()
	switch {
	case w.Outcome.Listed() != endedKnown || endedKnown != remainingKnown:
		return fmt.Errorf("%w: a %s sweep with ended %v and remaining %v", ErrUnknownSweep, w.Outcome, w.Ended, w.Remaining)
	case ended < 0 || remaining < 0:
		return fmt.Errorf("%w: a negative count", ErrUnknownSweep)
	case w.Outcome == SweepCleared && remaining != 0:
		return fmt.Errorf("%w: a cleared sweep with %d remaining", ErrUnknownSweep, remaining)
	}
	return nil
}

// RecordRunCopySweep writes the record of the latest sweep of a run's copy,
// replacing whatever sweep the run's row recorded before, and returns it as
// stored with SweptAt assigned here. It refuses an empty run, a record
// outside the closed sets or whose counts do not match its outcome with
// ErrUnknownSweep, and a run that does not exist with ErrNotFound, wrapped.
// Two sweeps of one run recorded concurrently leave the one written last.
func (s *Store) RecordRunCopySweep(ctx context.Context, w RunCopySweep) (RunCopySweep, error) {
	if strings.TrimSpace(w.Run) == "" {
		return RunCopySweep{}, fmt.Errorf("store: a run copy sweep record names no run")
	}
	if err := w.valid(); err != nil {
		return RunCopySweep{}, fmt.Errorf("store: run %s: %w", w.Run, err)
	}
	now := encodeTime(nowUTC())
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := runExists(ctx, tx, w.Run); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO run_copy_sweep (run, outcome, ended, remaining, occasion, detail, swept_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (run) DO UPDATE SET outcome = excluded.outcome, ended = excluded.ended,
				remaining = excluded.remaining, occasion = excluded.occasion, detail = excluded.detail,
				swept_at = excluded.swept_at`,
			w.Run, string(w.Outcome), optionalInt64(w.Ended), optionalInt64(w.Remaining), string(w.Occasion), w.Detail, now)
		return err
	})
	if err != nil {
		return RunCopySweep{}, fmt.Errorf("store: recording the sweep of run %s's copy: %w", w.Run, err)
	}
	return s.RunCopySweep(ctx, w.Run)
}

// RunCopySweep returns the record of the latest sweep of a run's copy, or
// ErrNotFound, wrapped, when none has been written, which is the ordinary
// state of a run that has not finished. A row outside the closed sets is
// ErrUnknownSweep, wrapped.
func (s *Store) RunCopySweep(ctx context.Context, run string) (RunCopySweep, error) {
	if err := s.live(); err != nil {
		return RunCopySweep{}, err
	}
	var w RunCopySweep
	var outcome, occasion, swept string
	var ended, remaining Optional[int64]
	err := s.read.QueryRowContext(ctx, `SELECT run, outcome, ended, remaining, occasion, detail, swept_at
		FROM run_copy_sweep WHERE run = ?`, run).Scan(&w.Run, &outcome, &ended, &remaining, &occasion, &w.Detail, &swept)
	if err != nil {
		return RunCopySweep{}, fmt.Errorf("store: reading the sweep record of run %s: %w", run, errNoRows(err))
	}
	w.Outcome, w.Occasion = SweepOutcome(outcome), SweepOccasion(occasion)
	w.Ended, w.Remaining = optionalIntValue(ended), optionalIntValue(remaining)
	if err := w.valid(); err != nil {
		return RunCopySweep{}, fmt.Errorf("store: reading the sweep record of run %s: %w", run, err)
	}
	if w.SweptAt, err = decodeTime(swept); err != nil {
		return RunCopySweep{}, fmt.Errorf("store: reading the sweep record of run %s: %w", run, err)
	}
	return w, nil
}
