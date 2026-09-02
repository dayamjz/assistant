package graph

import (
	"context"
	"fmt"
	"sync"
)

// CheckpointStore is the boundary between the topology layer and whatever
// makes it durable. It has exactly four operations and does not grow a fifth:
// keeping the boundary narrow is what lets the substrate change later without
// the topology layer noticing.
//
// Reading a checkpoint back is deliberately limited to the latest one for a
// run and to the run's full history. Resuming from an earlier point is done by
// forking that point into a new run and resuming that, which leaves the
// original history intact.
type CheckpointStore interface {
	// Write appends c to the history of the run named by c.Run and returns the
	// identifier assigned to it. The store, not the caller, assigns Seq, and
	// the first checkpoint of a run is assigned Seq 1. The checkpoint is the
	// caller's to give: an implementation may retain it as it stands, because
	// what a caller hands over is never written to again.
	//
	// A checkpoint whose Seq is zero claims the run as a new one. Honouring
	// that claim is required of every implementation, not a description of any
	// one of them: refuse such a write with an error wrapping ErrRunExists
	// when c.Run already has history, and decide it atomically with assigning
	// Seq, under whatever serializes the store's writes. Claiming a run is
	// therefore one operation rather than a read followed by a write. The
	// claim is what gives ErrRunExists its meaning and what keeps two callers
	// starting the same run from interleaving into one history and losing a
	// run's work; a substrate that only appends and assigns Seq loses both,
	// and loses them silently.
	Write(ctx context.Context, c Checkpoint) (CheckpointID, error)
	// Latest returns the most recently written checkpoint for run. It returns
	// an error wrapping ErrNoSuchRun when the run has no history. What it
	// answers with may be the store's own storage: a caller never writes into
	// a checkpoint a store handed back, so nothing it returns has to be copied
	// defensively.
	Latest(ctx context.Context, run string) (Checkpoint, error)
	// History returns every checkpoint for run in write order. A run with no
	// history yields an empty slice and no error. As with Latest, the
	// checkpoints may be the store's own storage, because a caller treats what
	// it is handed as read-only.
	History(ctx context.Context, run string) ([]Checkpoint, error)
	// Fork copies the history of from.Run up to and including from into a new
	// run named into, and returns the identifier of the copy's last
	// checkpoint. The source run is not modified. It returns an error wrapping
	// ErrRunExists when into already has history.
	Fork(ctx context.Context, from CheckpointID, into string) (CheckpointID, error)
}

// MemoryStore is an in-memory CheckpointStore. It holds checkpoints in their
// serialized form and decodes them on every read, so the data-only encoding
// and its validation are on the ordinary path rather than only on the path a
// persistent substrate would take.
//
// A MemoryStore is safe for concurrent use.
type MemoryStore struct {
	mu   sync.Mutex
	runs map[string][][]byte
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{runs: make(map[string][][]byte)}
}

// Write implements CheckpointStore.
func (s *MemoryStore) Write(ctx context.Context, c Checkpoint) (CheckpointID, error) {
	if err := ctx.Err(); err != nil {
		return CheckpointID{}, fmt.Errorf("write checkpoint: %w", err)
	}
	if c.Run == "" {
		return CheckpointID{}, &CheckpointError{Field: "run", Detail: "is empty"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Seq == 0 && len(s.runs[c.Run]) > 0 {
		return CheckpointID{}, fmt.Errorf("%w: %q", ErrRunExists, c.Run)
	}
	c.Seq = len(s.runs[c.Run]) + 1
	encoded, err := encodeCheckpoint(c)
	if err != nil {
		return CheckpointID{}, err
	}
	s.runs[c.Run] = append(s.runs[c.Run], encoded)
	return c.ID(), nil
}

// Latest implements CheckpointStore.
func (s *MemoryStore) Latest(ctx context.Context, run string) (Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return Checkpoint{}, fmt.Errorf("read latest checkpoint: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.runs[run]
	if len(history) == 0 {
		return Checkpoint{}, fmt.Errorf("%w: %q", ErrNoSuchRun, run)
	}
	return decodeCheckpoint(history[len(history)-1])
}

// History implements CheckpointStore.
func (s *MemoryStore) History(ctx context.Context, run string) ([]Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list checkpoint history: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.runs[run]
	out := make([]Checkpoint, 0, len(history))
	for _, encoded := range history {
		c, err := decodeCheckpoint(encoded)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Fork implements CheckpointStore.
func (s *MemoryStore) Fork(ctx context.Context, from CheckpointID, into string) (CheckpointID, error) {
	if err := ctx.Err(); err != nil {
		return CheckpointID{}, fmt.Errorf("fork checkpoint: %w", err)
	}
	if into == "" {
		return CheckpointID{}, &CheckpointError{Field: "run", Detail: "the run to fork into has an empty name"}
	}
	if into == from.Run {
		return CheckpointID{}, fmt.Errorf("%w: %q", ErrRunExists, into)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	source := s.runs[from.Run]
	if from.Seq < 1 || from.Seq > len(source) {
		return CheckpointID{}, fmt.Errorf("%w: %s", ErrNoSuchCheckpoint, from)
	}
	if len(s.runs[into]) > 0 {
		return CheckpointID{}, fmt.Errorf("%w: %q", ErrRunExists, into)
	}
	copied := make([][]byte, 0, from.Seq)
	for i := 0; i < from.Seq; i++ {
		c, err := decodeCheckpoint(source[i])
		if err != nil {
			return CheckpointID{}, err
		}
		origin := CheckpointID{Run: from.Run, Seq: c.Seq}
		c.Run = into
		c.Seq = i + 1
		c.ForkedFrom = &origin
		encoded, err := encodeCheckpoint(c)
		if err != nil {
			return CheckpointID{}, err
		}
		copied = append(copied, encoded)
	}
	s.runs[into] = copied
	return CheckpointID{Run: into, Seq: from.Seq}, nil
}
