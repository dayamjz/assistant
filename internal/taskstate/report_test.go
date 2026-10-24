package taskstate

import (
	"context"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// The report tier's place in PRD section 6's precedence: an investigation
// task with a stored report resolves Reported from SourceReport whatever its
// terminal reads, live, dead or unobserved, and whatever its log's newest
// entry claims. Each row has a control with the report row taken away, which
// resolves from the tiers below, so the row is what decided. The tier sits
// below the hold tier, so an open hold still answers Held; it does not read
// a delivery task, which a promoted investigation is; and it is not
// Complete, so a removed copy leaves the task Reported. A report-ready entry
// with no row beside it is a line of the log and does not reach the tier: a
// dead terminal still outranks it (P8).
func TestAStoredReportOutranksTheTerminal(t *testing.T) {
	principles.Cite(t, principles.P8)
	investigation := store.Task{ID: "t", Shape: store.TaskInvestigation}
	stored := store.Known(epoch.Add(time.Hour))
	cases := []struct {
		name       string
		in         Inputs
		withoutSt  State
		withoutSrc Source
	}{
		{"with a live terminal", Inputs{Process: Live, Events: log("", "started", "report-ready")}, Running, SourceProcess},
		{"with a dead terminal", Inputs{Process: Dead, Events: log("", "started", "report-ready")}, Unknown, SourceProcess},
		{"with an unobserved terminal", Inputs{Events: log("", "started", "report-ready")}, Reported, SourceEvent},
		{"with an entry after the report", Inputs{Process: Dead, Events: log("", "started", "report-ready", "working")}, Unknown, SourceProcess},
		{"with a removed copy", Inputs{Process: Dead, Events: log("", "started", "report-ready"), Cleanup: cleanup(true)}, Unknown, SourceProcess},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.in.Task = investigation
			without := Resolve(c.in)
			if without.State != c.withoutSt || without.Source != c.withoutSrc || without.Basis.ReportStored {
				t.Fatalf("without the report row resolved %s from %s (stored %t), want %s from %s",
					without.State, without.Source, without.Basis.ReportStored, c.withoutSt, c.withoutSrc)
			}
			c.in.ReportStoredAt = stored
			got := Resolve(c.in)
			if got.State != Reported || got.Source != SourceReport || !got.Basis.ReportStored {
				t.Fatalf("with the report row resolved %s from %s (stored %t), want reported from the report",
					got.State, got.Source, got.Basis.ReportStored)
			}
			if got.Progress {
				t.Fatal("a reported task is progress; nothing proceeds on a delivered investigation")
			}
			if got.Basis.Process != c.in.Process && c.in.Process != "" {
				t.Fatalf("the basis records liveness %s, want %s as read", got.Basis.Process, c.in.Process)
			}
		})
	}

	// An entry claiming a state of its own after the report is superseded by
	// the report, not read as the answer.
	got := Resolve(Inputs{Task: investigation, Process: Live, ReportStoredAt: stored, Events: log("", "started", "report-ready", "working")})
	if len(got.Superseded) != 1 || got.Superseded[0].Kind != "working" || got.Superseded[0].By != SourceReport {
		t.Fatalf("superseded %+v, want the working entry overridden by the report", got.Superseded)
	}

	// The hold tier is above it.
	held := Resolve(Inputs{Task: investigation, Process: Dead, ReportStoredAt: stored, Holds: []store.Hold{taskHold("task/t/ask-1", 1)}})
	if held.State != Held || held.Source != SourceHold || !held.Basis.ReportStored {
		t.Fatalf("an open hold over a stored report resolved %s from %s, want held from the hold", held.State, held.Source)
	}

	// A promoted task is a delivery task, and its row does not decide.
	promoted := Resolve(Inputs{Task: store.Task{ID: "t", Shape: store.TaskDelivery}, Process: Dead, ReportStoredAt: stored,
		Events: log("", "started", "report-ready")})
	if promoted.State != Unknown || promoted.Source != SourceProcess || !promoted.Basis.ReportStored {
		t.Fatalf("a delivery task with a report row resolved %s from %s (stored %t), want unknown from the dead terminal",
			promoted.State, promoted.Source, promoted.Basis.ReportStored)
	}
}

// Through the store: an investigation whose report is filed through
// store.StoreTaskReport, the one writer of the row and its history entry,
// resolves Reported from the report row while its terminal is live and
// after it is dead, Held from the report's own hold while that is open, and
// Reported once more after a removal is recorded, never Complete. The
// control is an investigation with the same history and no report, whose
// dead terminal resolves Unknown: the history entry alone decides nothing
// (P8).
func TestAFiledReportIsReadThroughTheSnapshot(t *testing.T) {
	principles.Cite(t, principles.P8)
	ctx := context.Background()
	s := openStore(t)
	inquiry := store.Task{ID: "lookaaaaaaaa", Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: "/w/look"}
	control := store.Task{ID: "ctrlaaaaaaaa", Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: "/w/ctrl"}
	for _, task := range []store.Task{inquiry, control} {
		if _, err := s.CreateTask(ctx, task, store.Disposition{}); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		beginAttempt(t, s, task.ID, 0)
		if err := s.SetAttemptSession(ctx, task.ID, 1, "ref-"+task.ID, store.SessionHost{}); err != nil {
			t.Fatalf("SetAttemptSession: %v", err)
		}
		appendEvents(t, s, task.ID, "started")
	}
	key := "task/" + inquiry.ID + "/ask-1"
	if _, _, err := s.StoreTaskReport(ctx, store.TaskReport{TaskID: inquiry.ID, Body: "found it", Report: []byte(`{"findings":[]}`)},
		[]store.Hold{{Key: key, TaskID: store.Known(inquiry.ID), Subject: "decide"}}); err != nil {
		t.Fatalf("StoreTaskReport: %v", err)
	}
	appendEvents(t, s, control.ID, store.ReportReadyEventKind)

	liveness := Live
	reader := NewReader(s, func(context.Context, string) Liveness { return liveness }, nil)
	read := func(id string) Resolution {
		t.Helper()
		got, err := reader.Task(ctx, id)
		if err != nil {
			t.Fatalf("Task: %v", err)
		}
		return got
	}

	if got := read(inquiry.ID); got.State != Held || got.Source != SourceHold || !got.Basis.ReportStored {
		t.Fatalf("with its report's hold open the investigation resolved %s from %s, want held", got.State, got.Source)
	}
	if _, err := s.ResolveHold(ctx, key, "noted", store.ResolvedByMachineInterface()); err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	for _, l := range []Liveness{Live, Dead} {
		liveness = l
		if got := read(inquiry.ID); got.State != Reported || got.Source != SourceReport || got.Basis.Process != l {
			t.Fatalf("with its terminal %s the reported investigation resolved %s from %s, want reported from the report", l, got.State, got.Source)
		}
	}
	if got := read(control.ID); got.State != Unknown || got.Source != SourceProcess || got.Basis.ReportStored {
		t.Fatalf("the control with a report-ready entry and no report resolved %s from %s, want unknown from the dead terminal",
			got.State, got.Source)
	}

	if _, err := s.RecordTaskCleanup(ctx, store.TaskCleanup{TaskID: inquiry.ID, Proof: store.CleanupProofScratch, Examined: nil},
		store.TaskInvestigation); err != nil {
		t.Fatalf("RecordTaskCleanup: %v", err)
	}
	if err := s.MarkCleanupRemoved(ctx, inquiry.ID, time.Now()); err != nil {
		t.Fatalf("MarkCleanupRemoved: %v", err)
	}
	if got := read(inquiry.ID); got.State != Reported || got.Source != SourceReport || !got.Basis.CleanupRemoved {
		t.Fatalf("a removed reported investigation resolved %s from %s, want reported: a stored report is not Complete", got.State, got.Source)
	}
}
