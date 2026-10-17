package store

import (
	"context"
	"errors"
	"testing"
)

// A run's want of a code host is recorded while it is pending, reads back on
// the record, and is the one fact recorded twice; a run of a repository with a
// code host, which nothing marks, reads false.
func TestARunWithNoCodeHostSaysSoOnItsRecord(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if run.NoCodeHost {
		t.Fatal("a run nothing marked reads as having no code host")
	}
	if err := s.MarkRunNoCodeHost(ctx, run.ID); err != nil {
		t.Fatalf("MarkRunNoCodeHost: %v", err)
	}
	if err := s.MarkRunNoCodeHost(ctx, run.ID); err != nil {
		t.Fatalf("marking the run a second time: %v", err)
	}
	read, err := s.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !read.NoCodeHost {
		t.Fatal("a marked run reads as having a code host")
	}
	if err := s.MarkRunNoCodeHost(ctx, "no-such-run"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("marking a run that does not exist = %v, want ErrNotFound", err)
	}
}

// The schema refuses the fact acquired once a run has started, cleared, or
// written as anything but 1. The control is the write a pending run takes.
func TestTheSchemaRecordsNoCodeHostOnceBeforeARunStarts(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	started := seedRun(t, s)
	if _, err := s.TransitionRun(ctx, started.ID, []RunStatus{RunPending}, RunRunning); err != nil {
		t.Fatalf("starting the run: %v", err)
	}
	if err := rawWrite(ctx, s, `UPDATE run SET no_code_host = 1 WHERE id = ?`, started.ID); err == nil {
		t.Error("a running run acquired the want of a code host")
	}
	pending, err := s.CreateRun(ctx, Run{
		ID: "run-2", RepositoryID: started.RepositoryID, Branch: "other", SubmittedHead: "cccc",
		Build: testBuild(), ConfigDigest: "cfg-1",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := rawWrite(ctx, s, `UPDATE run SET no_code_host = 2 WHERE id = ?`, pending.ID); err == nil {
		t.Error("the want of a code host was recorded as 2")
	}
	if err := rawWrite(ctx, s, `UPDATE run SET no_code_host = 1 WHERE id = ?`, pending.ID); err != nil {
		t.Fatalf("a pending run could not be marked: %v", err)
	}
	if err := rawWrite(ctx, s, `UPDATE run SET no_code_host = NULL WHERE id = ?`, pending.ID); err == nil {
		t.Error("the want of a code host was cleared")
	}
}
