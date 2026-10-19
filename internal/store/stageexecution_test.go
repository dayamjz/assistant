package store

import (
	"context"
	"testing"
	"time"
)

// Every execution of a stage keeps its own row, in the order it ended, and
// reads back with the start and end it was given, so a stage that ran twice
// has two durations rather than the last one.
func TestEveryStageExecutionKeepsItsOwnStartAndEnd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	for _, e := range []StageExecution{
		{Run: "run-1", Stage: "review", Started: at, Ended: at.Add(3 * time.Second)},
		{Run: "run-2", Stage: "review", Started: at, Ended: at.Add(time.Second)},
		{Run: "run-1", Stage: "review", Started: at.Add(time.Minute), Ended: at.Add(time.Minute + 1500*time.Millisecond)},
	} {
		if _, err := s.AppendStageExecution(ctx, e); err != nil {
			t.Fatalf("appending %+v: %v", e, err)
		}
	}
	got, err := s.StageExecutions(ctx, "run-1")
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("run-1 has %d executions, want its two: %+v", len(got), got)
	}
	if got[0].Duration() != 3*time.Second || got[1].Duration() != 1500*time.Millisecond {
		t.Fatalf("the durations read back as %v and %v", got[0].Duration(), got[1].Duration())
	}
	if !got[1].Started.Equal(at.Add(time.Minute)) {
		t.Fatalf("the second execution started at %v", got[1].Started)
	}
}

// An execution nobody observed is refused: no run, no stage, a missing time,
// or an end before its start. The positive control is the well-formed row.
func TestAStageExecutionNobodyObservedIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	at := time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)
	good := StageExecution{Run: "run-1", Stage: "test", Started: at, Ended: at}
	if _, err := s.AppendStageExecution(ctx, good); err != nil {
		t.Fatalf("a well-formed execution was refused: %v", err)
	}
	for name, e := range map[string]StageExecution{
		"no run":           {Stage: "test", Started: at, Ended: at},
		"no stage":         {Run: "run-1", Started: at, Ended: at},
		"no start":         {Run: "run-1", Stage: "test", Ended: at},
		"no end":           {Run: "run-1", Stage: "test", Started: at},
		"end before start": {Run: "run-1", Stage: "test", Started: at, Ended: at.Add(-time.Second)},
	} {
		if _, err := s.AppendStageExecution(ctx, e); err == nil {
			t.Errorf("an execution with %s was accepted", name)
		}
	}
}
