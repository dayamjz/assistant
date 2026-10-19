package store

import (
	"context"
	"errors"
	"testing"
)

// A merge read after a run passed is recorded once beside the passed run,
// whose status does not move. A run that has not passed, an end that is not
// a merge, a second record, and a run whose own move already wrote one are
// each refused, and none writes a row; the first write is the control.
func TestAMergeOfAPassedRunIsRecordedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	running := runningRun(t, s)
	read := PullRequestRead{PullRequest: 7, Head: "0123abc", End: PullRequestMerged}

	var wrong *RunStatusError
	if _, err := s.RecordMergeOfPassedRun(ctx, running.ID, read); !errors.As(err, &wrong) || wrong.Actual != RunRunning {
		t.Fatalf("a running run = %v, want a *RunStatusError", err)
	}
	if _, err := s.TransitionRun(ctx, running.ID, []RunStatus{RunRunning}, RunPassed); err != nil {
		t.Fatal(err)
	}
	closedRead := read
	closedRead.End = PullRequestClosed
	if _, err := s.RecordMergeOfPassedRun(ctx, running.ID, closedRead); !errors.Is(err, ErrUnknownPullRequestEnd) {
		t.Fatalf("a closed read of a passed run = %v, want refused", err)
	}
	if _, found, _ := s.MergeRecord(ctx, running.ID); found {
		t.Fatal("a refused write left a merge record")
	}

	record, err := s.RecordMergeOfPassedRun(ctx, running.ID, read)
	if err != nil || record.End != PullRequestMerged || record.PullRequest != 7 || record.Head != "0123abc" {
		t.Fatalf("RecordMergeOfPassedRun = %+v, %v", record, err)
	}
	if again, err := s.Run(ctx, running.ID); err != nil || again.Status != RunPassed {
		t.Fatalf("the run moved to %s (%v)", again.Status, err)
	}
	if _, err := s.RecordMergeOfPassedRun(ctx, running.ID, read); !errors.Is(err, ErrMergeRecorded) {
		t.Fatalf("a second record = %v, want ErrMergeRecorded", err)
	}

	other := runningRun2(t, s)
	if _, err := s.TransitionRunOnPullRequest(ctx, other.ID, []RunStatus{RunRunning}, RunPassed, read, HoldClosure{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordMergeOfPassedRun(ctx, other.ID, read); !errors.Is(err, ErrMergeRecorded) {
		t.Fatalf("a run its own move recorded = %v, want ErrMergeRecorded", err)
	}
}

// runningRun2 is a second running run beside runningRun's.
func runningRun2(t *testing.T, s *Store) Run {
	t.Helper()
	ctx := context.Background()
	r := completeRun()
	r.ID = "run-second"
	created, err := s.CreateRun(ctx, r)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	running, err := s.TransitionRun(ctx, created.ID, []RunStatus{RunPending}, RunRunning)
	if err != nil {
		t.Fatal(err)
	}
	return running
}
