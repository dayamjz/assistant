package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Checkpoint is the authoritative position of a run's graph, written after
// every node. There is one per run, and writing a new one replaces the old:
// a checkpoint answers where the run is now, and a history of where it has been
// is what the rounds and the events are for.
type Checkpoint struct {
	// RunID names the run.
	RunID string
	// State is the graph's shared state, serialized. internal/graph owns what
	// is in it; this package stores bytes.
	State []byte
	// Position is the node the run has reached.
	Position string
	// OpenDecision names a decision waiting on a person, if one is. It is
	// unknown when nothing waits, which is a different fact from a decision
	// whose name happens to be empty.
	OpenDecision Optional[string]
	// Revision increases by one on every write, so a reader can tell a
	// checkpoint it has already seen from a newer one without comparing
	// contents.
	Revision int64
	// WrittenAt is when this checkpoint was written.
	WrittenAt time.Time
}

// WriteCheckpoint replaces a run's checkpoint and returns it with the revision
// it was given. The read of the previous revision and the write of the new one
// happen in one transaction on the single writer connection, so two concurrent
// writers cannot be handed the same revision.
func (s *Store) WriteCheckpoint(ctx context.Context, c Checkpoint) (Checkpoint, error) {
	if strings.TrimSpace(c.RunID) == "" {
		return Checkpoint{}, fmt.Errorf("store: checkpoint has no run")
	}
	if strings.TrimSpace(c.Position) == "" {
		return Checkpoint{}, fmt.Errorf("store: checkpoint of run %s has no position", c.RunID)
	}

	now := nowUTC()
	var revision int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var previous Optional[int64]
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM checkpoint WHERE run_id = ?`, c.RunID).
			Scan(&previous); err != nil && !isNoRows(err) {
			return err
		}
		revision = previous.Or(0) + 1
		_, err := tx.ExecContext(ctx, `
			INSERT INTO checkpoint (run_id, state, position, open_decision, revision, written_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id) DO UPDATE SET
				state         = excluded.state,
				position      = excluded.position,
				open_decision = excluded.open_decision,
				revision      = excluded.revision,
				written_at    = excluded.written_at`,
			c.RunID, bytesOrEmpty(c.State), c.Position, c.OpenDecision, revision, encodeTime(now))
		return err
	})
	if err != nil {
		return Checkpoint{}, fmt.Errorf("store: writing the checkpoint of run %s: %w", c.RunID, err)
	}
	c.Revision = revision
	c.WrittenAt = now
	c.State = bytesOrEmpty(c.State)
	return c, nil
}

// Checkpoint returns a run's checkpoint, or ErrNotFound when the run has not
// reached one.
func (s *Store) Checkpoint(ctx context.Context, runID string) (Checkpoint, error) {
	if err := s.live(); err != nil {
		return Checkpoint{}, err
	}
	var c Checkpoint
	var written string
	err := s.read.QueryRowContext(ctx, `
		SELECT run_id, state, position, open_decision, revision, written_at
		FROM checkpoint WHERE run_id = ?`, runID).
		Scan(&c.RunID, &c.State, &c.Position, &c.OpenDecision, &c.Revision, &written)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("store: reading the checkpoint of run %s: %w", runID, errNoRows(err))
	}
	if c.WrittenAt, err = decodeTime(written); err != nil {
		return Checkpoint{}, fmt.Errorf("store: reading the checkpoint of run %s: %w", runID, err)
	}
	c.State = bytesOrEmpty(c.State)
	return c, nil
}
