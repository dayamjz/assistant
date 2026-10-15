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
// answer is as of the snapshot, and they run in a fixed order: the records,
// then the slot, then the probe, which is the order P8's "one consistent
// read" is approximated in. The liveness is as of the probe and may be
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
// It fails as Task does, and it is ObserveTask with the snapshot reduced to
// the task record.
func (r *Reader) Resolved(ctx context.Context, id string) (Resolved, error) {
	o, err := r.ObserveTask(ctx, id)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Record: o.Snapshot.Task, State: o.State}, nil
}

// ObserveTask resolves one task and returns it with the snapshot it was
// resolved from, on Observe's terms, for a reader that needs a record the
// state was derived from beside the state: the task surface in
// internal/service, which fills in what a paused worker said it is waiting
// on from the history entry the resolution's basis names, out of the same
// read rather than a second one. It fails as Task does.
func (r *Reader) ObserveTask(ctx context.Context, id string) (Observed, error) {
	snap, err := r.store.TaskSnapshot(ctx, id)
	if err != nil {
		return Observed{}, err
	}
	return Observed{Snapshot: snap, State: r.resolve(ctx, snap)}, nil
}

// All resolves every task, newest task first, each with its record, from
// records read in a single transaction.
func (r *Reader) All(ctx context.Context) ([]Resolved, error) {
	observed, err := r.Observe(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Resolved, 0, len(observed))
	for _, o := range observed {
		out = append(out, Resolved{Record: o.Snapshot.Task, State: o.State})
	}
	return out, nil
}

// Observed is a task's snapshot with the state resolved from it, for a
// reader that needs the records the state was derived from beside the state:
// the supervisor loop in internal/service, which reads the events past its
// marker and the open holds out of the same read the state came from rather
// than from a second read that could see a different log (P8).
type Observed struct {
	// Snapshot is every record the state was resolved from, as one read.
	Snapshot store.TaskSnapshot
	// State is what Resolve made of it.
	State Resolution
}

// Observe resolves every task, newest task first, and returns each with the
// snapshot it was resolved from. It reads what All reads and nothing more;
// All is Observe with the snapshot reduced to the task record.
func (r *Reader) Observe(ctx context.Context) ([]Observed, error) {
	snaps, err := r.store.TaskSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Observed, 0, len(snaps))
	for _, snap := range snaps {
		out = append(out, Observed{Snapshot: snap, State: r.resolve(ctx, snap)})
	}
	return out, nil
}

// resolve asks the two live inputs in the order NewReader states, the slot
// before the probe, and resolves.
func (r *Reader) resolve(ctx context.Context, snap store.TaskSnapshot) Resolution {
	in := Inputs{
		Task: snap.Task, Runs: snap.Runs, Holds: snap.Holds, Events: snap.Events,
		MergeRecords: snap.MergeRecords, Cleanup: snap.Cleanup, LocalLanding: snap.LocalLanding,
		ReportStoredAt: snap.ReportStoredAt, Process: Unobserved,
	}
	if run, ok := newest(snap.Runs); ok && r.advancing != nil {
		in.Advancing = r.advancing(run.ID)
	}
	if n := len(snap.Attempts); n > 0 && r.probe != nil {
		if ref, ok := snap.Attempts[n-1].SessionRef.Get(); ok {
			in.Process = r.probe(ctx, ref)
		}
	}
	return Resolve(in)
}
