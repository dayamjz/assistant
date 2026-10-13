package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// An attempt is anchored to the highest attempt the caller saw. The first
// anchors to zero, the next to the first, and an anchor the attempts have
// moved past is refused with a *TaskMovedError that names where they stand.
func TestAttemptsAreAnchoredToTheLatestSeen(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab")

	if got, err := s.TaskAttempts(ctx, task.ID); err != nil || len(got) != 0 {
		t.Fatalf("a new task has attempts %v (%v)", got, err)
	}
	first, err := s.BeginTaskAttempt(ctx, task.ID, 0)
	if err != nil || first.Sequence != 1 || first.SessionRef.IsKnown() {
		t.Fatalf("the first attempt = %+v, %v", first, err)
	}
	second, err := s.BeginTaskAttempt(ctx, task.ID, first.Sequence)
	if err != nil || second.Sequence != 2 {
		t.Fatalf("the second attempt = %+v, %v", second, err)
	}

	_, err = s.BeginTaskAttempt(ctx, task.ID, first.Sequence)
	var moved *TaskMovedError
	if !errors.As(err, &moved) || !errors.Is(err, ErrTaskMoved) {
		t.Fatalf("an attempt anchored to a superseded one = %v, want *TaskMovedError", err)
	}
	if moved.Task != task.ID || moved.Anchor != AnchoredAttempt || moved.Expected != 1 || moved.Stands != 2 {
		t.Fatalf("the refusal names %+v, want task-1's attempts standing at 2 against an anchor of 1", moved)
	}
	if _, err := s.BeginTaskAttempt(ctx, task.ID, 0); !errors.Is(err, ErrTaskMoved) {
		t.Fatalf("a zero anchor on a task with attempts = %v, want ErrTaskMoved", err)
	}
	if _, err := s.BeginTaskAttempt(ctx, "nosuchtaskaa", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an attempt of a missing task = %v, want ErrNotFound", err)
	}

	got, err := s.TaskAttempts(ctx, task.ID)
	if err != nil || len(got) != 2 || got[0].Sequence != 1 || got[1].Sequence != 2 {
		t.Fatalf("TaskAttempts = %+v, %v", got, err)
	}
	if got[1].StartedAt.Before(got[0].StartedAt) {
		t.Fatalf("the second attempt started before the first: %+v", got)
	}
}

// Two callers that saw the same latest attempt both try to begin the next.
// Exactly one wins and the other is refused with ErrTaskMoved: the claim is
// decided in the transaction that assigns the sequence. Run under the race
// detector, which `make test` does.
func TestTwoCallersCannotBothBeginTheNextAttempt(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab")
	first, err := s.BeginTaskAttempt(ctx, task.ID, 0)
	if err != nil {
		t.Fatalf("BeginTaskAttempt: %v", err)
	}

	wins, losses := raceTwo(t, func() error {
		_, err := s.BeginTaskAttempt(ctx, task.ID, first.Sequence)
		return err
	})
	if wins != 1 || losses != 1 {
		t.Fatalf("%d callers won and %d were refused, want exactly one of each", wins, losses)
	}
	got, err := s.TaskAttempts(ctx, task.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("after the race the task has %d attempts (%v), want 2", len(got), err)
	}
}

// raceTwo runs two copies of write at once, both released by one gate, and
// counts the one that succeeded and the one refused with ErrTaskMoved. Any
// other failure fails the test.
func raceTwo(t *testing.T, write func() error) (wins, losses int) {
	t.Helper()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- write()
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrTaskMoved):
			losses++
		default:
			t.Fatalf("a racer failed for some other reason than the anchor: %v", err)
		}
	}
	return wins, losses
}

// The session is filled once, after the attempt exists: a second fill is
// refused and the first stands, an empty reference is refused, and an attempt
// that does not exist is not found.
func TestAnAttemptsSessionIsFilledOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab")
	a, err := s.BeginTaskAttempt(ctx, task.ID, 0)
	if err != nil {
		t.Fatalf("BeginTaskAttempt: %v", err)
	}

	if err := s.SetAttemptSession(ctx, task.ID, a.Sequence, " "); err == nil {
		t.Fatal("SetAttemptSession accepted an empty reference")
	}
	if err := s.SetAttemptSession(ctx, task.ID, a.Sequence+1, "pane-9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetAttemptSession on a missing attempt = %v, want ErrNotFound", err)
	}
	if err := s.SetAttemptSession(ctx, task.ID, a.Sequence, "pane-1"); err != nil {
		t.Fatalf("SetAttemptSession: %v", err)
	}
	if err := s.SetAttemptSession(ctx, task.ID, a.Sequence, "pane-2"); !errors.Is(err, ErrAttemptSessionRecorded) {
		t.Fatalf("a second fill = %v, want ErrAttemptSessionRecorded", err)
	}
	got, err := s.TaskAttempts(ctx, task.ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("TaskAttempts = %+v, %v", got, err)
	}
	if ref, ok := got[0].SessionRef.Get(); !ok || ref != "pane-1" {
		t.Fatalf("the attempt's session is %q (known=%v), want the first fill", ref, ok)
	}
}
