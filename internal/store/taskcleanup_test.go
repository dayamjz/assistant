package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The cleanup record is written once and read back as written. A second
// write is refused and the first stands; a proof outside the closed set and a
// task that does not exist are refused before anything is written.
func TestACleanupRecordIsWrittenOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "task-1")

	if _, err := s.TaskCleanup(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a task cleanup has not reached = %v, want ErrNotFound", err)
	}
	if _, err := s.RecordTaskCleanup(ctx, TaskCleanup{TaskID: task.ID, Proof: "guessed"}); !errors.Is(err, ErrUnknownCleanupProof) {
		t.Fatalf("a proof outside the set = %v, want ErrUnknownCleanupProof", err)
	}
	if _, err := s.RecordTaskCleanup(ctx, TaskCleanup{TaskID: "nope", Proof: CleanupProofDefaultBranch}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a record for a missing task = %v, want ErrNotFound", err)
	}
	if _, err := s.TaskCleanup(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("a refused write left a record behind")
	}

	removed := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	written, err := s.RecordTaskCleanup(ctx, TaskCleanup{
		TaskID: task.ID, Proof: CleanupProofMergedPullRequest, Examined: []string{"abc", "def"}, RemovedAt: Known(removed),
	})
	if err != nil {
		t.Fatalf("RecordTaskCleanup: %v", err)
	}
	if written.Proof != CleanupProofMergedPullRequest || !reflect.DeepEqual(written.Examined, []string{"abc", "def"}) ||
		written.RecordedAt.IsZero() || !written.RemovedAt.Or(time.Time{}).Equal(removed) {
		t.Fatalf("the record came back as %+v", written)
	}
	read, err := s.TaskCleanup(ctx, task.ID)
	if err != nil || !reflect.DeepEqual(read, written) {
		t.Fatalf("TaskCleanup = %+v, %v; the write returned %+v", read, err, written)
	}

	_, err = s.RecordTaskCleanup(ctx, TaskCleanup{TaskID: task.ID, Proof: CleanupProofRemoteBranch})
	if !errors.Is(err, ErrCleanupRecorded) {
		t.Fatalf("a second record = %v, want ErrCleanupRecorded", err)
	}
	if again, err := s.TaskCleanup(ctx, task.ID); err != nil || !reflect.DeepEqual(again, written) {
		t.Fatalf("the second write disturbed the first: %+v, %v", again, err)
	}
}

// A check that examined nothing and removed nothing records those as known
// facts: an empty list, not an unknown one, and a removal that did not happen.
func TestACleanupRecordKeepsAnEmptyExaminationAndNoRemovalApart(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "task-1")
	written, err := s.RecordTaskCleanup(ctx, TaskCleanup{TaskID: task.ID, Proof: CleanupProofDefaultBranch})
	if err != nil {
		t.Fatalf("RecordTaskCleanup: %v", err)
	}
	if written.Examined == nil || len(written.Examined) != 0 {
		t.Fatalf("an empty examination reads back as %#v, want an empty list", written.Examined)
	}
	if written.RemovedAt.IsKnown() {
		t.Fatalf("a copy left standing reads back as removed at %v", written.RemovedAt)
	}
}

// The set is closed at the read as well as the write: a row carrying a proof
// this package does not define is refused rather than read back as some proof
// it is not. The row is planted directly, because no accessor can write it.
func TestACleanupRowOutsideTheSetIsRefusedOnRead(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "task-1")
	if _, err := s.write.ExecContext(ctx, `
		INSERT INTO task_cleanup (task_id, proof, examined, recorded_at, removed_at)
		VALUES (?, 'guessed', '[]', ?, NULL)`, task.ID, encodeTime(nowUTC())); err != nil {
		t.Fatalf("planting the row: %v", err)
	}
	if _, err := s.TaskCleanup(ctx, task.ID); !errors.Is(err, ErrUnknownCleanupProof) {
		t.Fatalf("reading the planted row = %v, want ErrUnknownCleanupProof", err)
	}
	if _, err := s.TaskSnapshot(ctx, task.ID); !errors.Is(err, ErrUnknownCleanupProof) {
		t.Fatalf("a snapshot over the planted row = %v, want ErrUnknownCleanupProof", err)
	}
	for _, p := range CleanupProofs() {
		if !p.Recognized() || strings.TrimSpace(p.String()) == "" {
			t.Errorf("%q is in CleanupProofs and not Recognized", p)
		}
	}
	if CleanupProof("guessed").Recognized() {
		t.Fatal("a proof nobody defined is Recognized")
	}
}
