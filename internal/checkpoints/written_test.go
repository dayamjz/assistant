package checkpoints_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/checkpoints"
	"github.com/dayamjz/assistant/internal/graph"
)

// LatestWritten answers with the checkpoint Latest answers with and the time
// internal/store recorded the entry at, which falls between the moments
// around the write that appended it; a second append moves both. A run with
// no history is ErrNoSuchRun, as Latest says. It is the durable store's alone
// because it reads a column only that store keeps.
func TestLatestWrittenIsTheLatestEntryAndWhenItWasAppended(t *testing.T) {
	ctx := context.Background()
	s := checkpoints.New(openRecords(t))
	g := linearGraph(t, &recorder{})
	if _, _, err := s.LatestWritten(ctx, "run"); !errors.Is(err, graph.ErrNoSuchRun) {
		t.Fatalf("LatestWritten of an unknown run = %v, want ErrNoSuchRun", err)
	}
	write := func(anchor graph.CheckpointID, position string) (graph.CheckpointID, time.Time, time.Time) {
		before := time.Now()
		id, err := s.Write(ctx, anchor, graph.Checkpoint{
			Run: "run", Position: position, Status: graph.StatusRunning, State: mustState(t, g, nil),
			Counters: graph.Counters{Budget: 9, Traversals: []int{0}, Fingerprints: []string{""}, EdgeDigest: "d"},
		})
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		return id, before, time.Now()
	}
	first, from, to := write(graph.CheckpointID{}, "a")
	got, at, err := s.LatestWritten(ctx, "run")
	if err != nil {
		t.Fatalf("LatestWritten: %v", err)
	}
	if got.ID() != first || got.Position != "a" {
		t.Fatalf("LatestWritten answered %s at %q, want %s at a", got.ID(), got.Position, first)
	}
	if at.Before(from.Add(-time.Second)) || at.After(to.Add(time.Second)) {
		t.Fatalf("the entry was recorded at %s, outside the write between %s and %s", at, from, to)
	}
	second, _, _ := write(first, "b")
	got, later, err := s.LatestWritten(ctx, "run")
	if err != nil {
		t.Fatalf("LatestWritten: %v", err)
	}
	if got.ID() != second || got.Position != "b" || later.Before(at) {
		t.Fatalf("after a second append LatestWritten answered %s at %q written %s, want %s at b no earlier than %s",
			got.ID(), got.Position, later, second, at)
	}
}
