package runs_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
)

// heard is what an OnFinish callback was called with, in order.
type heard struct {
	mu   sync.Mutex
	runs []store.Run
}

func (h *heard) record(_ context.Context, r store.Run) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runs = append(h.runs, r)
}

func (h *heard) take() []store.Run {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.runs
	h.runs = nil
	return out
}

// OnFinish is called for every move that leaves a run in one of the three
// terminal statuses, with the row the move returned, and for no other move
// and no refused one. The terminal statuses are written here rather than
// read from runs.Finished, so the table is not its own witness, and every
// move is driven against every status, so a move that never reaches a
// terminal status is shown not to call it, which is the half that keeps a
// sweep off an active run's copy.
func TestOnFinishHearsEveryEndingAndNothingElse(t *testing.T) {
	terminal := []store.RunStatus{store.RunPassed, store.RunFailed, store.RunTerminated}
	agent := resolution(standin.New(t, standin.Script{}))
	for _, move := range transitions {
		for _, from := range store.RunStatuses() {
			t.Run(move.name+" from "+from.String(), func(t *testing.T) {
				ctx := t.Context()
				s, _ := openStore(t)
				var got heard
				svc, err := runs.New(runs.Options{Store: s, Agent: agent, OnFinish: got.record})
				if err != nil {
					t.Fatalf("runs.New: %v", err)
				}
				run := seedRun(t, s, svc, "run-1")
				place(t, svc, run.ID, from)
				got.take()

				after, err := move.call(svc, ctx, run.ID)
				calls := got.take()
				if err != nil {
					if len(calls) != 0 {
						t.Fatalf("a refused %s from %s called OnFinish with %+v", move.name, from, calls)
					}
					return
				}
				if !contains(terminal, after.Status) {
					if len(calls) != 0 {
						t.Fatalf("%s from %s left the run %s and called OnFinish with %+v", move.name, from, after.Status, calls)
					}
					return
				}
				if len(calls) != 1 {
					t.Fatalf("%s from %s left the run %s and called OnFinish %d times, want once", move.name, from, after.Status, len(calls))
				}
				if calls[0].ID != after.ID || calls[0].Status != after.Status || !calls[0].UpdatedAt.Equal(after.UpdatedAt) {
					t.Fatalf("OnFinish was given %+v, want the row the move returned, %+v", calls[0], after)
				}
			})
		}
	}
}

// OnFinish runs after the lock the moves take is given back, so a callback
// that moves a run itself, or asks for a fixer role, does not wait on the
// move that called it. A callback under the lock would deadlock here.
func TestOnFinishRunsOutsideTheLock(t *testing.T) {
	ctx := t.Context()
	s, _ := openStore(t)
	var svc *runs.Service
	var again store.Run
	var againErr error
	svc, err := runs.New(runs.Options{
		Store: s, Agent: resolution(standin.New(t, standin.Script{})),
		OnFinish: func(ctx context.Context, r store.Run) {
			if r.Status == store.RunTerminated {
				return
			}
			again, againErr = svc.Terminate(ctx, r.ID)
		},
	})
	if err != nil {
		t.Fatalf("runs.New: %v", err)
	}
	run := seedRun(t, s, svc, "run-1")
	if _, err := svc.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Pass(ctx, run.ID); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	// Terminate from passed is refused by the table, which is the answer a
	// callback that reached the store gets; what this establishes is that it
	// reached it.
	if !errors.Is(againErr, store.ErrRunStatus) || again.ID != "" {
		t.Fatalf("the callback's move answered %+v, %v, want the table's refusal of a move out of passed", again, againErr)
	}
}
