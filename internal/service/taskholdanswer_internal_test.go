package service

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
)

// taskHoldToAnswer resolves a named hold by finding id or full key, and
// refuses a closed, foreign, or missing hold as an invalid request rather
// than answering the oldest in its place. The happy path of answering by
// name is the CLI walk in taskhold_test.go; this locks the resolver's edges
// without another end-to-end walk.
func TestTaskHoldToAnswerNamesTheHoldAndRefusesTheEdges(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	database := filepath.Join(t.TempDir(), "state.db")
	seedDatabase(t, database)
	st, err := store.Open(ctx, database, store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Service{store: st}

	const (
		taskA    = "taskaaaaaa23"
		taskB    = "taskbbbbbb23"
		finding1 = "finding-1"
		finding2 = "finding-2"
	)
	for _, id := range []string{taskA, taskB} {
		if _, err := st.CreateTask(ctx, store.Task{
			ID: id, Shape: store.TaskInvestigation, Project: "one", Mode: "headless",
			WorktreePath: "/worktrees/" + id,
		}, store.Disposition{}); err != nil {
			t.Fatal(err)
		}
	}
	keyA1 := tasks.HoldKey(taskA, finding1)
	keyA2 := tasks.HoldKey(taskA, finding2)
	keyB1 := tasks.HoldKey(taskB, finding1)
	for _, h := range []store.Hold{
		{Key: keyA1, TaskID: store.Known(taskA), Subject: "first of A", Detail: "first of A"},
		{Key: keyA2, TaskID: store.Known(taskA), Subject: "second of A", Detail: "second of A"},
		{Key: keyB1, TaskID: store.Known(taskB), Subject: "first of B", Detail: "first of B"},
	} {
		if _, err := st.RegisterHold(ctx, h); err != nil {
			t.Fatal(err)
		}
	}

	byFinding, err := s.taskHoldToAnswer(ctx, taskA, finding2)
	if err != nil || byFinding.Key != keyA2 {
		t.Fatalf("finding id answered %+v (%v), want %s", byFinding, err, keyA2)
	}
	byKey, err := s.taskHoldToAnswer(ctx, taskA, keyA2)
	if err != nil || byKey.Key != keyA2 {
		t.Fatalf("full key answered %+v (%v), want %s", byKey, err, keyA2)
	}
	oldest, err := s.taskHoldToAnswer(ctx, taskA, "")
	if err != nil || oldest.Key != keyA1 {
		t.Fatalf("an empty name answered %+v (%v), want the oldest %s", oldest, err, keyA1)
	}

	if _, err := st.ResolveHold(ctx, keyA2, "approved", store.ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.taskHoldToAnswer(ctx, taskA, finding2); !errors.Is(err, ipc.ErrInvalidRequest) {
		t.Fatalf("a closed hold answered %v, want an invalid request", err)
	}
	if _, err := s.taskHoldToAnswer(ctx, taskA, keyB1); !errors.Is(err, ipc.ErrInvalidRequest) {
		t.Fatalf("a foreign-task key answered %v, want an invalid request", err)
	}
	if _, err := s.taskHoldToAnswer(ctx, taskA, "missing-finding"); !errors.Is(err, ipc.ErrInvalidRequest) {
		t.Fatalf("a missing hold answered %v, want an invalid request", err)
	}
}
