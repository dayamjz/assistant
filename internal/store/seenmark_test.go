package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// endedRun creates a run with the given identifier, moves it to running, and
// ends it on a pull request read with the given end, which is what writes its
// merge record.
func endedRun(t *testing.T, s *Store, id string, end PullRequestEnd) {
	t.Helper()
	ctx := context.Background()
	seedRepository(t, s)
	r := completeRun()
	r.ID = id
	if _, err := s.CreateRun(ctx, r); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := s.TransitionRun(ctx, id, []RunStatus{RunPending}, RunRunning); err != nil {
		t.Fatalf("pending to running: %v", err)
	}
	to := RunPassed
	if end == PullRequestClosed {
		to = RunFailed
	}
	if _, err := s.TransitionRunOnPullRequest(ctx, id, []RunStatus{RunRunning}, to,
		PullRequestRead{PullRequest: 7, Head: "0123abc", End: end}, HoldClosure{}); err != nil {
		t.Fatalf("ending run %s on a %s pull request: %v", id, end, err)
	}
}

// cleanedTask creates a delivery task with a cleanup record, and marks its
// removal when removed is true.
func cleanedTask(t *testing.T, s *Store, id string, removed bool) {
	t.Helper()
	ctx := context.Background()
	seedTask(t, s, id)
	if _, err := s.RecordTaskCleanup(ctx, TaskCleanup{TaskID: id, Proof: CleanupProofRemoteBranch}, TaskDelivery); err != nil {
		t.Fatalf("RecordTaskCleanup: %v", err)
	}
	if removed {
		if err := s.MarkCleanupRemoved(ctx, id, time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("MarkCleanupRemoved: %v", err)
		}
	}
}

// A seen mark names a landed record the way that record is keyed, and only
// a landed one: a merge record that reads merged, by its run, and a task's
// cleanup record whose removal is recorded, by its task. The control for
// each refusal is the same call naming the landed record beside it, which
// is accepted, so the refusals are the records' and not the call's.
func TestASeenMarkNamesOnlyLandedRecords(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	endedRun(t, s, "run-merged", PullRequestMerged)
	endedRun(t, s, "run-closed", PullRequestClosed)
	cleanedTask(t, s, "taskremoveda", true)
	cleanedTask(t, s, "taskstanding", false)

	for _, c := range []struct {
		named SeenRecord
		want  error
	}{
		{SeenRecord{Kind: SeenMerge, Key: "run-closed"}, ErrNotLanded},
		{SeenRecord{Kind: SeenMerge, Key: "run-nowhere"}, ErrNotLanded},
		{SeenRecord{Kind: SeenMerge, Key: ""}, ErrNotLanded},
		{SeenRecord{Kind: SeenCleanup, Key: "taskstanding"}, ErrNotLanded},
		{SeenRecord{Kind: SeenCleanup, Key: "run-merged"}, ErrNotLanded},
		{SeenRecord{Kind: "landed", Key: "run-merged"}, ErrUnknownSeenKind},
	} {
		_, err := s.MarkSeen(ctx, []SeenRecord{{Kind: SeenMerge, Key: "run-merged"}, c.named})
		var refused *SeenRecordError
		if !errors.As(err, &refused) || !errors.Is(err, c.want) || refused.Record != c.named {
			t.Errorf("marking %+v = %v, want a *SeenRecordError naming it with %v", c.named, err, c.want)
		}
	}
	if marks, err := s.SeenMarks(ctx); err != nil || len(marks) != 0 {
		t.Fatalf("a refused batch wrote marks: %+v, %v", marks, err)
	}
	if _, err := s.MarkSeen(ctx, nil); err == nil {
		t.Fatal("a mark naming nothing was accepted")
	}

	landed := []SeenRecord{{Kind: SeenMerge, Key: "run-merged"}, {Kind: SeenCleanup, Key: "taskremoveda"}}
	marked, err := s.MarkSeen(ctx, landed)
	if err != nil {
		t.Fatalf("marking the two landed records: %v", err)
	}
	if len(marked) != 2 || marked[0].SeenRecord != landed[0] || marked[1].SeenRecord != landed[1] {
		t.Fatalf("the marks are %+v, want one per named record in order", marked)
	}
	marks, err := s.SeenMarks(ctx)
	if err != nil {
		t.Fatalf("SeenMarks: %v", err)
	}
	if len(marks) != 2 || marks[0].SeenRecord != landed[0] || marks[1].SeenRecord != landed[1] {
		t.Fatalf("the seen mark holds %+v, want the two records marked", marks)
	}
}

// A mark is appended and written once: marking a record again keeps the
// first row and returns it as it was first written, and the database
// refuses to rewrite or remove a row. The control is the first mark itself,
// which a direct insert of a second record shows the table takes.
func TestASeenMarkIsWrittenOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	endedRun(t, s, "run-merged", PullRequestMerged)
	record := SeenRecord{Kind: SeenMerge, Key: "run-merged"}
	first, err := s.MarkSeen(ctx, []SeenRecord{record})
	if err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	again, err := s.MarkSeen(ctx, []SeenRecord{record})
	if err != nil {
		t.Fatalf("marking the same record again: %v", err)
	}
	if !again[0].MarkedAt.Equal(first[0].MarkedAt) {
		t.Fatalf("a second mark moved the row to %v from %v", again[0].MarkedAt, first[0].MarkedAt)
	}
	if marks, err := s.SeenMarks(ctx); err != nil || len(marks) != 1 {
		t.Fatalf("marking twice left %+v, %v; want one row", marks, err)
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE seen_mark SET marked_at = ? WHERE key = ?`,
		encodeTime(nowUTC()), record.Key); err == nil {
		t.Error("a seen mark was rewritten")
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM seen_mark WHERE key = ?`, record.Key); err == nil {
		t.Error("a seen mark was removed")
	}
	if _, err := s.write.ExecContext(ctx, `INSERT INTO seen_mark (kind, key, marked_at) VALUES ('merge', 'other', ?)`,
		encodeTime(nowUTC())); err != nil {
		t.Fatalf("the table took no row at all, so the refusals above prove nothing: %v", err)
	}
}

// The kinds are a closed set, and writing to the result of SeenKinds does
// not change it.
func TestSeenKindsAreAClosedSet(t *testing.T) {
	for _, k := range SeenKinds() {
		if !k.Recognized() {
			t.Errorf("%s is in the set and not recognized", k)
		}
	}
	for _, outside := range []SeenKind{"", "landed", "MERGE"} {
		if outside.Recognized() {
			t.Errorf("%q is recognized", outside)
		}
	}
	kinds := SeenKinds()
	kinds[0] = "landed"
	if SeenKinds()[0] != SeenMerge {
		t.Error("writing to the result of SeenKinds changed the set")
	}
}
