package taskstate

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/store"
)

// Prober reports whether the terminal a session reference names is alive. It
// is I/O, so a Settler calls it before the transaction that writes its answer.
type Prober func(ctx context.Context, sessionRef string) Liveness

// Settler is the one writer of task state: it runs Resolve over a snapshot read
// inside the store transaction that stores the result.
//
// What the type guarantees is bounded. store.SettleTaskState is exported, so a
// package that passes its own decision can still write the record; this build
// passes the resolver's, and TestOnlyDecisionMethodsWriteTaskState pins which
// store methods accept a decision at all. The terminal probe
// runs before the transaction, so the Process input is as-of-probe, and an
// input changed by a writer that does not settle leaves the record stale until
// the next settle: Reader.Audit detects that and nothing prevents it.
type Settler struct {
	store *store.Store
	probe Prober
}

// NewSettler builds a Settler over a store. A nil probe means every terminal is
// Unobserved, which skips the process rung.
func NewSettler(s *store.Store, probe Prober) *Settler {
	return &Settler{store: s, probe: probe}
}

// Create records a task, its opening event, and its opening state, which the
// resolver decides, in one transaction.
func (s *Settler) Create(ctx context.Context, t store.Task) (store.Task, error) {
	return s.store.CreateTask(ctx, t, decision(s.liveness(ctx, t)))
}

// Append adds an event to a task's history and settles its state in the same
// transaction, so there is no window in which the log holds the event and the
// record has not seen it.
func (s *Settler) Append(ctx context.Context, taskID, kind, detail string) (store.TaskEvent, store.TaskState, error) {
	t, err := s.store.Task(ctx, taskID)
	if err != nil {
		return store.TaskEvent{}, store.TaskState{}, err
	}
	return s.store.AppendTaskEventAndSettle(ctx, taskID, kind, detail, decision(s.liveness(ctx, t)))
}

// Settle re-resolves one task and stores the result. A resolution equal to the
// stored one writes no new answer and does not move the revision, so the
// revision counts changes of the answer rather than settles.
func (s *Settler) Settle(ctx context.Context, taskID string) (store.TaskState, error) {
	t, err := s.store.Task(ctx, taskID)
	if err != nil {
		return store.TaskState{}, err
	}
	return s.store.SettleTaskState(ctx, taskID, decision(s.liveness(ctx, t)))
}

// SettleForRun settles every task a run is attributed to. It attempts all of
// them and reports every failure.
func (s *Settler) SettleForRun(ctx context.Context, runID string) error {
	ids, err := s.store.TasksForRun(ctx, runID)
	if err != nil {
		return err
	}
	return s.settleEach(ctx, ids)
}

// SettleAll settles every task, which is what a service owes its records when
// it opens: whatever moved while nothing was settling is caught up.
func (s *Settler) SettleAll(ctx context.Context) error {
	tasks, err := s.store.Tasks(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ID
	}
	return s.settleEach(ctx, ids)
}

func (s *Settler) settleEach(ctx context.Context, ids []string) error {
	var errs []error
	for _, id := range ids {
		if _, err := s.Settle(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("taskstate: settling task %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Settler) liveness(ctx context.Context, t store.Task) Liveness {
	return probeOf(ctx, s.probe, t)
}

func probeOf(ctx context.Context, probe Prober, t store.Task) Liveness {
	ref, known := t.SessionRef.Get()
	if probe == nil || !known {
		return Unobserved
	}
	return probe(ctx, ref)
}

// decision closes the resolver over a liveness already probed, in the shape the
// store takes.
func decision(live Liveness) store.TaskDecision {
	return func(snap store.TaskSnapshot) (store.TaskStateWrite, error) {
		return Resolve(inputsOf(snap, live)).write()
	}
}

func inputsOf(snap store.TaskSnapshot, live Liveness) Inputs {
	return Inputs{Task: snap.Task, Runs: snap.Runs, Holds: snap.Holds, Events: snap.Events, Process: live}
}
