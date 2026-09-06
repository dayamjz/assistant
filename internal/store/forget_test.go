package store

import (
	"context"
	"errors"
	"testing"
)

// Forgetting a repository takes everything recorded against its runs with it,
// so nothing is left pointing at a row that is gone.
func TestForgettingARepositoryTakesEverythingRecordedAgainstItsRuns(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	if _, err := s.UpsertStageResult(ctx, StageResult{RunID: run.ID, Stage: "review", Status: StagePassed}); err != nil {
		t.Fatalf("UpsertStageResult: %v", err)
	}
	if _, err := s.AppendRound(ctx, Round{RunID: run.ID, Stage: "review", Number: 1, Summary: "nothing found"}); err != nil {
		t.Fatalf("AppendRound: %v", err)
	}
	if _, err := s.RegisterHold(ctx, Hold{
		Key: "hold-1", RunID: Known(run.ID), Subject: "a decision", Detail: "about the change",
	}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if _, err := s.AppendGraphCheckpoint(ctx, run.ID, "", 0, []byte(`{"seq":1}`)); err != nil {
		t.Fatalf("AppendGraphCheckpoint: %v", err)
	}
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunPending}, RunTerminated); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}

	removed, err := s.ForgetRepository(ctx, run.RepositoryID)
	if err != nil {
		t.Fatalf("ForgetRepository: %v", err)
	}
	if removed != 1 {
		t.Fatalf("ForgetRepository removed %d runs, want 1", removed)
	}
	if _, err := s.Repository(ctx, run.RepositoryID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the repository reads back as %v, want ErrNotFound", err)
	}
	if _, err := s.Run(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the run reads back as %v, want ErrNotFound", err)
	}
	if _, err := s.Hold(ctx, "hold-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the hold reads back as %v, want ErrNotFound", err)
	}
	if history, err := s.GraphCheckpointHistory(ctx, run.ID); err != nil || len(history) != 0 {
		t.Fatalf("the checkpoint history reads back as (%d entries, %v), want none", len(history), err)
	}
	if results, err := s.StageResults(ctx, run.ID); err != nil || len(results) != 0 {
		t.Fatalf("the stage results read back as (%d, %v), want none", len(results), err)
	}
	if rounds, err := s.Rounds(ctx, run.ID); err != nil || len(rounds) != 0 {
		t.Fatalf("the rounds read back as (%d, %v), want none", len(rounds), err)
	}
}

// A run that has not finished is one a service may still be driving, and
// removing it would have that service writing against rows that are gone.
func TestForgettingARepositoryIsRefusedWhileARunHasNotFinished(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	if _, err := s.ForgetRepository(ctx, run.RepositoryID); !errors.Is(err, ErrRunActive) {
		t.Fatalf("ForgetRepository = %v, want ErrRunActive", err)
	}
	// Nothing was removed.
	if _, err := s.Run(ctx, run.ID); err != nil {
		t.Fatalf("the refused removal took the run anyway: %v", err)
	}
	if _, err := s.Repository(ctx, run.RepositoryID); err != nil {
		t.Fatalf("the refused removal took the repository anyway: %v", err)
	}
}

// A task with a life of its own must not be left pointing at a run that no
// longer exists, and which of the two goes is not this accessor's call.
func TestForgettingARepositoryIsRefusedWhileATaskNamesOneOfItsRuns(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunPending}, RunTerminated); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}
	if _, err := s.CreateTask(ctx, Task{
		ID: "task-1", Shape: TaskDelivery, Project: "one", Mode: "local", WorktreePath: "/copies/one",
		RunID: Known(run.ID),
	}, "queued", "test"); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if _, err := s.ForgetRepository(ctx, run.RepositoryID); !errors.Is(err, ErrRepositoryInUse) {
		t.Fatalf("ForgetRepository = %v, want ErrRepositoryInUse", err)
	}
	if _, err := s.Run(ctx, run.ID); err != nil {
		t.Fatalf("the refused removal took the run anyway: %v", err)
	}
}

// A repository that is not there has nothing left to remove, which is not an
// error.
func TestForgettingARepositoryThatIsNotThereIsNothingToDo(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	removed, err := s.ForgetRepository(ctx, "never-existed")
	if err != nil {
		t.Fatalf("ForgetRepository on an absent repository: %v", err)
	}
	if removed != 0 {
		t.Fatalf("ForgetRepository removed %d runs of a repository that is not there", removed)
	}
}

// Fleet work is listed newest first, and a home with none says so rather than
// failing.
func TestTasksAreListedNewestFirst(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	if tasks, err := s.Tasks(ctx); err != nil || len(tasks) != 0 {
		t.Fatalf("a fresh store lists (%d tasks, %v), want none", len(tasks), err)
	}
	for _, id := range []string{"task-1", "task-2"} {
		if _, err := s.CreateTask(ctx, Task{
			ID: id, Shape: TaskDelivery, Project: "one", Mode: "local", WorktreePath: "/copies/" + id,
		}, "queued", "test"); err != nil {
			t.Fatalf("CreateTask %s: %v", id, err)
		}
	}
	tasks, err := s.Tasks(ctx)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("Tasks returned %d, want 2", len(tasks))
	}
	for i := 1; i < len(tasks); i++ {
		if tasks[i-1].CreatedAt.Before(tasks[i].CreatedAt) {
			t.Fatalf("Tasks is not newest first: %v then %v", tasks[i-1].CreatedAt, tasks[i].CreatedAt)
		}
	}
}
