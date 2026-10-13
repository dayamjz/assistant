package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// A new task is created with its opening event, its disposition, and nothing
// else: the store holds no state for it, so the history is all there is to
// read here. seedTask creates an independent task, so the disposition is a
// dispatch.
func TestANewTaskOpensItsHistory(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab")

	events, err := s.TaskEvents(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	if len(events) != 2 || events[0].Sequence != 1 || events[0].Kind != OpeningEventKind ||
		events[1].Sequence != 2 || events[1].Kind != DispatchedEventKind {
		t.Fatalf("a new task's history is %+v, want the opening event and the dispatch", events)
	}
	if task.RepositoryID.IsKnown() || task.Branch.IsKnown() {
		t.Fatalf("a task created without a branch reports one: %+v", task)
	}
}

func TestTaskEventsPaginateInAppendOrder(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab")

	for i := 3; i <= 6; i++ {
		e, err := s.AppendTaskEvent(ctx, task.ID, fmt.Sprintf("kind-%d", i), "")
		if err != nil {
			t.Fatalf("AppendTaskEvent: %v", err)
		}
		if e.Sequence != int64(i) {
			t.Fatalf("event %d was given sequence %d", i, e.Sequence)
		}
	}
	page, err := s.TaskEvents(ctx, task.ID, 3, 2)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	if len(page) != 2 || page[0].Sequence != 4 || page[1].Sequence != 5 {
		t.Fatalf("the page is %+v", page)
	}
	rest, err := s.TaskEvents(ctx, task.ID, 5, 0)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	if len(rest) != 1 || rest[0].Sequence != 6 {
		t.Fatalf("the rest is %+v", rest)
	}
}

func TestTaskAccessorsRefuseIncompleteRecords(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	complete := Task{ID: "taskaaaaaaab", Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/task-1"}
	cases := map[string]func(Task) Task{
		"no identifier":               func(t Task) Task { t.ID = ""; return t },
		"unknown shape":               func(t Task) Task { t.Shape = "something"; return t },
		"no shape":                    func(t Task) Task { t.Shape = ""; return t },
		"no copy path":                func(t Task) Task { t.WorktreePath = " "; return t },
		"branch without a repository": func(t Task) Task { t.Branch = Known("topic"); return t },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.CreateTask(ctx, mutate(complete), Disposition{}); err == nil {
				t.Fatal("CreateTask accepted an incomplete task")
			}
		})
	}
	if _, err := s.CreateTask(ctx, complete, Disposition{}); err != nil {
		t.Fatalf("CreateTask refused a complete task: %v", err)
	}
	// A repository alone is an investigation task working in one with no
	// branch of its own (PRD section 6), so it is a complete record.
	alone := complete
	alone.ID, alone.Shape, alone.RepositoryID = "taskaaaaaaac", TaskInvestigation, Known("repo-1")
	if _, err := s.CreateTask(ctx, alone, Disposition{}); err != nil {
		t.Fatalf("CreateTask refused a task naming a repository alone: %v", err)
	}
	if _, err := s.AppendTaskEvent(ctx, "taskaaaaaaab", " ", ""); err == nil {
		t.Fatal("AppendTaskEvent accepted an event with no kind")
	}
	if _, err := s.AppendTaskEvent(ctx, "nosuchtaskaa", "started", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AppendTaskEvent on a missing task = %v, want ErrNotFound", err)
	}
}

func TestTaskLinksStartUnknown(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	task := seedTask(t, s, "taskaaaaaaab")

	if task.RunID.IsKnown() || task.PullRequest.IsKnown() || task.BlockerTask.IsKnown() {
		t.Fatalf("a new task reports links it does not have: %+v", task)
	}
	if err := s.SetTaskRun(ctx, task.ID, run.ID); err != nil {
		t.Fatalf("SetTaskRun: %v", err)
	}
	if err := s.SetTaskPullRequest(ctx, task.ID, "pr-7"); err != nil {
		t.Fatalf("SetTaskPullRequest: %v", err)
	}
	got, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if id, known := got.RunID.Get(); !known || id != run.ID {
		t.Fatalf("the run link came back as %q (known=%v)", id, known)
	}
	if err := s.SetTaskRun(ctx, "nosuchtaskaa", run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetTaskRun on a missing task: %v", err)
	}
}

// Concurrent writers must not lose a write or hand two of them the same
// position. Run under the race detector, which `make test` does.
func TestConcurrentWritersKeepEveryWrite(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab")

	const writers = 8
	const each = 12

	var wg sync.WaitGroup
	sequences := make(chan int64, writers*each)
	failures := make(chan error, writers*each)

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				e, err := s.AppendTaskEvent(ctx, task.ID, fmt.Sprintf("w%d-%d", w, i), "")
				if err != nil {
					failures <- err
					continue
				}
				sequences <- e.Sequence
			}
		}(w)
	}
	wg.Wait()
	close(sequences)
	close(failures)

	for err := range failures {
		t.Fatalf("a concurrent write failed: %v", err)
	}

	total := writers * each
	// CreateTask appended the opening event and the dispatch as 1 and 2, so
	// the concurrent appends occupy 3 through total+2.
	assertDense(t, "event sequences", sequences, total, 3)

	events, err := s.TaskEvents(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	if len(events) != total+2 {
		t.Fatalf("%d events were appended but %d are stored, so a write was lost", total+2, len(events))
	}
}

// assertDense checks that a channel of positions holds exactly the run of
// integers from first, with no duplicates and no gaps. A lost write shows up as
// a gap; two writers handed the same position show up as a duplicate.
func assertDense(t *testing.T, what string, got <-chan int64, count int, first int64) {
	t.Helper()
	seen := make(map[int64]bool, count)
	for n := range got {
		if seen[n] {
			t.Fatalf("%s: %d was handed out twice", what, n)
		}
		seen[n] = true
	}
	if len(seen) != count {
		t.Fatalf("%s: got %d distinct values, want %d", what, len(seen), count)
	}
	for n := first; n < first+int64(count); n++ {
		if !seen[n] {
			t.Fatalf("%s: %d is missing, so a write was lost or a position was skipped", what, n)
		}
	}
}

// An anchored append is accepted while the history stands where the caller
// saw it and refused once it has moved, with a *TaskMovedError naming where it
// stands. An anchor of zero names a position no history stands at, because
// every task opens with an event.
func TestAnAnchoredAppendIsRefusedOnceTheHistoryMoved(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab") // opening event 1, dispatched 2

	e, err := s.AppendTaskEventAfter(ctx, task.ID, 2, "started", "")
	if err != nil || e.Sequence != 3 {
		t.Fatalf("an append anchored to the tip = %+v, %v", e, err)
	}
	_, err = s.AppendTaskEventAfter(ctx, task.ID, 2, "started", "")
	var moved *TaskMovedError
	if !errors.As(err, &moved) || !errors.Is(err, ErrTaskMoved) {
		t.Fatalf("an append anchored to a superseded tip = %v, want *TaskMovedError", err)
	}
	if moved.Task != task.ID || moved.Anchor != AnchoredEvent || moved.Expected != 2 || moved.Stands != 3 {
		t.Fatalf("the refusal names %+v, want task-1's events standing at 3 against an anchor of 2", moved)
	}
	if _, err := s.AppendTaskEventAfter(ctx, task.ID, 0, "started", ""); !errors.Is(err, ErrTaskMoved) {
		t.Fatalf("an append anchored to zero = %v, want ErrTaskMoved", err)
	}
	if _, err := s.AppendTaskEventAfter(ctx, "nosuchtaskaa", 0, "started", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an anchored append to a missing task = %v, want ErrNotFound", err)
	}
	events, err := s.TaskEvents(ctx, task.ID, 0, 0)
	if err != nil || len(events) != 3 {
		t.Fatalf("the refusals wrote something: %d events (%v)", len(events), err)
	}
}

// Two callers that read the same tip both append after it. Exactly one wins
// and the other is refused with ErrTaskMoved, because the anchor is decided
// in the transaction that assigns the sequence. The positive control is the
// unanchored append, where both land. Run under the race detector.
func TestTwoAnchoredAppendsAfterOneTipLandOne(t *testing.T) {
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab") // events 1 and 2

	wins, losses := raceTwo(t, func() error {
		_, err := s.AppendTaskEventAfter(ctx, task.ID, 2, "started", "")
		return err
	})
	if wins != 1 || losses != 1 {
		t.Fatalf("%d anchored appends won and %d were refused, want exactly one of each", wins, losses)
	}
	events, err := s.TaskEvents(ctx, task.ID, 0, 0)
	if err != nil || len(events) != 3 {
		t.Fatalf("after the race the history has %d events (%v), want 3", len(events), err)
	}

	control := seedTask(t, s, "taskaaaaaaac")
	wins, losses = raceTwo(t, func() error {
		_, err := s.AppendTaskEvent(ctx, control.ID, "started", "")
		return err
	})
	if wins != 2 || losses != 0 {
		t.Fatalf("%d unanchored appends won and %d were refused, want both to land", wins, losses)
	}
	events, err = s.TaskEvents(ctx, control.ID, 0, 0)
	if err != nil || len(events) != 4 {
		t.Fatalf("after the control race the history has %d events (%v), want 4", len(events), err)
	}
}
