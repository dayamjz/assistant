package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestReopeningKeepsEverything(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	s := openStoreAt(t, path)
	run := seedRun(t, s)
	task := seedTask(t, s, "task-1")
	if _, err := s.SetTaskState(ctx, task.ID, "running", "supervisor", Known("stage review")); err != nil {
		t.Fatalf("SetTaskState: %v", err)
	}
	if _, err := s.RegisterHold(ctx, Hold{Key: "k", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again := openStoreAt(t, path)
	if got, err := again.Run(ctx, run.ID); err != nil || got.Build != run.Build {
		t.Fatalf("the run did not survive reopening: %+v, %v", got, err)
	}
	st, err := again.TaskState(ctx, task.ID)
	if err != nil {
		t.Fatalf("TaskState: %v", err)
	}
	if st.State != "running" || st.Revision != 2 {
		t.Fatalf("the task state did not survive reopening: %+v", st)
	}
	open, err := again.OpenHolds(ctx)
	if err != nil {
		t.Fatalf("OpenHolds: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("%d holds survived reopening, want 1", len(open))
	}
}

func TestAccessorsRefuseAfterClose(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRun(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close is idempotent.
	if err := s.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}

	checks := map[string]func() error{
		"Repository":  func() error { _, err := s.Repository(ctx, "repo-1"); return err },
		"Run":         func() error { _, err := s.Run(ctx, "run-1"); return err },
		"CreateRun":   func() error { _, err := s.CreateRun(ctx, completeRun()); return err },
		"StageResult": func() error { _, err := s.StageResult(ctx, "run-1", "review"); return err },
		"Rounds":      func() error { _, err := s.Rounds(ctx, "run-1"); return err },
		"Checkpoint":  func() error { _, err := s.Checkpoint(ctx, "run-1"); return err },
		"TaskState":   func() error { _, err := s.TaskState(ctx, "task-1"); return err },
		"TaskEvents":  func() error { _, err := s.TaskEvents(ctx, "task-1", 0, 0); return err },
		"OpenHolds":   func() error { _, err := s.OpenHolds(ctx); return err },
		"AppendEvent": func() error { _, err := s.AppendTaskEvent(ctx, "task-1", "kind", ""); return err },
	}
	for name, check := range checks {
		if err := check(); !errors.Is(err, ErrClosed) {
			t.Fatalf("%s after Close returned %v, want ErrClosed", name, err)
		}
	}
}

func TestOpenRefusesAnEmptyPath(t *testing.T) {
	if _, err := Open(context.Background(), "", WithRedactor(workingRedactor())); err == nil {
		t.Fatal("Open accepted an empty path")
	}
}

func TestPathIsAbsolute(t *testing.T) {
	s := openStore(t)
	if !filepath.IsAbs(s.Path()) {
		t.Fatalf("Path returned %q, which is not absolute", s.Path())
	}
}
