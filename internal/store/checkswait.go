package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ChecksWait is when one run's checks stage began waiting on checks that had
// not settled, and the base commit that wait was armed against. It is
// authoritative for that one fact: one row per run, rewritten in place.
//
// It is what PRD section 10's checks_timeout is measured from. The timeout
// itself is configuration and is not stored here, so a row says when a wait
// began and never whether it has elapsed; the checks stage decides that from
// the row, the configured timeout and its own clock.
//
// A row is removed only by EndChecksWait, which the checks stage calls when
// the checks settle and when the wait elapses. A run whose wait ended any
// other way - its read failed, or it was cancelled or superseded mid-wait -
// keeps its row, and nothing removes the rows of runs that have finished. For
// a failed read that is the point: the run resumes into the same wait. For a
// run that will never resume it is a leak of one small row per run, read by
// nothing, because a row is keyed by its run and only that run's checks stage
// reads it.
type ChecksWait struct {
	// Run is the run the wait belongs to.
	Run string
	// Base is the commit the run's base branch stood at on the upstream when
	// the wait was last armed.
	Base string
	// ArmedAt is when the wait was last armed: when it began, or when the base
	// branch was last seen to have moved since.
	ArmedAt time.Time
}

// ArmChecksWait returns the run's wait as it stands after an observation of
// the base branch at base, made at the instant at.
//
// A run with no wait recorded begins one, armed at at against base. A run
// whose recorded wait names base already keeps it unchanged, so reading the
// same base again does not move ArmedAt. A run whose recorded wait names
// another base is re-armed: base and ArmedAt are both replaced. That is PRD
// section 10's re-arming rule, and it is decided here, inside the one
// transaction that reads and writes the row, so two callers arming one run
// cannot interleave a read with the other's write.
//
// Any difference in base re-arms, not only a commit that descends from the
// recorded one. This package compares text and resolves nothing, so a base
// branch that was force-moved backwards re-arms the wait as an advance would.
//
// It refuses an empty run or base, because a wait keyed by nothing, or armed
// against a base nobody read, would make every later comparison meaningless.
func (s *Store) ArmChecksWait(ctx context.Context, run, base string, at time.Time) (ChecksWait, error) {
	if strings.TrimSpace(run) == "" {
		return ChecksWait{}, fmt.Errorf("store: a checks wait names no run")
	}
	if strings.TrimSpace(base) == "" {
		return ChecksWait{}, fmt.Errorf("store: the checks wait of run %s names no base commit", run)
	}
	var out ChecksWait
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		recorded, err := scanChecksWait(tx.QueryRowContext(ctx, checksWaitColumns+` FROM checks_wait WHERE run = ?`, run))
		switch {
		case err == nil && recorded.Base == base:
			out = recorded
			return nil
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return err
		}
		out = ChecksWait{Run: run, Base: base, ArmedAt: at.UTC()}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO checks_wait (run, base, armed_at) VALUES (?, ?, ?)
			ON CONFLICT(run) DO UPDATE SET
				base     = excluded.base,
				armed_at = excluded.armed_at`,
			run, base, encodeTime(out.ArmedAt))
		return err
	})
	if err != nil {
		return ChecksWait{}, fmt.Errorf("store: arming the checks wait of run %s: %w", run, err)
	}
	return out, nil
}

// EndChecksWait removes the run's wait, so the next wait that run's checks
// stage begins is armed afresh. A run with no wait recorded is not an error:
// ending a wait that never began leaves the record exactly as a caller asking
// for this wants it.
func (s *Store) EndChecksWait(ctx context.Context, run string) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM checks_wait WHERE run = ?`, run)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: ending the checks wait of run %s: %w", run, err)
	}
	return nil
}

const checksWaitColumns = `SELECT run, base, armed_at`

func scanChecksWait(sc scanner) (ChecksWait, error) {
	var w ChecksWait
	var armed string
	if err := sc.Scan(&w.Run, &w.Base, &armed); err != nil {
		return ChecksWait{}, err
	}
	var err error
	if w.ArmedAt, err = decodeTime(armed); err != nil {
		return ChecksWait{}, err
	}
	return w, nil
}
