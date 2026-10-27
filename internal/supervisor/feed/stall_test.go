package feed_test

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// heldTask plants one task held on the hold keyed key, with no wake queued
// about it: a task whose only waiter, by its state and its queue, is a
// person.
func heldTask(t *testing.T, s *store.Store, key string) {
	t.Helper()
	const id = "abcdefghij23"
	if _, err := s.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "headless", WorktreePath: "/worktrees/one",
	}, store.Disposition{}); err != nil {
		t.Fatalf("creating a task: %v", err)
	}
	if _, err := s.RegisterHold(t.Context(), store.Hold{Key: key, TaskID: store.Known(id), Subject: "a decision"}); err != nil {
		t.Fatalf("registering the hold: %v", err)
	}
}

// PRD section 6's "a held stall stays watched", at the stop guard, through
// the real lock, beacon, instance file and read-only database P10's own test
// reads: a task held on an open stall hold with nothing queued about it is
// in flight, so with no live supervisor holding the lock a turn end is
// blocked, and with a healthy one it is allowed, on P10's terms. The control
// is the same task held on a stop's hold instead, which with no supervisor at
// all is nothing in flight, so the blocking case blocks because the hold is a
// stall's and not because every held task blocks.
func TestAnOpenStallHoldKeepsTheStopGuardOnP10sTerms(t *testing.T) {
	principles.Cite(t, principles.P10)
	t.Parallel()
	fresh := supervisor.FreshnessFor(10 * time.Second)
	stall := supervisor.StallHold{Task: "abcdefghij23", Attempt: 1, Epoch: 1, Surface: 3}.Key()

	stopped := createdHome(t)
	withDatabase(t, stopped, func(s *store.Store) { heldTask(t, s, stall) })
	if got := feed.Guard(t.Context(), stopped, fresh, time.Now()); got.Allowed() || got.Reason == supervisor.StopNothingInFlight {
		t.Fatalf("a stall-held task with no supervisor answered %s, want a block for the missing supervisor", got.Reason)
	}

	healthy := createdHome(t)
	withDatabase(t, healthy, func(s *store.Store) { heldTask(t, s, stall) })
	now := time.Now()
	holdInAnotherProcess(t, healthy)
	writeInstance(t, healthy, instance)
	writeBeacon(t, healthy, supervisor.Identity{Instance: instance, Epoch: 1}, now)
	if got := feed.Guard(t.Context(), healthy, fresh, now); got.Reason != supervisor.StopSupervised {
		t.Fatalf("a stall-held task under a healthy supervisor answered %s, want supervised", got.Reason)
	}

	other := createdHome(t)
	withDatabase(t, other, func(s *store.Store) { heldTask(t, s, "task/abcdefghij23/stopped/1") })
	if got := feed.Guard(t.Context(), other, fresh, time.Now()); got.Reason != supervisor.StopNothingInFlight {
		t.Fatalf("a task held on a stop's hold with no supervisor answered %s, want nothing in flight", got.Reason)
	}
}
