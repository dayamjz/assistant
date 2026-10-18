package store

import (
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// P6: a stale observer cannot change the next worker attempt's selection.
func TestTaskFallbackIsAnchoredAndWrittenOnce(t *testing.T) {
	principles.Cite(t, principles.P6)
	s := openStore(t)
	ctx := t.Context()
	task := seedTask(t, s, "taskaaaaaaab")
	if _, has, err := s.TaskFallback(ctx, task.ID); err != nil || has {
		t.Fatalf("initial fallback: %v %v", has, err)
	}
	a, err := s.BeginTaskAttempt(ctx, task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.BeginTaskAttempt(ctx, task.ID, a.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	f := TaskFallback{Primary: "claude", Backup: "cursor", Reason: "quota", Attempt: a.Sequence}
	if err = s.RecordTaskFallback(ctx, task.ID, f); !errors.Is(err, ErrTaskMoved) {
		t.Fatalf("stale handoff: %v", err)
	}
	f.Attempt = next.Sequence
	if err = s.RecordTaskFallback(ctx, task.ID, f); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordTaskFallback(ctx, task.ID, f); !errors.Is(err, ErrTaskFallbackRecorded) {
		t.Fatalf("second handoff: %v", err)
	}
	got, has, err := s.TaskFallback(ctx, task.ID)
	if err != nil || !has || got != f {
		t.Fatalf("fallback: %+v %v %v", got, has, err)
	}
	if _, err = s.write.ExecContext(ctx, `UPDATE task_agent_fallback SET backup_agent='codex' WHERE task_id=?`, task.ID); err == nil {
		t.Fatal("sealed fallback was changed")
	}
	if _, err = s.write.ExecContext(ctx, `DELETE FROM task_agent_fallback WHERE task_id=?`, task.ID); err == nil {
		t.Fatal("sealed fallback was deleted")
	}
}

func TestHandoffHoldCannotBlockANewerAttempt(t *testing.T) {
	s := openStore(t)
	ctx := t.Context()
	task := seedTask(t, s, "taskaaaaaaab")
	first, err := s.BeginTaskAttempt(ctx, task.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.BeginTaskAttempt(ctx, task.ID, first.Sequence); err != nil {
		t.Fatal(err)
	}
	h := Hold{Key: "task/" + task.ID + "/worker-fallback/1", Subject: "retry preserved work"}
	if _, err = s.RegisterTaskAttemptHold(ctx, task.ID, first.Sequence, h); !errors.Is(err, ErrTaskMoved) {
		t.Fatalf("stale hold: %v", err)
	}
	if _, err = s.Hold(ctx, h.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale hold was registered: %v", err)
	}
}
