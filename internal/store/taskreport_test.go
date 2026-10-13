package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// A report is stored with the holds registered from it in one transaction,
// written once. The control for atomicity is a fault planted at the hold
// table: with it, nothing is stored, not the report row and not the holds
// registered before the one that failed, so a crash between the two writes
// cannot leave a stored report whose decisions were never registered.
func TestStoreTaskReportRegistersTheHoldsOrStoresNothing(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "taskaaaaaaab")
	holds := []Hold{
		{Key: "task/" + task.ID + "/finding/a", TaskID: Known(task.ID), Subject: "should the retry be bounded?", Detail: "pkg/a.go:3"},
		{Key: "task/" + task.ID + "/finding/b", TaskID: Known(task.ID), Subject: "is the cache keyed right?", Detail: "pkg/b.go"},
	}

	if _, err := s.TaskReport(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a task with no report = %v, want ErrNotFound", err)
	}
	for name, bad := range map[string]struct {
		report TaskReport
		holds  []Hold
	}{
		"no body":        {TaskReport{TaskID: task.ID, Report: []byte("{}")}, nil},
		"no parsed":      {TaskReport{TaskID: task.ID, Body: "x"}, nil},
		"no task":        {TaskReport{TaskID: "nope", Body: "x", Report: []byte("{}")}, nil},
		"hold no key":    {TaskReport{TaskID: task.ID, Body: "x", Report: []byte("{}")}, []Hold{{TaskID: Known(task.ID), Subject: "s"}}},
		"hold no task":   {TaskReport{TaskID: task.ID, Body: "x", Report: []byte("{}")}, []Hold{{Key: "k", Subject: "s"}}},
		"hold elsewhere": {TaskReport{TaskID: task.ID, Body: "x", Report: []byte("{}")}, []Hold{{Key: "k", TaskID: Known("other"), Subject: "s"}}},
	} {
		if _, err := s.StoreTaskReport(ctx, bad.report, bad.holds); err == nil {
			t.Errorf("%s was stored", name)
		}
		if _, err := s.TaskReport(ctx, task.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("after %s a report is there: %v", name, err)
		}
	}

	// The fault: the second hold cannot be registered.
	if _, err := s.write.ExecContext(ctx, `
		CREATE TRIGGER fault BEFORE INSERT ON hold WHEN NEW.key = '`+holds[1].Key+`'
		BEGIN SELECT RAISE(ABORT, 'planted fault'); END`); err != nil {
		t.Fatalf("planting the fault: %v", err)
	}
	if _, err := s.StoreTaskReport(ctx, TaskReport{TaskID: task.ID, Body: "the report", Report: []byte(`{"summary":"s"}`)}, holds); err == nil {
		t.Fatal("StoreTaskReport succeeded past the planted fault, so the control shows nothing")
	}
	if _, err := s.TaskReport(ctx, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the report survived the failed registration: %v", err)
	}
	if _, err := s.Hold(ctx, holds[0].Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the first hold survived the failed registration of the second: %v", err)
	}
	if _, err := s.write.ExecContext(ctx, `DROP TRIGGER fault`); err != nil {
		t.Fatalf("lifting the fault: %v", err)
	}

	stored, err := s.StoreTaskReport(ctx, TaskReport{TaskID: task.ID, Body: "the report", Report: []byte(`{"summary":"s"}`)}, holds)
	if err != nil {
		t.Fatalf("StoreTaskReport: %v", err)
	}
	if stored.Body != "the report" || string(stored.Report) != `{"summary":"s"}` || stored.RecordedAt.IsZero() ||
		!reflect.DeepEqual(stored.HoldKeys, []string{holds[0].Key, holds[1].Key}) {
		t.Fatalf("the report came back as %+v", stored)
	}
	for _, h := range holds {
		got, err := s.Hold(ctx, h.Key)
		if err != nil || !got.Open() || got.Subject != h.Subject || got.TaskID.Or("") != task.ID {
			t.Fatalf("hold %s = %+v, %v; want open and naming the task", h.Key, got, err)
		}
	}
	if _, err := s.StoreTaskReport(ctx, TaskReport{TaskID: task.ID, Body: "again", Report: []byte(`{}`)}, nil); !errors.Is(err, ErrTaskReportStored) {
		t.Fatalf("a second report = %v, want ErrTaskReportStored", err)
	}
	if again, err := s.TaskReport(ctx, task.ID); err != nil || !reflect.DeepEqual(again, stored) {
		t.Fatalf("the second write disturbed the first: %+v, %v", again, err)
	}
	// A hold already registered is left as it stands: resolving one and
	// storing a report naming its key again does not reopen it.
	if _, err := s.ResolveHold(ctx, holds[0].Key, "bounded", ResolvedByProgram()); err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	other := seedTask(t, s, "taskaaaaaaac")
	if _, err := s.StoreTaskReport(ctx, TaskReport{TaskID: other.ID, Body: "x", Report: []byte(`{}`)},
		[]Hold{{Key: holds[0].Key, TaskID: Known(other.ID), Subject: "the same decision"}}); err != nil {
		t.Fatalf("storing a report naming an existing key: %v", err)
	}
	if got, err := s.Hold(ctx, holds[0].Key); err != nil || got.Open() || got.TaskID.Or("") != task.ID {
		t.Fatalf("an existing hold was rewritten: %+v, %v", got, err)
	}
	// The seal holds at the database.
	if _, err := s.write.ExecContext(ctx, `UPDATE task_report SET body = 'rewritten' WHERE task_id = ?`, task.ID); err == nil {
		t.Fatal("the report was rewritten under the seal")
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM task_report WHERE task_id = ?`, task.ID); err == nil {
		t.Fatal("the report was deleted under the seal")
	}
	// An empty hold list is a known fact.
	third := seedTask(t, s, "taskaaaaaaad")
	if got, err := s.StoreTaskReport(ctx, TaskReport{TaskID: third.ID, Body: "x", Report: []byte(`{}`)}, nil); err != nil ||
		got.HoldKeys == nil || len(got.HoldKeys) != 0 {
		t.Fatalf("a report with no decisions = %+v, %v; want an empty key list", got, err)
	}
}

// ForgetHoldForTest is the counterfeit builder and nothing more: it deletes
// the one row it is given, refuses a key with no row, and takes the test.
func TestForgetHoldForTestDeletesOneRow(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	for _, key := range []string{"keep", "drop"} {
		if _, err := s.RegisterHold(ctx, Hold{Key: key, Subject: "s"}); err != nil {
			t.Fatalf("RegisterHold: %v", err)
		}
	}
	if err := s.ForgetHoldForTest(ctx, nil, "drop"); err == nil {
		t.Fatal("a call with no test was taken")
	}
	if err := s.ForgetHoldForTest(ctx, t, "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forgetting a key with no row = %v, want ErrNotFound", err)
	}
	if err := s.ForgetHoldForTest(ctx, t, "drop"); err != nil {
		t.Fatalf("ForgetHoldForTest: %v", err)
	}
	if _, err := s.Hold(ctx, "drop"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the row is still there: %v", err)
	}
	if _, err := s.Hold(ctx, "keep"); err != nil {
		t.Fatalf("the other row went with it: %v", err)
	}
}
