package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// GraphCheckpoint is one entry of a run's checkpoint history: the payload a
// caller handed over and the sequence this package assigned it.
//
// This is a different record from Checkpoint, which is PRD section 8's
// authoritative one-row-per-run position with a revision and no history. The
// two are kept apart rather than merged because PRD section 7's durability
// layer asks for a run's whole history and a fork from a point in it, neither
// of which a row rewritten in place can answer. Where a run's position has one
// owner is stated in the package comment.
//
// The payload is opaque bytes. internal/graph owns what a checkpoint means,
// and a store that also understood it would be a second owner of the same
// contract, so nothing here reads inside it.
type GraphCheckpoint struct {
	// Run names the run. It is the name a caller drives its graph under and
	// not a reference to a row in the run table: a fork's destination has no
	// run row and cannot be given one here.
	Run string
	// Seq is the entry's position in the run's history, counted from one with
	// no gaps. This package assigns it; a caller never does.
	Seq int
	// Payload is the checkpoint as the caller serialized it.
	Payload []byte
	// WrittenAt is when the entry was appended.
	WrittenAt time.Time
}

// AppendGraphCheckpoint appends payload to the checkpoint history of run,
// anchored to the entry the caller decided the append against, and returns the
// entry with the sequence it was assigned.
//
// The anchor is anchorRun and anchorSeq together, and it is a property of the
// request rather than of the record: no column holds it, no read returns it,
// and it is gone once this call has decided. What it decides:
//
//   - anchorSeq zero claims the run as a new one, and anchorRun is not
//     consulted, because a claim on a run that has never been written names no
//     earlier entry to belong to. It is refused with ErrGraphRunExists when run
//     already has a history.
//   - Any other anchor requires run's latest entry to be exactly that one.
//     It is refused with a *GraphAnchorError, which wraps ErrGraphAnchor and
//     names where the run actually stands, when the run has moved since or when
//     anchorRun is some other run.
//
// The decision and the assignment happen in one transaction on the single
// writer connection, so two callers that read the same tip cannot both find it
// where they expected it and both append. A caller that read the history and
// then wrote would be deciding against a tip that may already have moved,
// which is the check-then-write this accessor exists to replace.
//
// An accepted append is assigned the sequence one past the anchor, always:
// acceptance establishes that the run stands at anchorSeq, and the sequence
// assigned is one past where the run stands. That is what lets a caller whose
// payload must carry its own sequence know the value before the write.
//
// Nothing here revises an entry. There is no accessor that updates or deletes
// one, so a run's history below its tip does not change once written.
func (s *Store) AppendGraphCheckpoint(ctx context.Context, run, anchorRun string, anchorSeq int, payload []byte) (GraphCheckpoint, error) {
	if strings.TrimSpace(run) == "" {
		return GraphCheckpoint{}, fmt.Errorf("store: checkpoint has no run")
	}
	if anchorSeq < 0 {
		return GraphCheckpoint{}, fmt.Errorf(
			"store: run %s: a checkpoint cannot be anchored to sequence %d", run, anchorSeq)
	}

	now := nowUTC()
	var seq int
	// A refusal below says what it is on its own and nothing wraps this call as
	// a whole: a *GraphAnchorError already names the run, the anchor, and where
	// the run stands.
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		stands, err := graphCheckpointTip(ctx, tx, run)
		if err != nil {
			return err
		}
		switch {
		case anchorSeq == 0:
			if stands > 0 {
				return fmt.Errorf("%w: %q stands at checkpoint %d", ErrGraphRunExists, run, stands)
			}
		case anchorRun != run || anchorSeq != stands:
			return &GraphAnchorError{Run: run, AnchorRun: anchorRun, AnchorSeq: anchorSeq, Stands: stands}
		}
		seq = stands + 1
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO graph_checkpoint (run, seq, payload, written_at)
			VALUES (?, ?, ?, ?)`,
			run, seq, bytesOrEmpty(payload), encodeTime(now)); err != nil {
			return fmt.Errorf("store: appending checkpoint %d to run %s: %w", seq, run, err)
		}
		return nil
	})
	if err != nil {
		return GraphCheckpoint{}, err
	}
	return GraphCheckpoint{Run: run, Seq: seq, Payload: bytesOrEmpty(payload), WrittenAt: now}, nil
}

// CopyGraphCheckpoints writes payloads into run as its whole history, in the
// order given and numbered from one, and returns the entries it wrote.
//
// It claims the destination the way an append with a zero anchor claims a new
// run: it is refused with ErrGraphRunExists when run already has a history, and
// that decision is made in the same transaction as the copy, so a destination
// two callers claim at once is written by exactly one of them.
//
// It is the destination half of a fork. Reading the source is the caller's, and
// this package does not know which run the payloads came from or what they say
// about it.
func (s *Store) CopyGraphCheckpoints(ctx context.Context, run string, payloads [][]byte) ([]GraphCheckpoint, error) {
	if strings.TrimSpace(run) == "" {
		return nil, fmt.Errorf("store: no run named to copy checkpoints into")
	}
	if len(payloads) == 0 {
		return nil, fmt.Errorf("store: run %s: copying no checkpoints would claim it and leave it empty", run)
	}

	now := nowUTC()
	var written []GraphCheckpoint
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		written = make([]GraphCheckpoint, 0, len(payloads))
		stands, err := graphCheckpointTip(ctx, tx, run)
		if err != nil {
			return err
		}
		if stands > 0 {
			return fmt.Errorf("%w: %q stands at checkpoint %d", ErrGraphRunExists, run, stands)
		}
		for i, payload := range payloads {
			seq := i + 1
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO graph_checkpoint (run, seq, payload, written_at)
				VALUES (?, ?, ?, ?)`,
				run, seq, bytesOrEmpty(payload), encodeTime(now)); err != nil {
				return fmt.Errorf("store: copying checkpoint %d into run %s: %w", seq, run, err)
			}
			written = append(written, GraphCheckpoint{
				Run: run, Seq: seq, Payload: bytesOrEmpty(payload), WrittenAt: now,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return written, nil
}

// LatestGraphCheckpoint returns the entry with the highest sequence in run's
// history, or ErrNotFound when the run has none.
//
// The entry it answers with is the latest position rather than the last line of
// an event log: every entry is a complete position, and which one is latest is
// the sequence AppendGraphCheckpoint assigned under the same serialization that
// decided the anchor. P8 is about inferring current state from a log of things
// that happened, which this is not.
func (s *Store) LatestGraphCheckpoint(ctx context.Context, run string) (GraphCheckpoint, error) {
	if err := s.live(); err != nil {
		return GraphCheckpoint{}, err
	}
	var c GraphCheckpoint
	var written string
	err := s.read.QueryRowContext(ctx, `
		SELECT run, seq, payload, written_at FROM graph_checkpoint
		WHERE run = ? ORDER BY seq DESC LIMIT 1`, run).
		Scan(&c.Run, &c.Seq, &c.Payload, &written)
	if err != nil {
		return GraphCheckpoint{}, fmt.Errorf(
			"store: reading the latest checkpoint of run %s: %w", run, errNoRows(err))
	}
	if c.WrittenAt, err = decodeTime(written); err != nil {
		return GraphCheckpoint{}, fmt.Errorf("store: reading the latest checkpoint of run %s: %w", run, err)
	}
	c.Payload = bytesOrEmpty(c.Payload)
	return c, nil
}

// GraphCheckpointHistory returns run's entries in sequence order. A run with no
// history yields an empty slice and no error.
func (s *Store) GraphCheckpointHistory(ctx context.Context, run string) ([]GraphCheckpoint, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `
		SELECT run, seq, payload, written_at FROM graph_checkpoint
		WHERE run = ? ORDER BY seq`, run)
	if err != nil {
		return nil, fmt.Errorf("store: listing the checkpoints of run %s: %w", run, err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]GraphCheckpoint, 0)
	for rows.Next() {
		var c GraphCheckpoint
		var written string
		if err := rows.Scan(&c.Run, &c.Seq, &c.Payload, &written); err != nil {
			return nil, fmt.Errorf("store: listing the checkpoints of run %s: %w", run, err)
		}
		if c.WrittenAt, err = decodeTime(written); err != nil {
			return nil, fmt.Errorf("store: listing the checkpoints of run %s: %w", run, err)
		}
		// A driver may hand back a nil slice for a zero-length blob. The column
		// is NOT NULL, so there is nothing for a nil to mean here.
		c.Payload = bytesOrEmpty(c.Payload)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing the checkpoints of run %s: %w", run, err)
	}
	return out, nil
}

// graphCheckpointTip reads the sequence a run stands at inside tx, which is
// zero when the run has no history. It is read on the writer connection inside
// the transaction that will act on it, which is what makes the anchor decision
// and the sequence assignment one step rather than two.
func graphCheckpointTip(ctx context.Context, tx *sql.Tx, run string) (int, error) {
	var tip Optional[int64]
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(seq) FROM graph_checkpoint WHERE run = ?`, run).Scan(&tip); err != nil {
		return 0, fmt.Errorf("store: reading the checkpoint history of run %s: %w", run, err)
	}
	return int(tip.Or(0)), nil
}
