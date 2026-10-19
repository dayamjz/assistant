package store

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
)

// A task is recorded with its disposition in one transaction: an independent
// task opens with queued and dispatched, a queued task with queued alone and
// its blocker named. The control for atomicity is a fault planted at the
// database on the second event: with it, nothing is recorded, not the task row
// and not the opening event, so a kill between the two writes cannot strand a
// task with no disposition.
func TestCreateTaskRecordsTheDispositionOrNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedRun(t, s) // repo-1

	blocker := createBranchTask(t, s, "blockeraaaaa", Disposition{})
	events, err := s.TaskEvents(ctx, blocker.ID, 0, 0)
	if err != nil || len(events) != 2 || events[0].Kind != OpeningEventKind || events[1].Kind != DispatchedEventKind {
		t.Fatalf("an independent task's history is %+v (%v), want queued then dispatched", events, err)
	}
	if blocker.BlockerTask.IsKnown() {
		t.Fatalf("an independent task names a blocker: %+v", blocker)
	}

	queued := createBranchTask(t, s, "queuedaaaaaa", Disposition{Blocker: Known(blocker.ID), Observed: blocker.Row})
	events, err = s.TaskEvents(ctx, queued.ID, 0, 0)
	if err != nil || len(events) != 1 || events[0].Kind != OpeningEventKind {
		t.Fatalf("a queued task's history is %+v (%v), want queued alone", events, err)
	}
	if got := queued.BlockerTask.Or(""); got != blocker.ID {
		t.Fatalf("the queued task names blocker %q, want %s", got, blocker.ID)
	}

	// The fault: the dispatch event cannot be written. The task row and the
	// opening event were written before it in the same transaction, and both
	// must be gone.
	if _, err := s.write.ExecContext(ctx, `
		CREATE TRIGGER fault BEFORE INSERT ON task_event WHEN NEW.kind = 'dispatched'
		BEGIN SELECT RAISE(ABORT, 'planted fault'); END`); err != nil {
		t.Fatalf("planting the fault: %v", err)
	}
	if _, err := s.CreateTask(ctx, Task{
		ID: "strandedaaaa", Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/stranded",
	}, Disposition{}); err == nil {
		t.Fatal("CreateTask succeeded past the planted fault, so the control shows nothing")
	}
	if _, err := s.Task(ctx, "strandedaaaa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the task row survived the failed dispatch: %v", err)
	}
	if events, err := s.TaskEvents(ctx, "strandedaaaa", 0, 0); err != nil || len(events) != 0 {
		t.Fatalf("the opening event survived the failed dispatch: %+v (%v)", events, err)
	}
}

// The disposition is anchored to the newest task of the branch the caller
// saw. A newer task on the branch means the decision was made without seeing
// it, so the creation is refused with a *TaskMovedError naming the row that
// stands, and nothing is written. The control is the same creation anchored
// to that row, which is accepted.
func TestCreateTaskIsRefusedWhenTheBranchMoved(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := openStore(t)
	seedRun(t, s)

	first := createBranchTask(t, s, "firstaaaaaaa", Disposition{})
	second := createBranchTask(t, s, "secondaaaaaa", Disposition{Blocker: Known(first.ID), Observed: first.Row})

	_, err := s.CreateTask(ctx, Task{
		ID: "staleaaaaaaa", Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/stale",
		RepositoryID: Known("repo-1"), Branch: Known("topic"),
	}, Disposition{Blocker: Known(first.ID), Observed: first.Row})
	var moved *TaskMovedError
	if !errors.As(err, &moved) || !errors.Is(err, ErrTaskMoved) {
		t.Fatalf("a creation anchored to a superseded task = %v, want *TaskMovedError", err)
	}
	if moved.Task != "staleaaaaaaa" || moved.Anchor != AnchoredBranch || moved.Expected != first.Row || moved.Stands != second.Row {
		t.Fatalf("the refusal names %+v, want the branch standing at %d against an anchor of %d", moved, second.Row, first.Row)
	}
	if _, err := s.Task(ctx, "staleaaaaaaa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refused creation wrote a task: %v", err)
	}
	if _, err := s.CreateTask(ctx, Task{
		ID: "staleaaaaaaa", Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/stale",
		RepositoryID: Known("repo-1"), Branch: Known("topic"),
	}, Disposition{Blocker: Known(second.ID), Observed: second.Row}); err != nil {
		t.Fatalf("the same creation anchored to the newest task was refused: %v", err)
	}

	// Another branch is another set: its newest is nothing, so the anchor is
	// zero, and the first branch's tasks do not count against it.
	if _, err := s.CreateTask(ctx, Task{
		ID: "elsewhereaaa", Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/elsewhere",
		RepositoryID: Known("repo-1"), Branch: Known("other"),
	}, Disposition{}); err != nil {
		t.Fatalf("a task on an empty branch anchored to zero was refused: %v", err)
	}
	// A task with no branch has no set to have observed, so an anchor on one
	// is a caller's mistake and not a move: it is refused, and not as
	// ErrTaskMoved.
	_, err = s.CreateTask(ctx, Task{
		ID: "looseaaaaaaa", Shape: TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/loose",
	}, Disposition{Observed: first.Row})
	if err == nil || errors.Is(err, ErrTaskMoved) {
		t.Fatalf("a branch-less task anchored to a row = %v, want a refusal that is not a move", err)
	}
	if _, err := s.Task(ctx, "looseaaaaaaa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refused creation wrote the task: %v", err)
	}
}

// Two intakes that read the same branch both create a task anchored to what
// they saw. Exactly one lands and the other is refused with ErrTaskMoved,
// because the anchor is decided in the transaction that inserts the row. Run
// under the race detector.
func TestTwoIntakesOnOneBranchCreateOneTask(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := openStore(t)
	seedRun(t, s)
	first := createBranchTask(t, s, "firstaaaaaaa", Disposition{})

	ids := []string{"racerraaaaab", "racerraaaaac"}
	var next atomic.Int32
	wins, losses := raceTwo(t, func() error {
		id := ids[next.Add(1)-1]
		_, err := s.CreateTask(ctx, Task{
			ID: id, Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/" + id,
			RepositoryID: Known("repo-1"), Branch: Known("topic"),
		}, Disposition{Blocker: Known(first.ID), Observed: first.Row})
		return err
	})
	if wins != 1 || losses != 1 {
		t.Fatalf("%d intakes landed and %d were refused, want exactly one of each", wins, losses)
	}
	tasks, err := s.Tasks(ctx)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("after the race the store holds %d tasks (%v), want the first and one racer", len(tasks), err)
	}
}

// The identifier is checked by internal/home's one validator, so a task is
// never recorded under a name its directory could not take.
func TestCreateTaskRefusesAnIdentifierTheHomeWouldRefuse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	for _, id := range []string{"", "task-1", "TASKAAAAAAAB", "short", "../aaaaaaaaa"} {
		_, err := s.CreateTask(ctx, Task{
			ID: id, Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/x",
		}, Disposition{})
		if !errors.Is(err, home.ErrTaskID) {
			t.Errorf("CreateTask(%q) = %v, want home.ErrTaskID", id, err)
		}
	}
}

// A blocker is a task that exists and is not the one being created.
func TestCreateTaskRefusesABlockerItCannotRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	base := Task{ID: "taskaaaaaaab", Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/worktrees/task-1"}
	if _, err := s.CreateTask(ctx, base, Disposition{Blocker: Known("taskaaaaaaab")}); err == nil {
		t.Fatal("a task was accepted as its own blocker")
	}
	if _, err := s.CreateTask(ctx, base, Disposition{Blocker: Known("neveraaaaaaa")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a blocker that does not exist = %v, want ErrNotFound", err)
	}
	if _, err := s.Task(ctx, base.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused creation wrote the task: %v", err)
	}
}
