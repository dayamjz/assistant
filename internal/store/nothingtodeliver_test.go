package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// nothingRun is a running run with one checkpoint in its history, which a
// nothing-to-deliver record can name.
func nothingRun(t *testing.T, s *Store) Run {
	t.Helper()
	run := runningRun(t, s)
	if _, err := s.AppendGraphCheckpoint(context.Background(), run.ID, "", 0, []byte(`{"seq":1}`)); err != nil {
		t.Fatalf("AppendGraphCheckpoint: %v", err)
	}
	return run
}

// PRD section 8's nothing-to-deliver record is written in the transaction
// that ends the run, so a run never reads as ended this way without it or
// the reverse (P8). The control is an ordinary pass, which leaves no record.
// A record the transaction refuses, one naming a checkpoint the run's
// history lacks or a submitted commit the run was not given, leaves the run
// where it stood with no record; one written is read back as written, and a
// retry after the commit writes nothing twice.
func TestTheNothingToDeliverRecordIsWrittenWithTheRunsEndOrNotAtAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("control: an ordinary pass writes none", func(t *testing.T) {
		t.Parallel()
		s := openStore(t)
		run := nothingRun(t, s)
		if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunRunning}, RunPassed); err != nil {
			t.Fatal(err)
		}
		if _, found, err := s.NothingToDeliver(ctx, run.ID); err != nil || found {
			t.Fatalf("an ordinary pass left a record: %v, %v", found, err)
		}
	})

	for _, tc := range []struct {
		name   string
		change func(*NothingToDeliver)
		want   error
	}{
		{"a checkpoint the run's history lacks", func(r *NothingToDeliver) { r.Checkpoint = 2 }, ErrNothingNotOfRun},
		{"a submitted commit the run was not given", func(r *NothingToDeliver) { r.Submitted = "ffff" }, ErrNothingNotOfRun},
	} {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			t.Parallel()
			s := openStore(t)
			run := nothingRun(t, s)
			r := nothingToDeliverOf2(run)
			tc.change(&r)
			if _, err := s.TransitionRunOnNothingToDeliver(ctx, run.ID, []RunStatus{RunRunning}, RunPassed, r); !errors.Is(err, tc.want) {
				t.Fatalf("the write = %v, want %v", err, tc.want)
			}
			if got, _ := s.Run(ctx, run.ID); got.Status != RunRunning {
				t.Fatalf("a refused record moved the run to %s", got.Status)
			}
			if _, found, _ := s.NothingToDeliver(ctx, run.ID); found {
				t.Fatal("a refused record was written")
			}
		})
	}

	for _, tc := range []struct {
		name   string
		change func(*NothingToDeliver)
	}{
		{"no examined commit", func(r *NothingToDeliver) { r.Examined = nil }},
		{"another answer", func(r *NothingToDeliver) { r.Answer = "absent" }},
		{"no default-branch commit", func(r *NothingToDeliver) { r.DefaultCommit = "" }},
		{"no checkpoint", func(r *NothingToDeliver) { r.Checkpoint = 0 }},
	} {
		t.Run("refused before the run is read: "+tc.name, func(t *testing.T) {
			t.Parallel()
			s := openStore(t)
			run := nothingRun(t, s)
			r := nothingToDeliverOf2(run)
			tc.change(&r)
			if _, err := s.TransitionRunOnNothingToDeliver(ctx, run.ID, []RunStatus{RunRunning}, RunPassed, r); err == nil {
				t.Fatal("the write was accepted")
			}
			if got, _ := s.Run(ctx, run.ID); got.Status != RunRunning {
				t.Fatalf("a refused record moved the run to %s", got.Status)
			}
		})
	}

	t.Run("written with the move, once", func(t *testing.T) {
		t.Parallel()
		s := openStore(t)
		run := nothingRun(t, s)
		want := nothingToDeliverOf2(run)
		moved, err := s.TransitionRunOnNothingToDeliver(ctx, run.ID, []RunStatus{RunRunning}, RunPassed, want)
		if err != nil || moved.Status != RunPassed {
			t.Fatalf("the write = %+v, %v", moved, err)
		}
		got, found, err := s.NothingToDeliver(ctx, run.ID)
		if err != nil || !found || got.Run != run.ID || got.DefaultCommit != want.DefaultCommit ||
			got.Submitted != want.Submitted || !slices.Equal(got.Examined, want.Examined) ||
			got.Answer != want.Answer || got.Checkpoint != want.Checkpoint || got.RecordedAt.IsZero() {
			t.Fatalf("read back %+v (%v, %v), want %+v", got, found, err, want)
		}
		again := want
		again.DefaultCommit = "eeee"
		if _, err := s.TransitionRunOnNothingToDeliver(ctx, run.ID, []RunStatus{RunRunning}, RunPassed, again); err != nil {
			t.Fatalf("a retry after the commit = %v, want the run returned as it stands", err)
		}
		if got2, _, _ := s.NothingToDeliver(ctx, run.ID); got2.DefaultCommit != want.DefaultCommit {
			t.Fatalf("a retry rewrote the record to %+v", got2)
		}
	})
}

// The record is sealed: never updated, and not deleted while its run
// stands, as the merge record is; TestForgettingARepositoryRemovesItsRowsFromEveryRunKeyedTable
// is the control that it goes with its run. A seen mark of it is refused
// deletion while the record stands too.
func TestTheNothingToDeliverRecordIsSealed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := nothingRun(t, s)
	if _, err := s.TransitionRunOnNothingToDeliver(ctx, run.ID, []RunStatus{RunRunning}, RunPassed, nothingToDeliverOf2(run)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkSeen(ctx, []SeenRecord{{Kind: SeenNothingToDeliver, Key: run.ID}}); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	for statement, refusal := range map[string]string{
		`UPDATE nothing_to_deliver SET default_commit = 'ffff' WHERE run = ?`:              "not updated",
		`DELETE FROM nothing_to_deliver WHERE run = ?`:                                     "while its run stands",
		`DELETE FROM seen_mark WHERE kind = 'nothing-to-deliver' AND key = ?`:              "while the record it marks stands",
		`INSERT INTO nothing_to_deliver VALUES (?, 'd', 's', '["s"]', 'absent', 1, 'now')`: "CHECK",
	} {
		if _, err := s.write.ExecContext(ctx, statement, run.ID); err == nil || !strings.Contains(err.Error(), refusal) {
			t.Fatalf("%s = %v, want a refusal carrying %q", statement, err, refusal)
		}
	}
}

// A seen mark names a nothing-to-deliver record by its run, and is refused
// for a run that wrote none, as it is for a run with no merge record.
func TestASeenMarkOfANothingToDeliverRecordNamesOneThatExists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := nothingRun(t, s)
	mark := []SeenRecord{{Kind: SeenNothingToDeliver, Key: run.ID}}
	var refused *SeenRecordError
	if _, err := s.MarkSeen(ctx, mark); !errors.As(err, &refused) || !errors.Is(refused.Reason, ErrNotLanded) {
		t.Fatalf("marking a run with no record = %v, want ErrNotLanded", err)
	}
	if _, err := s.TransitionRunOnNothingToDeliver(ctx, run.ID, []RunStatus{RunRunning}, RunPassed, nothingToDeliverOf2(run)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.MarkSeen(ctx, mark); err != nil || len(got) != 1 || got[0].SeenRecord != mark[0] {
		t.Fatalf("marking the record = %+v, %v", got, err)
	}
}

// A task's snapshot carries the nothing-to-deliver record of a run
// attributed to it, read in the snapshot's transaction, and not the record of
// a run it is not attributed to.
func TestATaskSnapshotCarriesItsRunsNothingToDeliverRecords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := nothingRun(t, s)
	if _, err := s.TransitionRunOnNothingToDeliver(ctx, run.ID, []RunStatus{RunRunning}, RunPassed, nothingToDeliverOf2(run)); err != nil {
		t.Fatal(err)
	}
	named, err := s.CreateTask(ctx, Task{ID: "taskaaaaaaaa", Shape: TaskDelivery, Project: "one", Mode: "interactive",
		WorktreePath: "/w/a", RunID: Known(run.ID)}, Disposition{})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateTask(ctx, Task{ID: "taskbbbbbbbb", Shape: TaskDelivery, Project: "one", Mode: "interactive",
		WorktreePath: "/w/b"}, Disposition{})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.TaskSnapshot(ctx, named.ID)
	if err != nil || len(snap.NothingToDeliver) != 1 || snap.NothingToDeliver[0].Run != run.ID {
		t.Fatalf("the task naming the run carries %+v (%v)", snap.NothingToDeliver, err)
	}
	if snap, err := s.TaskSnapshot(ctx, other.ID); err != nil || len(snap.NothingToDeliver) != 0 {
		t.Fatalf("a task the run is not attributed to carries %+v (%v)", snap.NothingToDeliver, err)
	}
}
