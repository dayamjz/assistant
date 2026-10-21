package service

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// PRD principle P8 for promotion_window: whether the window has passed is
// read from the report row's own time in the snapshot, so the answer for one
// read is the same whichever service asks it and however recently that
// service started. It turns exactly at the stored time plus the window, is
// never true for a task with no stored report, and never for a promoted
// task or a relayed investigation, whose workers other rules decide.
func TestThePromotionWindowIsReadFromTheStoredReport(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	stored := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	window := time.Hour
	reported := taskstate.Observed{
		Snapshot: store.TaskSnapshot{Task: store.Task{ID: "t", Shape: store.TaskInvestigation}, ReportStoredAt: store.Known(stored)},
		State:    taskstate.Resolution{State: taskstate.Reported, Source: taskstate.SourceReport},
	}
	if promotionWindowPassed(reported, stored.Add(window-time.Nanosecond), window) {
		t.Error("the window passed before the stored time plus the window")
	}
	if !promotionWindowPassed(reported, stored.Add(window), window) {
		t.Error("the window had not passed at the stored time plus the window")
	}
	late := stored.Add(24 * time.Hour)
	unreported := reported
	unreported.Snapshot.ReportStoredAt = store.Unknown[time.Time]()
	promoted := reported
	promoted.Snapshot.Task.Shape = store.TaskDelivery
	relayed := reported
	relayed.State = taskstate.Resolution{State: taskstate.Complete, Source: taskstate.SourceRecord, Basis: taskstate.Basis{Relayed: true}}
	for name, o := range map[string]taskstate.Observed{"unreported": unreported, "promoted": promoted, "relayed": relayed} {
		if promotionWindowPassed(o, late, window) {
			t.Errorf("the window passed for a %s task", name)
		}
	}
}
