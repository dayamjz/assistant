package taskstate

import (
	"context"

	"github.com/dayamjz/assistant/internal/store"
)

// Reader resolves tasks out of a store. It holds no state of its own beyond
// what it was built with, so every call reads the records afresh.
type Reader struct {
	store     *store.Store
	probe     func(ctx context.Context, sessionRef string) Liveness
	advancing func(runID string) bool
}

// NewReader returns a Reader over s.
//
// probe reports the liveness of a task's terminal session by its reference,
// which is the session of the task's latest attempt; it is called only for a
// task whose latest attempt has one, and a nil probe means no task's terminal
// is ever observed. advancing reports whether the service is executing a run
// now, by the run's identifier; it is asked about the newest attributed run
// only, and a nil advancing means no run is ever advancing. Both are functions
// and not interfaces because their consumers are this package's tiers and the
// two modules that answer them: the session provider for the probe, and the
// service's slot map for advancing.
//
// Both run after the store has been read, outside its transaction, so neither
// answer is as of the snapshot. The liveness is as of the probe and may be
// stale by the time the answer is read. The advancing fact is as of the slot
// map, so a run that settled between the snapshot and the question can be
// reported advancing by the slot and settled by the record; Resolve reads the
// record for the state and treats the disagreement as no progress, which is
// stated on Resolution.Progress.
func NewReader(s *store.Store, probe func(ctx context.Context, sessionRef string) Liveness, advancing func(runID string) bool) *Reader {
	return &Reader{store: s, probe: probe, advancing: advancing}
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
	in := Inputs{
		Task: snap.Task, Runs: snap.Runs, Holds: snap.Holds, Events: snap.Events,
		MergeRecords: snap.MergeRecords, Cleanup: snap.Cleanup, Process: Unobserved,
	}
	if n := len(snap.Attempts); n > 0 && r.probe != nil {
		if ref, ok := snap.Attempts[n-1].SessionRef.Get(); ok {
			in.Process = r.probe(ctx, ref)
		}
	}
	if run, ok := newest(snap.Runs); ok && r.advancing != nil {
		in.Advancing = r.advancing(run.ID)
	}
	return Resolve(in)
}
