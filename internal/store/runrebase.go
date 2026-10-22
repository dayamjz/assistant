package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RunRebase is the run rebase record: one rebase a run's rebase stage made in
// the run's isolated copy, recorded by the stage when it made it, that carried
// the commit the run was submitted. The stage replayed Before, a head it had
// established carries Submitted, onto Onto, the replay applied without a
// conflict, the copy then stood at After, and the stage had established that
// integrating Before into After would change nothing. This package stores
// what the stage says and checks none of it but Submitted.
//
// It is what internal/tasks' cleanup proofs follow from a commit a task's copy
// holds to the commit the gate published for it after rewriting it: the
// program saw the rewrite happen and wrote down what it became, so nothing
// has to match two commits by what they hold. It records what the rebase did
// and decides nothing; whether After reached an upstream is the proof's to
// read fresh.
type RunRebase struct {
	// Run is the run whose stage made the rebase.
	Run string
	// Submitted is the commit the run was submitted, which is the run row's
	// SubmittedHead; RecordRunRebase refuses any other.
	Submitted string
	// Before is the head the rebase replayed.
	Before string
	// Onto is the commit it was replayed onto.
	Onto string
	// After is the head the copy stood at once the replay applied.
	After string
	// RecordedAt is when the row was first written.
	RecordedAt time.Time
}

// ErrRebaseNotOfRun is returned by RecordRunRebase for a record whose
// Submitted is not the commit the run's row says it was submitted: such a row
// would claim a rebase carried work the run was never given.
var ErrRebaseNotOfRun = errors.New("store: the rebase record names a submitted commit the run was not given")

// RecordRunRebase writes one run rebase record and returns it as stored. It
// refuses a record missing any commit, or naming one with white space in it,
// before anything is read; a run that does not exist with ErrNotFound; and a
// Submitted other than the run row's SubmittedHead with ErrRebaseNotOfRun,
// decided in the transaction that writes the row. The same rebase written
// again finds the row already written and returns it unchanged, so a stage
// re-executed after a resume records nothing twice.
func (s *Store) RecordRunRebase(ctx context.Context, r RunRebase) (RunRebase, error) {
	if strings.TrimSpace(r.Run) == "" {
		return RunRebase{}, fmt.Errorf("store: a run rebase record names no run")
	}
	for name, v := range map[string]string{"submitted": r.Submitted, "before": r.Before, "onto": r.Onto, "after": r.After} {
		if v == "" || strings.ContainsAny(v, " \t\r\n") {
			return RunRebase{}, fmt.Errorf("store: run %s: a run rebase record names %q as its %s commit", r.Run, v, name)
		}
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var submitted string
		if err := tx.QueryRowContext(ctx, `SELECT submitted_head FROM run WHERE id = ?`, r.Run).Scan(&submitted); err != nil {
			return errNoRows(err)
		}
		if submitted != r.Submitted {
			return fmt.Errorf("%w: run %s was submitted %s and the record names %s", ErrRebaseNotOfRun, r.Run, submitted, r.Submitted)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO run_rebase (run, submitted, before, onto, after, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (run, before, onto, after) DO NOTHING`,
			r.Run, r.Submitted, r.Before, r.Onto, r.After, encodeTime(nowUTC()))
		return err
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrRebaseNotOfRun) {
			return RunRebase{}, err
		}
		return RunRebase{}, fmt.Errorf("store: recording a rebase of run %s: %w", r.Run, err)
	}
	all, err := s.RunRebases(ctx, r.Run)
	if err != nil {
		return RunRebase{}, err
	}
	for _, got := range all {
		if got.Before == r.Before && got.Onto == r.Onto && got.After == r.After {
			return got, nil
		}
	}
	return RunRebase{}, fmt.Errorf("store: run %s: the rebase record just written does not read back", r.Run)
}

// RunRebases returns every run rebase record of the run, in the order they
// were first written. A run whose stage recorded no rebase has none, which is
// an answer rather than a failure, and so does a run that does not exist.
func (s *Store) RunRebases(ctx context.Context, run string) ([]RunRebase, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT run, submitted, before, onto, after, recorded_at FROM run_rebase WHERE run = ? ORDER BY id`, run)
	if err != nil {
		return nil, fmt.Errorf("store: reading the rebases of run %s: %w", run, err)
	}
	defer func() { _ = rows.Close() }()
	var out []RunRebase
	for rows.Next() {
		var r RunRebase
		var recorded string
		if err := rows.Scan(&r.Run, &r.Submitted, &r.Before, &r.Onto, &r.After, &recorded); err != nil {
			return nil, fmt.Errorf("store: reading the rebases of run %s: %w", run, err)
		}
		if r.RecordedAt, err = decodeTime(recorded); err != nil {
			return nil, fmt.Errorf("store: reading the rebases of run %s: %w", run, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the rebases of run %s: %w", run, err)
	}
	return out, nil
}
