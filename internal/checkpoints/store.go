package checkpoints

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/store"
)

// Store is a durable [graph.CheckpointStore]. It keeps a run's checkpoints in
// the database internal/store owns, so the run's position outlives the process
// that reached it.
//
// A Store is safe for concurrent use, and the guarantee it rests on is
// internal/store's: every append is decided and assigned inside one transaction
// on a single writer connection, so two callers appending to one run cannot
// both be told their write landed.
type Store struct {
	records *store.Store
}

// Store satisfies the interface it is written against, checked here so a
// signature that drifts fails to build rather than failing to be usable.
var _ graph.CheckpointStore = (*Store)(nil)

// New returns a Store that keeps its checkpoints in records, which must be
// open. Nothing is written or read until an operation is called.
func New(records *store.Store) *Store { return &Store{records: records} }

// Write implements [graph.CheckpointStore].
//
// The anchor goes to the store as the anchor of one append, so the decision it
// governs and the sequence assigned are one step under the writer connection.
// Nothing here reads the run's tip first: a read here and a write there could
// interleave, which is the check-then-write the anchor exists to replace.
//
// The sequence written into the payload is one past the anchor, which is the
// sequence an accepted append is assigned, because acceptance establishes that
// the run stands at the anchor. A refused append writes nothing, so a payload
// carrying a sequence that was never assigned is never stored.
func (s *Store) Write(ctx context.Context, anchor graph.CheckpointID, c graph.Checkpoint) (graph.CheckpointID, error) {
	if c.Run == "" {
		return graph.CheckpointID{}, &graph.CheckpointError{Field: "run", Detail: "is empty"}
	}
	c.Seq = anchor.Seq + 1
	payload, err := encode(c)
	if err != nil {
		return graph.CheckpointID{}, err
	}
	entry, err := s.records.AppendGraphCheckpoint(ctx, c.Run, anchor.Run, anchor.Seq, payload)
	if err != nil {
		return graph.CheckpointID{}, translateAppend(c.Run, anchor, err)
	}
	return graph.CheckpointID{Run: entry.Run, Seq: entry.Seq}, nil
}

// Latest implements [graph.CheckpointStore].
func (s *Store) Latest(ctx context.Context, run string) (graph.Checkpoint, error) {
	entry, err := s.records.LatestGraphCheckpoint(ctx, run)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return graph.Checkpoint{}, fmt.Errorf("%w: %q", graph.ErrNoSuchRun, run)
		}
		return graph.Checkpoint{}, err
	}
	return decode(entry)
}

// History implements [graph.CheckpointStore].
func (s *Store) History(ctx context.Context, run string) ([]graph.Checkpoint, error) {
	entries, err := s.records.GraphCheckpointHistory(ctx, run)
	if err != nil {
		return nil, err
	}
	out := make([]graph.Checkpoint, 0, len(entries))
	for _, entry := range entries {
		c, err := decode(entry)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Fork implements [graph.CheckpointStore].
//
// The source is read first and the destination is claimed by the copy itself,
// so a destination two forks reach at once is written by exactly one of them.
// Reading the source separately answers the same bytes the copy writes, because
// nothing revises an entry once it is appended and a fork copies only the
// prefix up to its point.
func (s *Store) Fork(ctx context.Context, from graph.CheckpointID, into string) (graph.CheckpointID, error) {
	if into == "" {
		return graph.CheckpointID{}, &graph.CheckpointError{
			Field: "run", Detail: "the run to fork into has an empty name",
		}
	}
	if into == from.Run {
		return graph.CheckpointID{}, fmt.Errorf("%w: %q", graph.ErrRunExists, into)
	}
	source, err := s.records.GraphCheckpointHistory(ctx, from.Run)
	if err != nil {
		return graph.CheckpointID{}, err
	}
	if from.Seq < 1 || from.Seq > len(source) {
		return graph.CheckpointID{}, fmt.Errorf("%w: %s", graph.ErrNoSuchCheckpoint, from)
	}
	copied := make([][]byte, 0, from.Seq)
	for i := 0; i < from.Seq; i++ {
		c, err := decode(source[i])
		if err != nil {
			return graph.CheckpointID{}, err
		}
		origin := graph.CheckpointID{Run: from.Run, Seq: c.Seq}
		c.Run = into
		c.Seq = i + 1
		c.ForkedFrom = &origin
		payload, err := encode(c)
		if err != nil {
			return graph.CheckpointID{}, err
		}
		copied = append(copied, payload)
	}
	if _, err := s.records.CopyGraphCheckpoints(ctx, into, copied); err != nil {
		if errors.Is(err, store.ErrGraphRunExists) {
			return graph.CheckpointID{}, fmt.Errorf("%w: %q", graph.ErrRunExists, into)
		}
		return graph.CheckpointID{}, err
	}
	return graph.CheckpointID{Run: into, Seq: from.Seq}, nil
}

// translateAppend renders a refused append as the condition the interface
// requires, in the wording graph.MemoryStore uses for the same refusal, so a
// caller reading either one is reading the same thing. Anything else is the
// substrate's own failure and is passed through as it stands.
func translateAppend(run string, anchor graph.CheckpointID, err error) error {
	var moved *store.GraphAnchorError
	switch {
	case errors.Is(err, store.ErrGraphRunExists):
		return fmt.Errorf("%w: %q", graph.ErrRunExists, run)
	case errors.As(err, &moved):
		return fmt.Errorf("%w: anchored to %s, the run stands at %s",
			graph.ErrStaleAnchor, anchor, graph.CheckpointID{Run: run, Seq: moved.Stands})
	}
	return err
}

// encode renders a checkpoint as the bytes the record holds, which is the
// checkpoint's own JSON shape and nothing else.
func encode(c graph.Checkpoint) ([]byte, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("checkpoints: encoding checkpoint %s: %w", c.ID(), err)
	}
	return b, nil
}

// decode reads a stored entry back. The payload is untrusted input, so what
// refuses a malformed one is graph.Checkpoint's own decoder, which rejects an
// unknown field and an unrecognized enumeration rather than defaulting either.
func decode(entry store.GraphCheckpoint) (graph.Checkpoint, error) {
	var c graph.Checkpoint
	if err := json.Unmarshal(entry.Payload, &c); err != nil {
		return graph.Checkpoint{}, fmt.Errorf("checkpoints: decoding checkpoint %s#%d: %w",
			entry.Run, entry.Seq, err)
	}
	return c, nil
}
