package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// P8: the event log is not current state. Appending events changes nothing
// about what is true now, and the two records answer different questions.
func TestAppendingEventsDoesNotChangeState(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "task-1")

	opening, err := s.TaskState(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskState: %v", err)
	}
	if opening.State != "queued" || opening.Source != "intake" || opening.Revision != 1 {
		t.Fatalf("the opening state is %+v", opening)
	}

	// A worker appends notes that read like states. The authoritative record
	// is untouched by all of them.
	for _, kind := range []string{"started", "blocked", "waiting for review"} {
		if _, err := s.AppendTaskEvent(ctx, task.ID, kind, "worker said so"); err != nil {
			t.Fatalf("AppendTaskEvent: %v", err)
		}
	}
	after, err := s.TaskState(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskState: %v", err)
	}
	if after != opening {
		t.Fatalf("appending events changed the state from %+v to %+v", opening, after)
	}

	// Resolving the state is a separate call with its own source, and it moves
	// the revision so a reader can tell its copy is stale.
	resolved, err := s.SetTaskState(ctx, task.ID, "running", "supervisor", Known("stage review is live"))
	if err != nil {
		t.Fatalf("SetTaskState: %v", err)
	}
	if resolved.Revision != 2 {
		t.Fatalf("the second state has revision %d, want 2", resolved.Revision)
	}
	if detail, known := resolved.Detail.Get(); !known || detail != "stage review is live" {
		t.Fatalf("the detail came back as %q (known=%v)", detail, known)
	}

	// The history still says what it said. It was never the answer.
	events, err := s.TaskEvents(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[len(events)-1].Kind != "waiting for review" {
		t.Fatalf("the last event is %+v", events[len(events)-1])
	}
	current, err := s.TaskState(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskState: %v", err)
	}
	if current.State != "running" {
		t.Fatalf("the current state is %q, which is what the last event says rather than what was resolved", current.State)
	}
}

func TestTaskStateDetailIsUnknownWhenThereIsNone(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "task-1")

	st, err := s.TaskState(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskState: %v", err)
	}
	if st.Detail.IsKnown() {
		t.Fatalf("an opening state with no detail reported one: %q", st.Detail)
	}
	// An explicitly empty detail is a different fact from no detail at all.
	if _, err := s.SetTaskState(ctx, task.ID, "running", "supervisor", Known("")); err != nil {
		t.Fatalf("SetTaskState: %v", err)
	}
	st, err = s.TaskState(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskState: %v", err)
	}
	detail, known := st.Detail.Get()
	if !known || detail != "" {
		t.Fatalf("an explicitly empty detail read back as %q (known=%v)", detail, known)
	}
}

func TestTaskEventsPaginateInAppendOrder(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "task-1")

	for i := 1; i <= 5; i++ {
		e, err := s.AppendTaskEvent(ctx, task.ID, fmt.Sprintf("kind-%d", i), "")
		if err != nil {
			t.Fatalf("AppendTaskEvent: %v", err)
		}
		if e.Sequence != int64(i) {
			t.Fatalf("event %d was given sequence %d", i, e.Sequence)
		}
	}
	page, err := s.TaskEvents(ctx, task.ID, 2, 2)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	if len(page) != 2 || page[0].Sequence != 3 || page[1].Sequence != 4 {
		t.Fatalf("the page is %+v", page)
	}
	rest, err := s.TaskEvents(ctx, task.ID, 4, 0)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	if len(rest) != 1 || rest[0].Sequence != 5 {
		t.Fatalf("the rest is %+v", rest)
	}
}

func TestTaskAccessorsRefuseIncompleteRecords(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	complete := Task{ID: "task-1", Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/task-1"}
	cases := map[string]func(Task) Task{
		"no identifier": func(t Task) Task { t.ID = ""; return t },
		"unknown shape": func(t Task) Task { t.Shape = "something"; return t },
		"no shape":      func(t Task) Task { t.Shape = ""; return t },
		"no copy path":  func(t Task) Task { t.WorktreePath = " "; return t },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.CreateTask(ctx, mutate(complete), "queued", "intake"); err == nil {
				t.Fatal("CreateTask accepted an incomplete task")
			}
		})
	}
	if _, err := s.CreateTask(ctx, complete, "", "intake"); err == nil {
		t.Fatal("CreateTask accepted a task with no opening state")
	}
	if _, err := s.CreateTask(ctx, complete, "queued", ""); err == nil {
		t.Fatal("CreateTask accepted an opening state with no source")
	}
	if _, err := s.CreateTask(ctx, complete, "queued", "intake"); err != nil {
		t.Fatalf("CreateTask refused a complete task: %v", err)
	}
	if _, err := s.SetTaskState(ctx, "task-1", "running", "", Unknown[string]()); err == nil {
		t.Fatal("SetTaskState accepted a state with no source")
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
	revisions := make(chan int64, writers*each)
	failures := make(chan error, writers*each*2)

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

				st, err := s.SetTaskState(ctx, task.ID, fmt.Sprintf("state-w%d-%d", w, i), "supervisor", Unknown[string]())
				if err != nil {
					failures <- err
					continue
				}
				revisions <- st.Revision
			}
		}(w)
	}
	wg.Wait()
	close(sequences)
	close(revisions)
	close(failures)

	for err := range failures {
		t.Fatalf("a concurrent write failed: %v", err)
	}

	total := writers * each
	assertDense(t, "event sequences", sequences, total, 1)
	// The opening state written by CreateTask is revision 1, so the concurrent
	// writes occupy 2 through total+1.
	assertDense(t, "state revisions", revisions, total, 2)

	events, err := s.TaskEvents(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	if len(events) != total {
		t.Fatalf("%d events were appended but %d are stored, so a write was lost", total, len(events))
	}

	// Exactly one of the concurrent states is current, and it is the one that
	// holds the highest revision. The state record is still a single row.
	current, err := s.TaskState(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskState: %v", err)
	}
	if current.Revision != int64(total)+1 {
		t.Fatalf("the current state has revision %d, want %d", current.Revision, total+1)
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

// Concurrent checkpoint writers must not be handed the same revision either.
func TestConcurrentCheckpointWritersGetDistinctRevisions(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	const writers = 8
	const each = 8
	revisions := make(chan int64, writers*each)
	failures := make(chan error, writers*each)

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				c, err := s.WriteCheckpoint(ctx, Checkpoint{
					RunID: run.ID, State: []byte(fmt.Sprintf("w%d-%d", w, i)), Position: "review",
				})
				if err != nil {
					failures <- err
					continue
				}
				revisions <- c.Revision
			}
		}(w)
	}
	wg.Wait()
	close(revisions)
	close(failures)

	for err := range failures {
		t.Fatalf("a concurrent checkpoint write failed: %v", err)
	}
	assertDense(t, "checkpoint revisions", revisions, writers*each, 1)
}
