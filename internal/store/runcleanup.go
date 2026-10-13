package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RunCleanup is the cleanup record of a run's isolated copy, on TaskCleanup's
// terms: PRD principle P12 covers every isolated copy, and the copy a run
// works in is one. It is keyed by the run and written once with its proof,
// and RemovedAt is the one revision it takes after that.
//
// It carries one thing TaskCleanup does not, a refusal. A run's copy is
// cleaned up when its run settles, and a copy the decision refused to remove
// is a fact PRD section 9's status has to say in a person's words: that a
// local copy was kept because its work is not yet safely elsewhere. The
// refusal is the decision's own reason, a word this package records and does
// not read, with the decision's detail beside it as evidence. A refused row
// holds no proof, and it is the one state a row is revised out of: a later
// refusal rewrites it and a proof replaces it, after which the row is sealed
// on TaskCleanup's terms.
type RunCleanup struct {
	// Run names the run.
	Run string `json:"run"`
	// Proof is which of P12's proofs the check established, and unknown
	// while the row records a refusal.
	Proof Optional[CleanupProof] `json:"proof"`
	// Examined is the commit identifiers the check read, in the order it
	// read them: for a proof, what established it; for a refusal, what the
	// copy was found to hold. An empty list is a known fact.
	Examined []string `json:"examined"`
	// Refusal is the decision's reason for keeping the copy, and unknown
	// once a proof is recorded. The word is the decision's and not this
	// package's; nothing here reads it.
	Refusal Optional[string] `json:"refusal"`
	// Detail is the decision's own account of what it examined, as
	// evidence. It is empty for a proof.
	Detail string `json:"detail"`
	// RecordedAt is when the row was last written: the refusal's time while
	// it records one, the proof's once it does.
	RecordedAt time.Time `json:"recorded_at"`
	// RemovedAt is when the copy was removed on the strength of the proof,
	// and unknown until MarkRunCleanupRemoved records it.
	RemovedAt Optional[time.Time] `json:"removed_at"`
}

// Kept reports whether the row records a refusal and no proof: the copy was
// examined and left standing.
func (c RunCleanup) Kept() bool { return !c.Proof.IsKnown() && c.Refusal.IsKnown() }

// RecordRunCleanup writes a run's cleanup record, the proof half of it, on
// RecordTaskCleanup's terms: once, refusing a second proof with
// ErrCleanupRecorded, a proof outside the closed set with
// ErrUnknownCleanupProof, a run that does not exist with ErrNotFound, and a
// record carrying a RemovedAt with ErrCleanupRemovalFirst. A refusal
// recorded earlier is replaced, because a copy whose work has since landed
// is the same copy and the proof is what the row exists to hold. The
// refusal fields the caller sets are ignored: a proof is not a refusal.
func (s *Store) RecordRunCleanup(ctx context.Context, c RunCleanup) (RunCleanup, error) {
	if strings.TrimSpace(c.Run) == "" {
		return RunCleanup{}, fmt.Errorf("store: a run cleanup record names no run")
	}
	proof, known := c.Proof.Get()
	if !known || !proof.Recognized() {
		return RunCleanup{}, fmt.Errorf("store: run %s: %w: %q", c.Run, ErrUnknownCleanupProof, proof)
	}
	if c.RemovedAt.IsKnown() {
		return RunCleanup{}, fmt.Errorf("store: run %s: %w", c.Run, ErrCleanupRemovalFirst)
	}
	encoded, err := encodeExamined(c.Examined)
	if err != nil {
		return RunCleanup{}, fmt.Errorf("store: run %s: %w", c.Run, err)
	}
	now := encodeTime(nowUTC())
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if err := runExists(ctx, tx, c.Run); err != nil {
			return err
		}
		var standing Optional[string]
		switch err := tx.QueryRowContext(ctx, `SELECT proof FROM run_cleanup WHERE run = ?`, c.Run).Scan(&standing); {
		case isNoRows(err):
			_, err := tx.ExecContext(ctx, `
				INSERT INTO run_cleanup (run, proof, examined, refusal, detail, recorded_at, removed_at)
				VALUES (?, ?, ?, NULL, '', ?, NULL)`, c.Run, string(proof), encoded, now)
			return err
		case err != nil:
			return err
		case standing.IsKnown():
			return ErrCleanupRecorded
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE run_cleanup SET proof = ?, examined = ?, refusal = NULL, detail = '', recorded_at = ?
			WHERE run = ?`, string(proof), encoded, now, c.Run)
		return err
	})
	if err != nil {
		return RunCleanup{}, fmt.Errorf("store: recording the cleanup of run %s: %w", c.Run, err)
	}
	return s.RunCleanup(ctx, c.Run)
}

// RecordRunCleanupRefusal records that the cleanup of a run's copy refused
// to remove it: reason is the decision's word for why, detail its account
// of what it examined, and examined the commits it found. It is refused with
// ErrCleanupRecorded once a proof stands, because a copy proven landed is
// not kept; otherwise it writes the row, or rewrites a refusal already
// there, so the row says what the most recent decision found. An empty
// reason is refused, and a run that does not exist is ErrNotFound, wrapped.
func (s *Store) RecordRunCleanupRefusal(ctx context.Context, run, reason, detail string, examined []string) (RunCleanup, error) {
	if strings.TrimSpace(run) == "" {
		return RunCleanup{}, fmt.Errorf("store: a run cleanup refusal names no run")
	}
	if strings.TrimSpace(reason) == "" {
		return RunCleanup{}, fmt.Errorf("store: run %s: a cleanup refusal names no reason", run)
	}
	encoded, err := encodeExamined(examined)
	if err != nil {
		return RunCleanup{}, fmt.Errorf("store: run %s: %w", run, err)
	}
	now := encodeTime(nowUTC())
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if err := runExists(ctx, tx, run); err != nil {
			return err
		}
		var standing Optional[string]
		switch err := tx.QueryRowContext(ctx, `SELECT proof FROM run_cleanup WHERE run = ?`, run).Scan(&standing); {
		case isNoRows(err):
			_, err := tx.ExecContext(ctx, `
				INSERT INTO run_cleanup (run, proof, examined, refusal, detail, recorded_at, removed_at)
				VALUES (?, NULL, ?, ?, ?, ?, NULL)`, run, encoded, reason, detail, now)
			return err
		case err != nil:
			return err
		case standing.IsKnown():
			return ErrCleanupRecorded
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE run_cleanup SET examined = ?, refusal = ?, detail = ?, recorded_at = ? WHERE run = ?`,
			encoded, reason, detail, now, run)
		return err
	})
	if err != nil {
		return RunCleanup{}, fmt.Errorf("store: recording the refused cleanup of run %s: %w", run, err)
	}
	return s.RunCleanup(ctx, run)
}

// MarkRunCleanupRemoved records when the run's copy was removed, on
// MarkCleanupRemoved's terms: refused with ErrNotFound when the run has no
// record carrying a proof, because a row recording a refusal is no proof
// and a removal with none behind it is what P12 forbids, and with
// ErrCleanupRemoved when the removal is already marked.
func (s *Store) MarkRunCleanupRemoved(ctx context.Context, run string, at time.Time) error {
	if strings.TrimSpace(run) == "" {
		return fmt.Errorf("store: a run cleanup removal names no run")
	}
	if at.IsZero() {
		return fmt.Errorf("store: marking the cleanup of run %s removed: the removal has no time", run)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var proof, removed Optional[string]
		err := tx.QueryRowContext(ctx, `SELECT proof, removed_at FROM run_cleanup WHERE run = ?`, run).Scan(&proof, &removed)
		if err != nil {
			return fmt.Errorf("store: marking the cleanup of run %s removed: %w", run, errNoRows(err))
		}
		if !proof.IsKnown() {
			return fmt.Errorf("store: marking the cleanup of run %s removed: the record holds a refusal and no proof: %w", run, ErrNotFound)
		}
		if removed.IsKnown() {
			return fmt.Errorf("store: marking the cleanup of run %s removed: %w", run, ErrCleanupRemoved)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_cleanup SET removed_at = ? WHERE run = ?`, encodeTime(at), run); err != nil {
			return fmt.Errorf("store: marking the cleanup of run %s removed: %w", run, err)
		}
		return nil
	})
}

// RunCleanup returns a run's cleanup record, or ErrNotFound when none has
// been written, which is the ordinary state of a run cleanup has not
// reached.
func (s *Store) RunCleanup(ctx context.Context, run string) (RunCleanup, error) {
	if err := s.live(); err != nil {
		return RunCleanup{}, err
	}
	var c RunCleanup
	var proof, refusal, removed Optional[string]
	var examined, recorded string
	err := s.read.QueryRowContext(ctx, `SELECT run, proof, examined, refusal, detail, recorded_at, removed_at
		FROM run_cleanup WHERE run = ?`, run).Scan(&c.Run, &proof, &examined, &refusal, &c.Detail, &recorded, &removed)
	if err != nil {
		return RunCleanup{}, fmt.Errorf("store: reading the cleanup record of run %s: %w", run, errNoRows(err))
	}
	if p, ok := proof.Get(); ok {
		if !CleanupProof(p).Recognized() {
			return RunCleanup{}, fmt.Errorf("store: reading the cleanup record of run %s: column proof: %w: %q", run, ErrUnknownCleanupProof, p)
		}
		c.Proof = Known(CleanupProof(p))
	}
	c.Refusal = refusal
	if err := json.Unmarshal([]byte(examined), &c.Examined); err != nil {
		return RunCleanup{}, fmt.Errorf("store: reading the cleanup record of run %s: column examined: %w", run, err)
	}
	if c.Examined == nil {
		c.Examined = []string{}
	}
	if c.RecordedAt, err = decodeTime(recorded); err != nil {
		return RunCleanup{}, fmt.Errorf("store: reading the cleanup record of run %s: %w", run, err)
	}
	if c.RemovedAt, err = optionalTimeValue(removed, "removed_at"); err != nil {
		return RunCleanup{}, fmt.Errorf("store: reading the cleanup record of run %s: %w", run, err)
	}
	return c, nil
}

// runExists answers ErrNotFound for a run identifier no row carries.
func runExists(ctx context.Context, q querier, id string) error {
	var one int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM run WHERE id = ?`, id).Scan(&one); err != nil {
		return errNoRows(err)
	}
	return nil
}

// encodeExamined encodes a commit list as the examined column holds it, with
// nil read as the empty list it is.
func encodeExamined(examined []string) (string, error) {
	if examined == nil {
		examined = []string{}
	}
	encoded, err := json.Marshal(examined)
	if err != nil {
		return "", fmt.Errorf("encoding the examined commits: %w", err)
	}
	return string(encoded), nil
}
