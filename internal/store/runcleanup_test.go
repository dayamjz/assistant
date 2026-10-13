package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// A run's cleanup record is written once with its proof, marked removed once
// after, and refuses on TaskCleanup's terms: a proof outside the set, a run
// that does not exist, a first write carrying a removal, and a second proof.
func TestARunCleanupRecordIsWrittenOnceWithItsProof(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	if _, err := s.RunCleanup(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a run cleanup has not reached = %v, want ErrNotFound", err)
	}
	if _, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run.ID, Proof: Known(CleanupProof("guessed"))}); !errors.Is(err, ErrUnknownCleanupProof) {
		t.Fatalf("a proof outside the set = %v, want ErrUnknownCleanupProof", err)
	}
	if _, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run.ID}); !errors.Is(err, ErrUnknownCleanupProof) {
		t.Fatalf("no proof at all = %v, want ErrUnknownCleanupProof", err)
	}
	if _, err := s.RecordRunCleanup(ctx, RunCleanup{Run: "nope", Proof: Known(CleanupProofRemoteBranch)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a record for a missing run = %v, want ErrNotFound", err)
	}
	removed := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	if _, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run.ID, Proof: Known(CleanupProofRemoteBranch), RemovedAt: Known(removed)}); !errors.Is(err, ErrCleanupRemovalFirst) {
		t.Fatalf("a first write carrying a removal = %v, want ErrCleanupRemovalFirst", err)
	}
	if _, err := s.RunCleanup(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("a refused write left a record behind")
	}
	if err := s.MarkRunCleanupRemoved(ctx, run.ID, removed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a removal marked before the proof = %v, want ErrNotFound", err)
	}

	written, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run.ID, Proof: Known(CleanupProofRemoteBranch), Examined: []string{"abc", "def"}})
	if err != nil {
		t.Fatalf("RecordRunCleanup: %v", err)
	}
	if written.Proof.Or("") != CleanupProofRemoteBranch || !reflect.DeepEqual(written.Examined, []string{"abc", "def"}) ||
		written.Refusal.IsKnown() || written.Detail != "" || written.RecordedAt.IsZero() || written.RemovedAt.IsKnown() || written.Kept() {
		t.Fatalf("the record came back as %+v", written)
	}
	if _, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run.ID, Proof: Known(CleanupProofDefaultBranch)}); !errors.Is(err, ErrCleanupRecorded) {
		t.Fatalf("a second proof = %v, want ErrCleanupRecorded", err)
	}
	if _, err := s.RecordRunCleanupRefusal(ctx, run.ID, "unproven", "later", nil); !errors.Is(err, ErrCleanupRecorded) {
		t.Fatalf("a refusal over a proof = %v, want ErrCleanupRecorded", err)
	}
	if err := s.MarkRunCleanupRemoved(ctx, run.ID, time.Time{}); err == nil {
		t.Fatal("a removal with no time was recorded")
	}
	if err := s.MarkRunCleanupRemoved(ctx, run.ID, removed); err != nil {
		t.Fatalf("MarkRunCleanupRemoved: %v", err)
	}
	if err := s.MarkRunCleanupRemoved(ctx, run.ID, removed.Add(time.Hour)); !errors.Is(err, ErrCleanupRemoved) {
		t.Fatalf("a second mark = %v, want ErrCleanupRemoved", err)
	}
	got, err := s.RunCleanup(ctx, run.ID)
	if err != nil {
		t.Fatalf("RunCleanup: %v", err)
	}
	written.RemovedAt = Known(removed)
	if !reflect.DeepEqual(got, written) {
		t.Fatalf("RunCleanup = %+v, want the proof with its removal marked, %+v", got, written)
	}
	// The seal holds at the database: the proof row cannot be rewritten or
	// deleted by any statement, which is what a later accessor would meet.
	if _, err := s.write.ExecContext(ctx, `UPDATE run_cleanup SET proof = 'default-branch' WHERE run = ?`, run.ID); err == nil {
		t.Fatal("the proof was rewritten under the seal")
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM run_cleanup WHERE run = ?`, run.ID); err == nil {
		t.Fatal("the record was deleted under the seal")
	}
}

// A refusal is the one state a row is revised out of: a later refusal
// rewrites it, a proof replaces it, and a refusal is never a proof.
func TestARunCleanupRefusalIsRewrittenUntilAProofReplacesIt(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	if _, err := s.RecordRunCleanupRefusal(ctx, run.ID, "", "no reason", nil); err == nil {
		t.Fatal("a refusal naming no reason was recorded")
	}
	if _, err := s.RecordRunCleanupRefusal(ctx, "nope", "unproven", "", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refusal for a missing run = %v, want ErrNotFound", err)
	}
	first, err := s.RecordRunCleanupRefusal(ctx, run.ID, "unproven", "none of the three", []string{"abc"})
	if err != nil {
		t.Fatalf("RecordRunCleanupRefusal: %v", err)
	}
	if !first.Kept() || first.Refusal.Or("") != "unproven" || first.Detail != "none of the three" ||
		!reflect.DeepEqual(first.Examined, []string{"abc"}) || first.Proof.IsKnown() {
		t.Fatalf("the refusal came back as %+v", first)
	}
	if err := s.MarkRunCleanupRemoved(ctx, run.ID, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a removal marked over a refusal = %v, want ErrNotFound: a refusal is no proof", err)
	}
	second, err := s.RecordRunCleanupRefusal(ctx, run.ID, "copy-dirty", "edited", []string{"abc", "def"})
	if err != nil {
		t.Fatalf("a second refusal: %v", err)
	}
	if second.Refusal.Or("") != "copy-dirty" || second.Detail != "edited" || !reflect.DeepEqual(second.Examined, []string{"abc", "def"}) {
		t.Fatalf("the second refusal did not rewrite the first: %+v", second)
	}
	proof, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run.ID, Proof: Known(CleanupProofMergedPullRequest), Examined: []string{"def"}})
	if err != nil {
		t.Fatalf("a proof over a refusal: %v", err)
	}
	if proof.Kept() || proof.Refusal.IsKnown() || proof.Detail != "" || proof.Proof.Or("") != CleanupProofMergedPullRequest ||
		!reflect.DeepEqual(proof.Examined, []string{"def"}) {
		t.Fatalf("the proof did not replace the refusal: %+v", proof)
	}
}

// A row carrying a proof outside the set is refused on read rather than
// read back as some proof it is not.
func TestARunCleanupRowOutsideTheSetIsRefusedOnRead(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if _, err := s.write.ExecContext(ctx, `
		INSERT INTO run_cleanup (run, proof, examined, refusal, detail, recorded_at, removed_at)
		VALUES (?, 'guessed', '[]', NULL, '', ?, NULL)`, run.ID, encodeTime(nowUTC())); err != nil {
		t.Fatalf("planting the row: %v", err)
	}
	if _, err := s.RunCleanup(ctx, run.ID); !errors.Is(err, ErrUnknownCleanupProof) {
		t.Fatalf("reading the planted row = %v, want ErrUnknownCleanupProof", err)
	}
}

// RecordTaskCleanup is anchored to the shape the cleanup was decided for,
// in the transaction that claims the record: a task whose shape is not that
// one is refused and nothing is written. The shape column is rewritten
// directly, because no accessor changes a shape in this build and the
// promotion that will (#109) is what the anchor exists to refuse.
func TestRecordTaskCleanupIsAnchoredToTheShape(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab")

	if _, err := s.RecordTaskCleanup(ctx, TaskCleanup{TaskID: task.ID, Proof: CleanupProofScratch}, TaskInvestigation); !errors.Is(err, ErrTaskShapeMoved) {
		t.Fatalf("a delivery task recorded as an investigation = %v, want ErrTaskShapeMoved", err)
	}
	if _, err := s.TaskCleanup(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("a refused write left a record behind")
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE task SET shape = 'investigation' WHERE id = ?`, task.ID); err != nil {
		t.Fatalf("moving the shape: %v", err)
	}
	if _, err := s.RecordTaskCleanup(ctx, TaskCleanup{TaskID: task.ID, Proof: CleanupProofRemoteBranch}, TaskDelivery); !errors.Is(err, ErrTaskShapeMoved) {
		t.Fatalf("a decision over a delivery task claimed after a promotion = %v, want ErrTaskShapeMoved", err)
	}
	if _, err := s.RecordTaskCleanup(ctx, TaskCleanup{TaskID: task.ID, Proof: CleanupProofScratch}, TaskInvestigation); err != nil {
		t.Fatalf("the matching shape was refused: %v", err)
	}
	// Scratch is in the closed set beside the three, so a record can carry it.
	if got, err := s.TaskCleanup(ctx, task.ID); err != nil || got.Proof != CleanupProofScratch {
		t.Fatalf("TaskCleanup = %+v, %v", got, err)
	}
}
