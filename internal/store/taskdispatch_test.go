package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// The start snapshot is unknown until a dispatch writes it, reads back what
// was written, and is rewritten by the next dispatch: the column holds the
// latest proven copy and not a history of them.
func TestStartSnapshotIsUnknownUntilWrittenAndHoldsTheLatest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab")
	if task.StartSnapshot.IsKnown() {
		t.Fatalf("a task nothing dispatched carries a start snapshot: %+v", task.StartSnapshot)
	}

	first := CopySnapshot{Head: "aaaa", Branch: "task/one", Refs: []SnapshotRef{{Name: "refs/heads/task/one", Commit: "aaaa"}}}
	if err := s.SetTaskStartSnapshot(ctx, task.ID, first); err != nil {
		t.Fatalf("SetTaskStartSnapshot: %v", err)
	}
	got, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if snap, ok := got.StartSnapshot.Get(); !ok || !reflect.DeepEqual(snap, first) {
		t.Fatalf("the start snapshot reads back %+v, want %+v", got.StartSnapshot, first)
	}

	// A detached copy with no references of its own records an empty list,
	// which is a known fact, and the rewrite replaces the first.
	second := CopySnapshot{Head: "bbbb"}
	if err := s.SetTaskStartSnapshot(ctx, task.ID, second); err != nil {
		t.Fatalf("SetTaskStartSnapshot again: %v", err)
	}
	got, err = s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if snap, ok := got.StartSnapshot.Get(); !ok || snap.Head != "bbbb" || snap.Branch != "" || len(snap.Refs) != 0 || snap.Refs == nil {
		t.Fatalf("the rewritten start snapshot reads back %+v", got.StartSnapshot)
	}

	if err := s.SetTaskStartSnapshot(ctx, task.ID, CopySnapshot{}); err == nil {
		t.Fatal("a snapshot with no head was recorded")
	}
	if err := s.SetTaskStartSnapshot(ctx, "nosuchtaskaa", first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a snapshot of a missing task = %v, want ErrNotFound", err)
	}
}

// The primary snapshot is one row per attempt, written once: it needs the
// attempt to exist, reads back what was written, and a second write for the
// same attempt is refused with the first standing.
func TestPrimarySnapshotIsWrittenOncePerAttempt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab")

	before := PrimarySnapshot{TaskID: task.ID, Attempt: 1, Head: "aaaa", HeadBranch: Known("main"),
		Branches: []SnapshotRef{{Name: "refs/heads/main", Commit: "aaaa"}}}
	if _, err := s.RecordPrimarySnapshot(ctx, before); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a snapshot for an attempt that does not exist = %v, want ErrNotFound", err)
	}
	if _, err := s.BeginTaskAttempt(ctx, task.ID, 0); err != nil {
		t.Fatalf("BeginTaskAttempt: %v", err)
	}
	recorded, err := s.RecordPrimarySnapshot(ctx, before)
	if err != nil {
		t.Fatalf("RecordPrimarySnapshot: %v", err)
	}
	if recorded.RecordedAt.IsZero() || recorded.Head != "aaaa" || recorded.HeadBranch.Or("") != "main" ||
		!reflect.DeepEqual(recorded.Branches, before.Branches) {
		t.Fatalf("the snapshot reads back as %+v", recorded)
	}

	again := before
	again.Head = "bbbb"
	if _, err := s.RecordPrimarySnapshot(ctx, again); !errors.Is(err, ErrPrimarySnapshotRecorded) {
		t.Fatalf("a second snapshot for the attempt = %v, want ErrPrimarySnapshotRecorded", err)
	}
	got, err := s.PrimarySnapshots(ctx, task.ID)
	if err != nil || len(got) != 1 || got[0].Head != "aaaa" {
		t.Fatalf("PrimarySnapshots = %+v, %v; want the first record standing alone", got, err)
	}

	// A detached primary with no branches at all records an unknown branch
	// and an empty list, which are both known facts about that read.
	if _, err := s.BeginTaskAttempt(ctx, task.ID, 1); err != nil {
		t.Fatalf("BeginTaskAttempt: %v", err)
	}
	detached, err := s.RecordPrimarySnapshot(ctx, PrimarySnapshot{TaskID: task.ID, Attempt: 2, Head: "cccc"})
	if err != nil {
		t.Fatalf("RecordPrimarySnapshot for the second attempt: %v", err)
	}
	if detached.HeadBranch.IsKnown() || detached.Branches == nil || len(detached.Branches) != 0 {
		t.Fatalf("the detached snapshot reads back as %+v", detached)
	}
	got, err = s.PrimarySnapshots(ctx, task.ID)
	if err != nil || len(got) != 2 || got[0].Attempt != 1 || got[1].Attempt != 2 {
		t.Fatalf("PrimarySnapshots = %+v, %v; want both attempts in order", got, err)
	}

	for _, bad := range []PrimarySnapshot{
		{TaskID: task.ID, Attempt: 0, Head: "aaaa"},
		{TaskID: task.ID, Attempt: 2, Head: ""},
		{TaskID: "", Attempt: 2, Head: "aaaa"},
	} {
		if _, err := s.RecordPrimarySnapshot(ctx, bad); err == nil {
			t.Errorf("a malformed snapshot %+v was recorded", bad)
		}
	}
	if got, err := s.PrimarySnapshots(ctx, "nosuchtaskaa"); err != nil || len(got) != 0 {
		t.Fatalf("PrimarySnapshots of a missing task = %+v, %v", got, err)
	}
}
