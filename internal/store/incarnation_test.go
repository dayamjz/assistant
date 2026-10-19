package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// The incarnation epoch only grows, and the store is its owner: a database
// that has recorded none reads as none, the first BeginIncarnation is epoch
// one, each later one is the next, and the epoch survives the store being
// closed and reopened, so a beacon written by an earlier process reads
// behind the current one rather than equal to it.
func TestIncarnationEpochGrowsAndSurvivesReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s := openStoreAt(t, path)
	if epoch, _, ok, err := s.CurrentIncarnation(ctx); err != nil || ok || epoch != 0 {
		t.Fatalf("CurrentIncarnation before any = %d, %v, %v; want none", epoch, ok, err)
	}
	first, err := s.BeginIncarnation(ctx)
	if err != nil || first.Epoch() != 1 {
		t.Fatalf("the first incarnation = %d (%v), want epoch 1", first.Epoch(), err)
	}
	second, err := s.BeginIncarnation(ctx)
	if err != nil || second.Epoch() != 2 {
		t.Fatalf("the second incarnation = %d (%v), want epoch 2", second.Epoch(), err)
	}
	if epoch, started, ok, err := s.CurrentIncarnation(ctx); err != nil || !ok || epoch != 2 || started.IsZero() {
		t.Fatalf("CurrentIncarnation = %d at %v, %v, %v; want epoch 2 with a start time", epoch, started, ok, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened := openStoreAt(t, path)
	third, err := reopened.BeginIncarnation(ctx)
	if err != nil || third.Epoch() != 3 {
		t.Fatalf("the incarnation after a reopen = %d (%v), want epoch 3", third.Epoch(), err)
	}
}

// The fence: a write by an incarnation that is no longer current is refused
// whole, markers included, with ErrStaleIncarnation, and the zero Incarnation
// nobody began is refused the same way. The control is the current
// incarnation's identical write, which lands.
func TestAReplacedIncarnationsWritesAreRefusedWhole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab").ID
	old := incarnationOf(t, s)
	current := incarnationOf(t, s)

	_, err := s.AppendWakeWithMarkers(ctx, old, WakeRequest{TaskID: task, Kind: "hold-opened", Marker: "hold-1"},
		MarkerAdvance{EventSequence: 7})
	if !errors.Is(err, ErrStaleIncarnation) {
		t.Fatalf("the replaced incarnation's append = %v, want ErrStaleIncarnation", err)
	}
	if _, err := s.AdvanceWakeMarkers(ctx, old, task, MarkerAdvance{EventSequence: 7}); !errors.Is(err, ErrStaleIncarnation) {
		t.Fatalf("the replaced incarnation's advance = %v, want ErrStaleIncarnation", err)
	}
	if _, err := s.AdvanceWakeMarkers(ctx, Incarnation{}, task, MarkerAdvance{}); !errors.Is(err, ErrStaleIncarnation) {
		t.Fatalf("the zero incarnation's advance = %v, want ErrStaleIncarnation", err)
	}
	if _, has, err := s.WakeMarkers(ctx, task); err != nil || has {
		t.Fatalf("markers after the refused writes: has=%v err=%v, want none written", has, err)
	}
	if wakes, err := s.TaskWakes(ctx, task); err != nil || len(wakes) != 0 {
		t.Fatalf("wakes after the refused write = %v (%v), want none", wakes, err)
	}

	out, err := s.AppendWakeWithMarkers(ctx, current, WakeRequest{TaskID: task, Kind: "hold-opened", Marker: "hold-1"},
		MarkerAdvance{EventSequence: 7})
	if err != nil || out.Disposition != WakeAppended || out.Markers.EventSequence != 7 {
		t.Fatalf("the current incarnation's append = %+v (%v), want appended with the markers advanced", out, err)
	}
}
