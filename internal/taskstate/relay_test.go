package taskstate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// relayOf is a findings relayed record naming the task, in the shape
// store.RecordTaskRelay writes it.
func relayOf(task string) store.Optional[store.TaskRelay] {
	return store.Known(store.TaskRelay{TaskID: task, Event: 3, RecordedAt: epoch.Add(2 * time.Hour)})
}

// PRD section 6's Reported to Complete edge, over Inputs: an investigation
// task with a findings relayed record resolves Complete from the terminal
// tier, whatever its terminal, its open holds and its report say, and
// Basis.Relayed records it. Each row's control is the same inputs without
// the record, which resolve from the tiers below, so the record is what
// decided. Nothing else reaches it: a findings-relayed entry with no record
// is a note and moves nothing (P8), a record naming another task is not
// read, and a delivery task, which a promoted investigation is, is not made
// Complete by one.
func TestARelayRecordCompletesAnInvestigation(t *testing.T) {
	principles.Cite(t, principles.P8)
	investigation := store.Task{ID: "t", Shape: store.TaskInvestigation}
	stored := store.Known(epoch.Add(time.Hour))
	reported := log("", "started", store.ReportReadyEventKind)
	cases := []struct {
		name       string
		in         Inputs
		withoutSt  State
		withoutSrc Source
	}{
		{"over a live terminal", Inputs{Process: Live, ReportStoredAt: stored, Events: reported}, Reported, SourceReport},
		{"over a dead terminal", Inputs{Process: Dead, ReportStoredAt: stored, Events: reported}, Reported, SourceReport},
		{"over a removed copy", Inputs{Process: Dead, ReportStoredAt: stored, Events: reported, Cleanup: cleanup(true)}, Reported, SourceReport},
		{"over a hold opened since", Inputs{Process: Dead, ReportStoredAt: stored, Events: reported,
			Holds: []store.Hold{taskHold("task/t/stop", 1)}}, Held, SourceHold},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.in.Task = investigation
			without := Resolve(c.in)
			if without.State != c.withoutSt || without.Source != c.withoutSrc || without.Basis.Relayed {
				t.Fatalf("without the record resolved %s from %s (relayed %t), want %s from %s",
					without.State, without.Source, without.Basis.Relayed, c.withoutSt, c.withoutSrc)
			}
			c.in.Relay = relayOf("t")
			c.in.Events = append(c.in.Events, store.TaskEvent{TaskID: "t", Sequence: 3, Kind: store.FindingsRelayedEventKind})
			got := Resolve(c.in)
			if got.State != Complete || got.Source != SourceRecord || !got.Basis.Relayed {
				t.Fatalf("with the record resolved %s from %s (relayed %t), want complete from the record",
					got.State, got.Source, got.Basis.Relayed)
			}
			if got.Progress {
				t.Fatal("a complete task reads as progress")
			}
		})
	}

	for name, in := range map[string]Inputs{
		"an entry with no record": {Task: investigation, Process: Dead, ReportStoredAt: stored,
			Events: log("", "started", store.ReportReadyEventKind, store.FindingsRelayedEventKind)},
		"a record naming another task": {Task: investigation, Process: Dead, ReportStoredAt: stored, Events: reported, Relay: relayOf("other")},
	} {
		if got := Resolve(in); got.State != Reported || got.Source != SourceReport || got.Basis.Relayed {
			t.Errorf("%s resolved %s from %s (relayed %t), want reported from the report", name, got.State, got.Source, got.Basis.Relayed)
		}
	}
	promoted := Resolve(Inputs{Task: store.Task{ID: "t", Shape: store.TaskDelivery}, Process: Dead, ReportStoredAt: stored,
		Events: reported, Relay: relayOf("t")})
	if promoted.State == Complete || !promoted.Basis.Relayed {
		t.Fatalf("a delivery task with a relay record resolved %s from %s (relayed %t), want it recorded and not deciding",
			promoted.State, promoted.Source, promoted.Basis.Relayed)
	}
}

// Through the store: an investigation whose report is filed and answered
// resolves Reported, and once store.RecordTaskRelay writes its record,
// Complete from the terminal tier. The writer is the one that keeps the edge
// to a reported investigation: it refuses a delivery task, here a promoted
// investigation that keeps its report and has no hold open, an investigation
// with no report, one an open hold names, a second record, and a write
// anchored to a history that moved, each with nothing written, which the
// unchanged resolution and history show.
func TestARelayIsRecordedOnlyForAReportedInvestigation(t *testing.T) {
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P8)
	ctx := context.Background()
	s := openStore(t)
	inquiry := store.Task{ID: "lookaaaaaaaa", Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: "/w/look"}
	silent := store.Task{ID: "mutebbbbbbbb", Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: "/w/mute"}
	for _, task := range []store.Task{inquiry, silent} {
		if _, err := s.CreateTask(ctx, task, store.Disposition{}); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		appendEvents(t, s, task.ID, "started")
	}
	// A promoted investigation is a delivery task that keeps its stored report
	// and has no hold open, so only its shape refuses it.
	promoted := store.Task{ID: "promcccccccc", Shape: store.TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: "/w/prom"}
	if _, err := s.CreateTask(ctx, promoted, store.Disposition{}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, _, err := s.StoreTaskReport(ctx, store.TaskReport{TaskID: promoted.ID, Body: "found it", Report: []byte(`{"findings":[]}`)},
		nil); err != nil {
		t.Fatalf("StoreTaskReport: %v", err)
	}
	if _, _, err := s.TransitionTaskShape(ctx, promoted.ID, store.TaskInvestigation, store.TaskDelivery, repo, "promoted",
		config.DeliveryGated, "Deliver it.", 3, "promoted in the test"); err != nil {
		t.Fatalf("TransitionTaskShape: %v", err)
	}
	key := "task/" + inquiry.ID + "/ask-1"
	if _, _, err := s.StoreTaskReport(ctx, store.TaskReport{TaskID: inquiry.ID, Body: "found it", Report: []byte(`{"findings":[]}`)},
		[]store.Hold{{Key: key, TaskID: store.Known(inquiry.ID), Subject: "decide"}}); err != nil {
		t.Fatalf("StoreTaskReport: %v", err)
	}
	reader := NewReader(s, func(context.Context, string) Liveness { return Dead }, nil)
	read := func(id string) Resolution {
		t.Helper()
		got, err := reader.Task(ctx, id)
		if err != nil {
			t.Fatalf("Task: %v", err)
		}
		return got
	}
	refused := func(id string, want error) {
		t.Helper()
		before := read(id)
		_, _, err := s.RecordTaskRelay(ctx, id, before.Basis.EventSequence)
		if !errors.Is(err, want) {
			t.Fatalf("recording task %s relayed = %v, want %v", id, err, want)
		}
		if after := read(id); after.Basis.EventSequence != before.Basis.EventSequence || after.Basis.Relayed != before.Basis.Relayed ||
			after.State != before.State {
			t.Fatalf("a refused relay moved task %s from %+v to %+v", id, before, after)
		}
	}

	refused(promoted.ID, store.ErrNotRelayable)
	refused(silent.ID, store.ErrNotRelayable)
	refused(inquiry.ID, store.ErrNotRelayable)
	if _, err := s.ResolveHold(ctx, key, "noted", store.ResolvedByMachineInterface()); err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	before := read(inquiry.ID)
	if before.State != Reported || before.Source != SourceReport {
		t.Fatalf("with its hold answered the investigation resolved %s from %s, want reported", before.State, before.Source)
	}
	var moved *store.TaskMovedError
	if _, _, err := s.RecordTaskRelay(ctx, inquiry.ID, before.Basis.EventSequence-1); !errors.As(err, &moved) {
		t.Fatalf("a relay anchored behind the history = %v, want the history moved", err)
	}
	relay, event, err := s.RecordTaskRelay(ctx, inquiry.ID, before.Basis.EventSequence)
	if err != nil {
		t.Fatalf("RecordTaskRelay: %v", err)
	}
	if event.Kind != store.FindingsRelayedEventKind || event.Sequence != before.Basis.EventSequence+1 || relay.Event != event.Sequence {
		t.Fatalf("the relay recorded %+v and appended %+v, want one entry after the history it read, named by the record", relay, event)
	}
	got := read(inquiry.ID)
	if got.State != Complete || got.Source != SourceRecord || !got.Basis.Relayed {
		t.Fatalf("a relayed investigation resolved %s from %s (relayed %t), want complete from the record", got.State, got.Source, got.Basis.Relayed)
	}
	refused(inquiry.ID, store.ErrTaskRelayed)
}
