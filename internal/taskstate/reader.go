package taskstate

import (
	"context"

	"github.com/dayamjz/assistant/internal/store"
)

// Reader resolves tasks out of a store. It holds no state of its own beyond
// what it was built with, so every call reads the records afresh.
type Reader struct {
	store *store.Store
	probe func(ctx context.Context, sessionRef string) Liveness
}

// NewReader returns a Reader over s. probe reports the liveness of a task's
// terminal session by its reference; it is called only for a task that has
// one, and a nil probe means no task's terminal is ever observed. It is a
// function and not an interface because its consumers are this package's
// process tier and the session module that does not exist yet.
//
// The probe runs after the store has been read, outside its transaction, so
// the liveness it reports is as of the probe and may be stale by the time the
// answer is read.
func NewReader(s *store.Store, probe func(ctx context.Context, sessionRef string) Liveness) *Reader {
	return &Reader{store: s, probe: probe}
}

// Resolved is a task record with the state resolved from the same read, so the
// record and the state never come from different points in time.
type Resolved struct {
	// Record is the task record as the snapshot read it.
	Record store.Task `json:"record"`
	// State is what Resolve made of that snapshot.
	State Resolution `json:"state"`
}

// Task resolves one task. It returns store.ErrNotFound, wrapped, for a task
// that does not exist. It reads and writes nothing else.
func (r *Reader) Task(ctx context.Context, id string) (Resolution, error) {
	got, err := r.Resolved(ctx, id)
	return got.State, err
}

// Resolved resolves one task and returns its record from the same snapshot.
// It fails as Task does.
func (r *Reader) Resolved(ctx context.Context, id string) (Resolved, error) {
	snap, err := r.store.TaskSnapshot(ctx, id)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Record: snap.Task, State: r.resolve(ctx, snap)}, nil
}

// All resolves every task, newest task first, each with its record, from
// records read in a single transaction.
func (r *Reader) All(ctx context.Context) ([]Resolved, error) {
	snaps, err := r.store.TaskSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Resolved, 0, len(snaps))
	for _, snap := range snaps {
		out = append(out, Resolved{Record: snap.Task, State: r.resolve(ctx, snap)})
	}
	return out, nil
}

func (r *Reader) resolve(ctx context.Context, snap store.TaskSnapshot) Resolution {
	liveness := Unobserved
	if ref, ok := snap.Task.SessionRef.Get(); ok && r.probe != nil {
		liveness = r.probe(ctx, ref)
	}
	return Resolve(Inputs{
		Task: snap.Task, Runs: snap.Runs, Holds: snap.Holds, Events: snap.Events, Process: liveness,
	})
}
