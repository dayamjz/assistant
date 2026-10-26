package store

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
)

// seedInvestigation records an investigation task naming a repository and
// no branch, the shape promotion starts from.
func seedInvestigation(t *testing.T, s *Store, id string) Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), Task{
		ID: id, Shape: TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: "/tasks/" + id + "/copy",
		RepositoryID: Known("repo-1"),
	}, Disposition{})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// historyOf reads a task's whole history, for accounting over what was
// written and never for its state.
func historyOf(t *testing.T, s *Store, id string) []TaskEvent {
	t.Helper()
	events, err := s.TaskEvents(context.Background(), id, 0, 0)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	return events
}

// The shape transition writes the shape, the repository, the branch, the
// delivery posture and the delivery intent together with the event that
// records it, in one transaction, and every refusal leaves all six as they
// were. PRD principle P14 is what the test
// claims: the task row is the one owner of the four facts, and the flip is
// one write of that row rather than three writes a reader could see in
// between, which is checked by holding the row and the history to their
// prior values after each refusal and to their new values after the one
// write.
func TestTheShapeTransitionWritesShapeRepositoryAndBranchTogether(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)
	task := seedInvestigation(t, s, "taskaaaaaaab")
	before := historyOf(t, s, task.ID)
	unchanged := func(t *testing.T, when string) {
		t.Helper()
		got, err := s.Task(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Shape != TaskInvestigation || got.Branch.IsKnown() || got.RepositoryID.Or("") != "repo-1" ||
			got.DeliveryIntent.IsKnown() {
			t.Fatalf("%s: the task reads %+v, want the investigation task as seeded", when, got)
		}
		if after := historyOf(t, s, task.ID); len(after) != len(before) {
			t.Fatalf("%s: the history grew from %d to %d entries", when, len(before), len(after))
		}
	}

	t.Run("the reverse move, an empty repository, branch or intent refuse before anything is read", func(t *testing.T) {
		if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskDelivery, TaskInvestigation, "repo-1", "topic", config.DeliveryGated, "Deliver it.", 2, "d", ResolvedByMachineInterface(), nil); !errors.Is(err, ErrShapeTransition) {
			t.Fatalf("delivery to investigation = %v, want ErrShapeTransition", err)
		}
		if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskInvestigation, "repo-1", "topic", config.DeliveryGated, "Deliver it.", 2, "d", ResolvedByMachineInterface(), nil); !errors.Is(err, ErrShapeTransition) {
			t.Fatalf("investigation to investigation = %v, want ErrShapeTransition", err)
		}
		if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "", "topic", config.DeliveryGated, "Deliver it.", 2, "d", ResolvedByMachineInterface(), nil); err == nil {
			t.Fatal("an empty repository was accepted")
		}
		if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", " ", config.DeliveryGated, "Deliver it.", 2, "d", ResolvedByMachineInterface(), nil); err == nil {
			t.Fatal("an empty branch was accepted")
		}
		for _, blank := range []string{"", " \n\t"} {
			if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic", config.DeliveryGated, blank, 2, "d", ResolvedByMachineInterface(), nil); err == nil {
				t.Fatalf("the blank intent %q was accepted", blank)
			}
		}
		for _, posture := range []config.Delivery{"", "direct", "fast"} {
			if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic", posture, "Deliver it.", 2, "d", ResolvedByMachineInterface(), nil); !errors.Is(err, ErrUnknownDelivery) {
				t.Fatalf("posture %q = %v, want ErrUnknownDelivery", posture, err)
			}
		}
		if _, _, err := s.TransitionTaskShape(ctx, "nope", TaskInvestigation, TaskDelivery, "repo-1", "topic", config.DeliveryGated, "Deliver it.", 2, "d", ResolvedByMachineInterface(), nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a task that does not exist = %v, want ErrNotFound", err)
		}
		unchanged(t, "after the refusals")
	})

	t.Run("an anchor the history moved past refuses", func(t *testing.T) {
		// The seeded task has two entries, queued and dispatched; a caller
		// that read only the first is behind.
		_, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic", config.DeliveryGated, "Deliver it.", 1, "d", ResolvedByMachineInterface(), nil)
		var moved *TaskMovedError
		if !errors.As(err, &moved) || !errors.Is(err, ErrTaskMoved) {
			t.Fatalf("a stale anchor = %v, want a *TaskMovedError matching ErrTaskMoved", err)
		}
		if moved.Anchor != AnchoredEvent || moved.Expected != 1 || moved.Stands != 2 {
			t.Fatalf("the refusal names %+v, want the event anchor at 1 against 2", moved)
		}
		unchanged(t, "after the stale anchor")
	})

	t.Run("the one write flips the shape, records what it works under, and appends the event", func(t *testing.T) {
		got, event, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic", config.DeliveryLocal, "Deliver it.", 2, "promoted in the test", ResolvedByMachineInterface(), nil)
		if err != nil {
			t.Fatalf("TransitionTaskShape: %v", err)
		}
		if got.Shape != TaskDelivery || got.RepositoryID.Or("") != "repo-1" || got.Branch.Or("") != "topic" ||
			got.Delivery != Known(config.DeliveryLocal) || got.DeliveryIntent != Known("Deliver it.") {
			t.Fatalf("the task reads %+v, want delivery on repo-1 topic under local, to deliver it", got)
		}
		if event.Sequence != 3 || event.Kind != PromotedEventKind || event.Detail != "promoted in the test" || event.At.IsZero() {
			t.Fatalf("the event is %+v, want promoted at sequence 3 with the detail", event)
		}
		history := historyOf(t, s, task.ID)
		if len(history) != 3 || history[2].Kind != PromotedEventKind || history[2].Detail != "promoted in the test" {
			t.Fatalf("the history is %+v, want the promoted entry third", history)
		}
		read, err := s.Task(ctx, task.ID)
		if err != nil || read.Shape != TaskDelivery || read.Branch.Or("") != "topic" || read.Delivery != Known(config.DeliveryLocal) ||
			read.DeliveryIntent != Known("Deliver it.") {
			t.Fatalf("Task = %+v, %v; want the flip read back", read, err)
		}
	})

	t.Run("a second transition of the same task refuses as moved", func(t *testing.T) {
		_, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "other", config.DeliveryGated, "Deliver something else.", 3, "d", ResolvedByMachineInterface(), nil)
		if !errors.Is(err, ErrTaskShapeMoved) {
			t.Fatalf("a second promotion = %v, want ErrTaskShapeMoved", err)
		}
		read, err := s.Task(ctx, task.ID)
		if err != nil || read.Branch.Or("") != "topic" || read.Delivery != Known(config.DeliveryLocal) ||
			read.DeliveryIntent != Known("Deliver it.") {
			t.Fatalf("the second promotion moved the branch, the posture or the intent: %+v, %v", read, err)
		}
		if history := historyOf(t, s, task.ID); len(history) != 3 {
			t.Fatalf("the refused transition appended: %+v", history)
		}
	})
}

// A task whose copy a cleanup has claimed is not promoted, and a cleanup of
// a task that was promoted is not claimed: the two writes read each other's
// row in their own transactions on the one writer connection, so in either
// order the second refuses.
func TestTheShapeTransitionAndTheCleanupClaimExcludeEachOther(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)

	t.Run("a cleanup record refuses the transition", func(t *testing.T) {
		task := seedInvestigation(t, s, "taskaaaaaaac")
		if _, err := s.RecordTaskCleanup(ctx, TaskCleanup{TaskID: task.ID, Proof: CleanupProofScratch}, TaskInvestigation); err != nil {
			t.Fatalf("RecordTaskCleanup: %v", err)
		}
		_, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic", config.DeliveryGated, "Deliver it.", 2, "d", ResolvedByMachineInterface(), nil)
		if !errors.Is(err, ErrCleanupRecorded) {
			t.Fatalf("promoting a claimed task = %v, want ErrCleanupRecorded", err)
		}
		read, err := s.Task(ctx, task.ID)
		if err != nil || read.Shape != TaskInvestigation || read.Branch.IsKnown() {
			t.Fatalf("the refused transition wrote: %+v, %v", read, err)
		}
		if history := historyOf(t, s, task.ID); len(history) != 2 {
			t.Fatalf("the refused transition appended: %+v", history)
		}
	})

	t.Run("the transition refuses a cleanup decided over the old shape", func(t *testing.T) {
		task := seedInvestigation(t, s, "taskaaaaaaad")
		if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic", config.DeliveryGated, "Deliver it.", 2, "d", ResolvedByMachineInterface(), nil); err != nil {
			t.Fatalf("TransitionTaskShape: %v", err)
		}
		_, err := s.RecordTaskCleanup(ctx, TaskCleanup{TaskID: task.ID, Proof: CleanupProofScratch}, TaskInvestigation)
		if !errors.Is(err, ErrTaskShapeMoved) {
			t.Fatalf("claiming a promoted task on the old shape = %v, want ErrTaskShapeMoved", err)
		}
		if _, err := s.TaskCleanup(ctx, task.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("the refused claim left a record: %v", err)
		}
	})
}

// The flip answers its copy's open kept work only for entries the caller
// verified the copy for: with an open entry it was not given, the flip is
// refused with ErrKeptWorkUnverified and nothing is written, the task an
// investigation, its history as it was and the entry open; given that
// entry, the flip answers it carry on naming the promoted task.
func TestTheFlipAnswersOnlyKeptWorkTheCallerVerified(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	ctx := context.Background()
	s := openStore(t)
	seedRun(t, s) // records repo-1, which a kept work entry's project is read off
	task := seedInvestigation(t, s, "taskaaaaaavk")
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{TaskID: task.ID, CopyPath: task.WorktreePath,
		Holds: []string{"cccc"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatalf("RecordKeptWork: %v", err)
	}
	before := historyOf(t, s, task.ID)
	anchor := before[len(before)-1].Sequence
	flip := func(verified []int64) (Task, error) {
		got, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic",
			config.DeliveryGated, "Deliver it.", anchor, "promoted in the test", ResolvedByMachineInterface(), verified)
		return got, err
	}

	for _, verified := range [][]int64{nil, {entry.ID + 1}} {
		if _, err := flip(verified); !errors.Is(err, ErrKeptWorkUnverified) {
			t.Fatalf("the flip verified for %v = %v; want ErrKeptWorkUnverified", verified, err)
		}
		if got, err := s.Task(ctx, task.ID); err != nil || got.Shape != TaskInvestigation {
			t.Fatalf("after the refused flip the task reads %+v, %v; want the investigation", got, err)
		}
		if after := historyOf(t, s, task.ID); len(after) != len(before) {
			t.Fatalf("the refused flip grew the history from %d to %d entries", len(before), len(after))
		}
		if still, err := s.KeptWorkEntry(ctx, entry.ID); err != nil || !still.Open() {
			t.Fatalf("after the refused flip the entry reads %+v, %v; want it open", still, err)
		}
	}

	if got, err := flip([]int64{entry.ID}); err != nil || got.Shape != TaskDelivery {
		t.Fatalf("the flip verified for the entry = %+v, %v; want the delivery task", got, err)
	}
	if closed, err := s.KeptWorkEntry(ctx, entry.ID); err != nil || closed.Open() || closed.Answer.Or("") != KeptCarryOn ||
		closed.CarriedOnTask.Or("") != task.ID {
		t.Fatalf("after the flip the entry reads %+v, %v; want it answered carry on by the promoted task", closed, err)
	}
}
