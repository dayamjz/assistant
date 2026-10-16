package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// PRD section 6's "an unacknowledged message surfaces once", under the
// injected clock: a message the record says was written is not detected
// while escalation_window has not elapsed from its written instant, is
// surfaced as actionable on the first pass after it has, and is never
// detected again, so one wake row stands for it. Its positive controls run
// beside it on the same task: a message the worker acknowledged and one
// whose send was refused are written facts the loop reads too, and neither
// surfaces, so the detection is reading the record's facts and not every
// row. The record is read back afterwards and is unchanged, and no message
// was added: the loop holds no route to a worker's input, and nothing
// resent it. P8: what the loop decides from is the message record, never
// the history or a log.
func TestAnUnacknowledgedMessageSurfacesOnceAfterTheWindowAndIsNotResent(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	h, records := loopHome(t)
	ctx := t.Context()
	task := plantRunning(t, records, "unreadmessag", "pane-unread").ID
	message := func(text string) store.Message {
		m, err := records.AppendMessage(ctx, task, 1, text, store.SentByCoordinator())
		if err != nil {
			t.Fatalf("recording the message: %v", err)
		}
		return m
	}
	unread := message("stay on scope")
	if _, err := records.MarkMessageWritten(ctx, unread.ID); err != nil {
		t.Fatal(err)
	}
	read := message("and add a test")
	if _, err := records.MarkMessageWritten(ctx, read.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.AcknowledgeMessage(ctx, task, read.ID); err != nil {
		t.Fatal(err)
	}
	refused := message("never arrived")
	if _, err := records.MarkMessageRefused(ctx, refused.ID, "the worker had ended"); err != nil {
		t.Fatal(err)
	}
	written, _ := records.TaskMessage(ctx, unread.ID)
	at, _ := written.WrittenAt.Get()

	terminals := newScriptedTerminals()
	terminals.live("pane-unread", "thinking\n")
	clock := &fakeClock{now: at}
	l := openLoop(t, h, records, terminals, clock)
	defer func() { _ = l.service.Close() }()

	if got := detectionsOf(l.nextPass(t), task, supervisor.KindMessageUnacknowledged); len(got) != 0 {
		t.Fatalf("a message written this instant was detected as unacknowledged: %+v", got)
	}
	clock.Advance(59 * time.Second)
	if got := detectionsOf(l.pass(t), task, supervisor.KindMessageUnacknowledged); len(got) != 0 {
		t.Fatalf("a message inside the window was detected: %+v", got)
	}
	clock.Advance(time.Second)
	got := detectionsOf(l.pass(t), task, supervisor.KindMessageUnacknowledged)
	if len(got) != 1 || got[0].Marker != messageUnacknowledgedMarker(unread.ID) ||
		got[0].Verdict.Reason != supervisor.ReasonActionable || got[0].Disposition != store.WakeAppended {
		t.Fatalf("the pass at the window = %+v, want message %d alone surfaced as actionable and appended", got, unread.ID)
	}
	for range 3 {
		clock.Advance(time.Hour)
		if again := detectionsOf(l.pass(t), task, supervisor.KindMessageUnacknowledged); len(again) != 0 {
			t.Fatalf("a later pass detected the message again: %+v", again)
		}
	}
	rows := wakesOfKind(t, l.records(t), task, supervisor.KindMessageUnacknowledged)
	if len(rows) != 1 || rows[0].Marker != messageUnacknowledgedMarker(unread.ID) {
		t.Fatalf("the message's wakes = %+v, want exactly one about message %d", rows, unread.ID)
	}
	after, err := l.records(t).TaskMessages(ctx, task)
	if err != nil || len(after) != 3 {
		t.Fatalf("the task has messages %+v (%v), want the three it was sent", after, err)
	}
	if after[0] != written {
		t.Fatalf("the surfaced message's record moved: %+v, want %+v", after[0], written)
	}
}

// A stop of a worker this service holds no session for ends it only through
// its verified identity, so a reference that cannot be verified ends
// nothing: one of another home, one with no start time, and one that does
// not decode are each *stopRefused before anything is signalled. The
// positive control is a reference of this home with a start time naming no
// running process, which agents.EndGroup answers without signalling
// anything and endWorker reports as ended through its identity, so the refusals are the reference's and not every
// unheld stop's.
func TestAStopOfAnUnheldWorkerEndsOnlyAVerifiedIdentity(t *testing.T) {
	t.Parallel()
	h, records := loopHome(t)
	_ = records.Close()
	s := &Service{home: h}
	this := session.HomeMarker(h)
	for _, c := range []struct {
		name string
		ref  string
	}{
		{"another home's reference", session.Reference{Home: strings.Repeat("0", len(this)), Task: "unheldtaskaa", PID: 1 << 22, Start: 7}.String()},
		{"a reference with no start time", session.Reference{Home: this, Task: "unheldtaskaa", PID: 1 << 22}.String()},
		{"text that is no reference", "not-a-reference"},
	} {
		_, err := s.endWorker("unheldtaskaa", c.ref)
		var refused *stopRefused
		if !errors.As(err, &refused) {
			t.Errorf("stopping %s = %v, want *stopRefused", c.name, err)
		}
	}
	gone := session.Reference{Home: this, Task: "unheldtaskaa", PID: 1 << 22, Start: 7}.String()
	ended, err := s.endWorker("unheldtaskaa", gone)
	if err != nil || !strings.Contains(ended, "verified identity") {
		t.Fatalf("stopping a verified reference naming no process = %q, %v, want it ended through its identity", ended, err)
	}
}

// PRD principle P6 at a stop the program cannot prove: the worker is asked
// to end, the probe that follows does not answer dead, and the stop is
// *stopUnverified with no hold registered, so the task is not made to read
// Held on a stop nobody can show happened. The probe is the service's own
// Terminals, here a double answering Live for the reference, and the ending
// is the identity path for a worker this service holds no session for. The
// positive control is the same request once the double answers dead, which
// registers the stop's hold for the attempt.
func TestAStopTheProbeDoesNotProveRegistersNoHold(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	h, records := loopHome(t)
	ref := session.Reference{Home: session.HomeMarker(h), Task: "unproventask", PID: 1 << 22, Start: 7}.String()
	task := plantRunning(t, records, "unproventask", ref).ID
	terminals := newScriptedTerminals()
	terminals.live(ref, "still going\n")
	l := openLoop(t, h, records, terminals, nil)
	defer func() { _ = l.service.Close() }()
	ctx := t.Context()

	_, err := l.service.stopTask(ctx, ipc.Request{}, machine.TaskRequest{Task: task})
	var unverified *stopUnverified
	if !errors.As(err, &unverified) || !errors.Is(err, ipc.ErrRefused) || unverified.Observation.Liveness != taskstate.Live {
		t.Fatalf("a stop the probe reads live = %v, want *stopUnverified carrying the live reading", err)
	}
	if _, err := l.records(t).Hold(ctx, stoppedKey(task, 1)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an unproven stop left a hold (%v)", err)
	}

	terminals.dead(ref)
	stopped, err := l.service.stopTask(ctx, ipc.Request{}, machine.TaskRequest{Task: task})
	if err != nil || stopped.Hold.Key != stoppedKey(task, 1) || !stopped.Hold.Open() {
		t.Fatalf("a stop the probe reads dead = %+v, %v, want the stop's hold for attempt 1", stopped, err)
	}
}
