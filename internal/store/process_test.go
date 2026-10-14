package store

import (
	"context"
	"errors"
	"testing"
)

// TestAProcessRecordIsWrittenListedAndClearedOnce pins the record's whole
// life on the accessors that own it: a record written is listed with the
// identity it was given, a second record for the same run and group is
// refused with the first standing, a clear removes it, and a second clear is
// told it cleared nothing rather than reading as a clear.
func TestAProcessRecordIsWrittenListedAndClearedOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	if records, err := s.ProcessRecords(ctx); err != nil || len(records) != 0 {
		t.Fatalf("a fresh home lists %v, %v, want no process record", records, err)
	}
	written, err := s.RecordProcess(ctx, ProcessRecord{RunID: run.ID, Stage: "review", Group: 4321, Identity: Known(uint64(98765))})
	if err != nil {
		t.Fatalf("RecordProcess: %v", err)
	}
	if written.StartedAt.IsZero() {
		t.Fatal("the record carries no start, which RecordProcess assigns")
	}
	_, err = s.RecordProcess(ctx, ProcessRecord{RunID: run.ID, Stage: "fix", Group: 4321, Identity: Known(uint64(1))})
	if !errors.Is(err, ErrProcessRecorded) {
		t.Fatalf("a second record for the same run and group answered %v, want ErrProcessRecorded", err)
	}
	unknown, err := s.RecordProcess(ctx, ProcessRecord{RunID: run.ID, Stage: "fix", Group: 4322})
	if err != nil {
		t.Fatalf("RecordProcess with no identity: %v", err)
	}
	if _, known := unknown.Identity.Get(); known {
		t.Fatal("a record written with no identity reads back with one")
	}

	records, err := s.ProcessRecords(ctx)
	if err != nil || len(records) != 2 {
		t.Fatalf("ProcessRecords = %v, %v, want two", records, err)
	}
	first := records[0]
	if first.Group != 4321 || first.Stage != "review" || first.RunID != run.ID {
		t.Fatalf("the first record reads %+v, want the review group as written, which the refused second write did not replace", first)
	}
	if start, known := first.Identity.Get(); !known || start != 98765 {
		t.Fatalf("the identity reads back as %d, %t, want 98765", start, known)
	}

	if err := s.ClearProcess(ctx, run.ID, 4321); err != nil {
		t.Fatalf("ClearProcess: %v", err)
	}
	if err := s.ClearProcess(ctx, run.ID, 4321); !errors.Is(err, ErrNotFound) {
		t.Fatalf("clearing a cleared record answered %v, want ErrNotFound", err)
	}
	records, err = s.ProcessRecords(ctx)
	if err != nil || len(records) != 1 || records[0].Group != 4322 {
		t.Fatalf("after one clear ProcessRecords = %v, %v, want only group 4322", records, err)
	}
}

// TestRecordProcessRefusesWhatRecoveryCouldNeverActOn pins the refusals: a
// record with no run, no stage, or no positive group, and one naming a run
// the home has no row for, are refused and leave nothing behind.
func TestRecordProcessRefusesWhatRecoveryCouldNeverActOn(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	for name, r := range map[string]ProcessRecord{
		"no run":      {Stage: "review", Group: 10},
		"no stage":    {RunID: run.ID, Stage: " ", Group: 10},
		"no group":    {RunID: run.ID, Stage: "review"},
		"negative":    {RunID: run.ID, Stage: "review", Group: -10},
		"no such run": {RunID: "no-such-run", Stage: "review", Group: 10},
	} {
		if _, err := s.RecordProcess(ctx, r); err == nil {
			t.Errorf("%s: RecordProcess accepted %+v", name, r)
		}
	}
	if _, err := s.RecordProcess(ctx, ProcessRecord{RunID: "no-such-run", Stage: "review", Group: 10}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a record naming no run answered %v, want ErrNotFound", err)
	}
	if records, err := s.ProcessRecords(ctx); err != nil || len(records) != 0 {
		t.Fatalf("refused records left %v, %v behind", records, err)
	}
}
