package taskstate

import (
	"context"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// A local landing record is the terminal tier for the task it names: Landed
// from the record above an open hold and above every run, Complete once the
// cleanup record carries a removal, and a record naming another task reaches
// nothing. The control is the same inputs without the record, which the hold
// tier answers, so the record is what decided. The detail says the branch was
// fast-forwarded on this machine and never that anything merged or passed.
func TestALocalLandingRecordLandsItsTask(t *testing.T) {
	principles.Cite(t, principles.P14)
	task := store.Task{ID: "t", Shape: store.TaskDelivery, Delivery: store.Known(config.DeliveryLocal)}
	landing := store.LocalLanding{Task: "t", Commit: "c2", Branch: "refs/heads/main", Before: "c1", After: "c2", RecordedAt: epoch}
	holding := []store.Hold{{Key: "task/t/local-approval/c2/c1", TaskID: store.Known("t"), Subject: "approve", OpenedAt: epoch}}
	runs := []store.Run{runIn("r1", store.RunFailed, 1)}

	got := Resolve(Inputs{Task: task, Holds: holding, Runs: runs, LocalLanding: store.Known(landing)})
	if got.State != Landed || got.Source != SourceRecord || got.Basis.LandedLocally != "c2" || got.Basis.LandedRun != "" {
		t.Fatalf("a locally landed task resolved %s from %s (basis %+v), want landed from the record", got.State, got.Source, got.Basis)
	}
	detail := got.Detail.Or("")
	if !strings.Contains(detail, "fast-forwarded") || !strings.Contains(detail, "c1") || !strings.Contains(detail, "c2") {
		t.Fatalf("the detail %q does not name the fast-forward", detail)
	}
	for _, word := range []string{"merged", "passed", "validated"} {
		if strings.Contains(detail, word) {
			t.Fatalf("the detail %q says %q of a change nothing validated", detail, word)
		}
	}
	if got.Progress {
		t.Fatal("a terminal record reads as progress")
	}
	if got := Resolve(Inputs{Task: task, Holds: holding, Runs: runs}); got.State != Held || got.Source != SourceHold {
		t.Fatalf("without the record the task resolved %s from %s, want held from the hold", got.State, got.Source)
	}

	got = Resolve(Inputs{Task: task, LocalLanding: store.Known(landing), Cleanup: cleanup(true)})
	if got.State != Complete || got.Source != SourceRecord {
		t.Fatalf("a locally landed task whose copy was removed resolved %s from %s, want complete", got.State, got.Source)
	}
	if got := Resolve(Inputs{Task: task, LocalLanding: store.Known(landing), Cleanup: cleanup(false)}); got.State != Landed {
		t.Fatalf("a proof with no removal resolved %s, want landed", got.State)
	}

	elsewhere := landing
	elsewhere.Task = "another"
	if got := Resolve(Inputs{Task: task, Holds: holding, LocalLanding: store.Known(elsewhere)}); got.State != Held || got.Basis.LandedLocally != "" {
		t.Fatalf("another task's record resolved %s (landed locally %q), want the hold's answer", got.State, got.Basis.LandedLocally)
	}
}

// Through the store and the Reader, as a consumer reads it: the approval
// hold holds the task, and the record written with the approval lands it in
// the same read that shows the hold closed.
func TestALocalLandingRecordIsReadThroughTheSnapshot(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	task, err := s.CreateTask(ctx, store.Task{ID: "taaaaaaaaalc", Shape: store.TaskDelivery, Project: "one", Mode: "interactive",
		WorktreePath: "/w/lc", Delivery: store.Known(config.DeliveryLocal)}, store.Disposition{})
	if err != nil {
		t.Fatal(err)
	}
	hold, err := s.RegisterHold(ctx, store.Hold{Key: "task/" + task.ID + "/local-approval/c2/c1", TaskID: store.Known(task.ID), Subject: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveTask(t, s, task.ID); got.State != Held {
		t.Fatalf("a task waiting for approval resolved %s", got.State)
	}
	if _, err := s.RecordLocalLanding(ctx, store.LocalLanding{Task: task.ID, Commit: "c2", Branch: "refs/heads/main", Before: "c1", After: "c2"},
		store.HoldClosure{Hold: hold, Resolution: "approve", By: store.ResolvedByMachineInterface()}); err != nil {
		t.Fatal(err)
	}
	if got := resolveTask(t, s, task.ID); got.State != Landed || got.Source != SourceRecord || got.Basis.Hold != "" {
		t.Fatalf("the approved task resolved %s from %s (hold %q), want landed from the record", got.State, got.Source, got.Basis.Hold)
	}
}
