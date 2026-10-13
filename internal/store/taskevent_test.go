package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// A new task is created with its opening event and nothing else: the store
// holds no state for it, so the history is all there is to read here.
func TestANewTaskOpensItsHistory(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "task-1")

	events, err := s.TaskEvents(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	if len(events) != 1 || events[0].Sequence != 1 || events[0].Kind != OpeningEventKind {
		t.Fatalf("a new task's history is %+v, want the single opening event", events)
	}
	if task.RepositoryID.IsKnown() || task.Branch.IsKnown() {
		t.Fatalf("a task created without a branch reports one: %+v", task)
	}
}

func TestTaskEventsPaginateInAppendOrder(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "task-1")

	for i := 2; i <= 6; i++ {
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

	complete := Task{ID: "task-1", Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/task-1"}
	cases := map[string]func(Task) Task{
		"no identifier":               func(t Task) Task { t.ID = ""; return t },
		"unknown shape":               func(t Task) Task { t.Shape = "something"; return t },
		"no shape":                    func(t Task) Task { t.Shape = ""; return t },
		"no copy path":                func(t Task) Task { t.WorktreePath = " "; return t },
		"branch without a repository": func(t Task) Task { t.Branch = Known("topic"); return t },
		"repository without a branch": func(t Task) Task { t.RepositoryID = Known("repo-1"); return t },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.CreateTask(ctx, mutate(complete)); err == nil {
				t.Fatal("CreateTask accepted an incomplete task")
			}
		})
	}
	if _, err := s.CreateTask(ctx, complete); err != nil {
		t.Fatalf("CreateTask refused a complete task: %v", err)
	}
	if _, err := s.AppendTaskEvent(ctx, "task-1", " ", ""); err == nil {
		t.Fatal("AppendTaskEvent accepted an event with no kind")
	}
}

func TestTaskLinksStartUnknown(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	task := seedTask(t, s, "task-1")

	if task.RunID.IsKnown() || task.PullRequest.IsKnown() || task.SessionRef.IsKnown() {
		t.Fatalf("a new task reports links it does not have: %+v", task)
	}
	if err := s.SetTaskRun(ctx, task.ID, run.ID); err != nil {
		t.Fatalf("SetTaskRun: %v", err)
	}
	if err := s.SetTaskSession(ctx, task.ID, "pane-3"); err != nil {
		t.Fatalf("SetTaskSession: %v", err)
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
	if err := s.SetTaskRun(ctx, "no-such-task", run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetTaskRun on a missing task: %v", err)
	}
}

// Concurrent writers must not lose a write or hand two of them the same
// position. Run under the race detector, which `make test` does.
func TestConcurrentWritersKeepEveryWrite(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "task-1")

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
	// The opening event CreateTask appended is sequence 1, so the concurrent
	// appends occupy 2 through total+1.
	assertDense(t, "event sequences", sequences, total, 2)

	events, err := s.TaskEvents(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	if len(events) != total+1 {
		t.Fatalf("%d events were appended but %d are stored, so a write was lost", total+1, len(events))
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
