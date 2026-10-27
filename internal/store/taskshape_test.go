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
		if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskDelivery, TaskInvestigation, "repo-1", "topic", config.DeliveryGated, "Deliver it.", 2, "d"); !errors.Is(err, ErrShapeTransition) {
			t.Fatalf("delivery to investigation = %v, want ErrShapeTransition", err)
		}
		if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskInvestigation, "repo-1", "topic", config.DeliveryGated, "Deliver it.", 2, "d"); !errors.Is(err, ErrShapeTransition) {
			t.Fatalf("investigation to investigation = %v, want ErrShapeTransition", err)
		}
		if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "", "topic", config.DeliveryGated, "Deliver it.", 2, "d"); err == nil {
			t.Fatal("an empty repository was accepted")
		}
		if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", " ", config.DeliveryGated, "Deliver it.", 2, "d"); err == nil {
			t.Fatal("an empty branch was accepted")
		}
		for _, blank := range []string{"", " \n\t"} {
			if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic", config.DeliveryGated, blank, 2, "d"); err == nil {
				t.Fatalf("the blank intent %q was accepted", blank)
			}
		}
		for _, posture := range []config.Delivery{"", "direct", "fast"} {
			if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic", posture, "Deliver it.", 2, "d"); !errors.Is(err, ErrUnknownDelivery) {
				t.Fatalf("posture %q = %v, want ErrUnknownDelivery", posture, err)
			}
		}
		if _, _, err := s.TransitionTaskShape(ctx, "nope", TaskInvestigation, TaskDelivery, "repo-1", "topic", config.DeliveryGated, "Deliver it.", 2, "d"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a task that does not exist = %v, want ErrNotFound", err)
		}
		unchanged(t, "after the refusals")
	})

	t.Run("an anchor the history moved past refuses", func(t *testing.T) {
		// The seeded task has two entries, queued and dispatched; a caller
		// that read only the first is behind.
		_, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic", config.DeliveryGated, "Deliver it.", 1, "d")
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
		got, event, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic", config.DeliveryLocal, "Deliver it.", 2, "promoted in the test")
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
		_, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "other", config.DeliveryGated, "Deliver something else.", 3, "d")
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
		_, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic", config.DeliveryGated, "Deliver it.", 2, "d")
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
		if _, _, err := s.TransitionTaskShape(ctx, task.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic", config.DeliveryGated, "Deliver it.", 2, "d"); err != nil {
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

// A retired housekeeping pass is never promoted: the transition reads the
// retirement record in the transaction that would flip the shape and
// refuses with ErrPassRetired, writing nothing, because every attempt
// claim refuses a retired pass, so the delivery it became could never
// begin. The positive control is the same transition of a pass not
// retired, which flips.
func TestTheShapeTransitionRefusesARetiredPass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	flip := func(id string) error {
		history := historyOf(t, s, id)
		_, _, err := s.TransitionTaskShape(ctx, id, TaskInvestigation, TaskDelivery, "repo-1", "housekeeping/"+id,
			config.DeliveryGated, "Correct the drift.", history[len(history)-1].Sequence, "promoted in the test")
		return err
	}
	mustPass(t, s, "taskhkrtirea")
	attempt := beginAttempt(t, s, "taskhkrtirea", 0)
	retire := registerRetireHold(t, s, "taskhkrtirea")
	if _, err := s.RetireHousekeepingPass(ctx, Retirement{Task: "taskhkrtirea", Attempt: attempt.Sequence, Hold: retire,
		By: ResolvedByMachineInterface(), Relaunches: func(string) bool { return false }, Ended: ended, Delivered: relayed}); err != nil {
		t.Fatalf("retiring the pass: %v", err)
	}
	before := historyOf(t, s, "taskhkrtirea")
	if err := flip("taskhkrtirea"); !errors.Is(err, ErrPassRetired) {
		t.Fatalf("promoting the retired pass = %v, want ErrPassRetired", err)
	}
	got, err := s.Task(ctx, "taskhkrtirea")
	if err != nil || got.Shape != TaskInvestigation || got.Branch.IsKnown() || got.Delivery.IsKnown() || got.DeliveryIntent.IsKnown() {
		t.Fatalf("the refused promotion wrote the record: %+v, %v", got, err)
	}
	if after := historyOf(t, s, "taskhkrtirea"); len(after) != len(before) {
		t.Fatalf("the refused promotion appended: %+v", after)
	}
	mustPass(t, s, "taskhkrtireb")
	if err := flip("taskhkrtireb"); err != nil {
		t.Fatalf("promoting a pass not retired = %v, want it flipped", err)
	}
}
