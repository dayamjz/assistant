package taskstate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

func events(kinds ...string) []store.TaskEvent {
	out := make([]store.TaskEvent, len(kinds))
	for i, kind := range kinds {
		out[i] = store.TaskEvent{TaskID: "t", Sequence: int64(i + 1), Kind: kind, Detail: "text of " + kind}
	}
	return out
}

func runIn(status store.RunStatus) store.Run { return store.Run{ID: "run-1", Status: status} }

func in(runs []store.Run, log []store.TaskEvent) Inputs {
	return Inputs{Task: store.Task{ID: "t"}, Runs: runs, Events: log}
}

func sequences(list []Superseded) []int64 {
	var out []int64
	for _, s := range list {
		out = append(out, s.Sequence)
	}
	return out
}

// Every status a run can have maps to a state, and none maps to Unknown, so a
// status added to the store without a row here fails this instead of reading
// as a task nobody can describe.
func TestEveryRunStatusHasATaskState(t *testing.T) {
	for _, status := range store.RunStatuses() {
		got := Resolve(in([]store.Run{runIn(status)}, nil))
		if got.Source != SourceRun || !got.State.Recognized() || got.State == Unknown {
			t.Errorf("run status %q resolves to %+v", status, got)
		}
	}
}

func TestRunStatusesMapToTheStatesThePRDGives(t *testing.T) {
	want := map[store.RunStatus]State{
		store.RunPending: Validating, store.RunRunning: Validating, store.RunHeld: Held,
		store.RunPassed: Ready, store.RunFailed: Failed, store.RunTerminated: Failed,
	}
	for status, state := range want {
		if got := Resolve(in([]store.Run{runIn(status)}, nil)); got.State != state {
			t.Errorf("run status %q resolves to %q, want %q", status, got.State, state)
		}
	}
}

func TestAnUnrecognizedRunStatusAnswersUnknownFromTheRunNotTheLog(t *testing.T) {
	got := Resolve(in([]store.Run{runIn("exploded")}, events("started")))
	if got.State != Unknown || got.Source != SourceRun {
		t.Fatalf("an unreadable run resolved to %+v; falling through would let the log outrank it", got)
	}
}

// The log ends on an open decision. With a run attributed, the run answers;
// without one, the log does. The second is the positive control: it shows the
// first is not a resolver that ignores events.
func TestARunOutranksALogEndingOnAnOpenDecision(t *testing.T) {
	principles.Cite(t, principles.P8)
	log := events("started", "validation-started", "decision-open")

	got := Resolve(in([]store.Run{runIn(store.RunPassed)}, log))
	if got.State != Ready || got.Source != SourceRun {
		t.Fatalf("resolved to %+v, want ready from the run", got)
	}
	if len(got.Superseded) != 1 || got.Superseded[0].Kind != "decision-open" ||
		got.Superseded[0].Claimed != Held || got.Superseded[0].By != SourceRun {
		t.Fatalf("the open decision was not marked superseded: %+v", got.Superseded)
	}
	if detail, _ := got.Detail.Get(); strings.Contains(detail, "text of decision-open") {
		t.Fatalf("the superseded event's text reached the detail: %q", detail)
	}

	control := Resolve(in(nil, log))
	if control.State != Held || control.Source != SourceEvent || len(control.Superseded) != 0 {
		t.Fatalf("with no run the log should answer held: %+v", control)
	}
}

// Whole-log reading: a decision closed later is not superseded, one left open
// is, and neither a tail-only reader (which sees a note) nor an any-open-anywhere
// reader (which sees two) gets the same list.
func TestOnlyUnclosedDecisionsAreSuperseded(t *testing.T) {
	principles.Cite(t, principles.P8)
	log := events("decision-open", "progress-note", "decision-closed", "started",
		"decision-open", "progress-note")
	got := Resolve(in([]store.Run{runIn(store.RunPassed)}, log))
	if want := []int64{5}; fmt.Sprint(sequences(got.Superseded)) != fmt.Sprint(want) {
		t.Fatalf("superseded %v, want %v", sequences(got.Superseded), want)
	}
	if tail := log[len(log)-1].Kind; tail != "progress-note" {
		t.Fatalf("the control's tail is %q, so a tail-only reader would find nothing", tail)
	}
}

func TestTheNewestClaimingEventIsSupersededWhenItDisagrees(t *testing.T) {
	got := Resolve(in([]store.Run{runIn(store.RunFailed)}, events("started", "progress-note")))
	if fmt.Sprint(sequences(got.Superseded)) != "[1]" || got.Superseded[0].Claimed != Running {
		t.Fatalf("superseded %+v", got.Superseded)
	}
	// An event that agrees with the answer is not outranked.
	agree := Resolve(in([]store.Run{runIn(store.RunFailed)}, events("started", "failed")))
	if len(agree.Superseded) != 0 {
		t.Fatalf("an agreeing event was reported superseded: %+v", agree.Superseded)
	}
}

func TestTheProcessRung(t *testing.T) {
	stale := events("started")
	cases := []struct {
		name    string
		process Liveness
		runs    []store.Run
		log     []store.TaskEvent
		state   State
		source  Source
		outrank int
	}{
		{"live terminal reads as running", Live, nil, events("report-ready"), Running, SourceProcess, 1},
		{"dead terminal with no run is unknown, not the stale event", Dead, nil, stale, Unknown, SourceProcess, 1},
		{"dead terminal with a run defers to the run", Dead, []store.Run{runIn(store.RunPassed)}, stale, Ready, SourceRun, 1},
		{"unobserved terminal skips the rung", Unobserved, nil, stale, Running, SourceEvent, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			input := in(c.runs, c.log)
			input.Process = c.process
			got := Resolve(input)
			if got.State != c.state || got.Source != c.source || len(got.Superseded) != c.outrank {
				t.Fatalf("resolved to %+v", got)
			}
		})
	}
}

func TestOnlyANewestRecognizedStateEventAnswers(t *testing.T) {
	for _, kinds := range [][]string{
		{"started", "progress-note"},
		{"started", "decision-closed"},
		{"started", "some-kind-nobody-defined"},
		nil,
	} {
		got := Resolve(in(nil, events(kinds...)))
		if got.State != Unknown || got.Source != SourceNone || got.Detail.IsKnown() {
			t.Errorf("log %v resolved to %+v, want unknown from nothing", kinds, got)
		}
	}
	got := Resolve(in(nil, events("progress-note", "waiting-external")))
	if got.State != Paused || got.Source != SourceEvent {
		t.Fatalf("resolved to %+v", got)
	}
}

func TestAHeldRunNamesTheOpenHoldSubjectOnly(t *testing.T) {
	input := in([]store.Run{runIn(store.RunHeld)}, events("decision-open"))
	input.Holds = []store.Hold{
		{Key: "closed", RunID: store.Known("run-1"), Subject: "an old question", Resolution: store.Known("yes")},
		{Key: "open", RunID: store.Known("run-1"), Subject: "ship it?", Detail: "private detail"},
		{Key: "other", RunID: store.Known("run-2"), Subject: "someone else's"},
	}
	got := Resolve(input)
	detail, _ := got.Detail.Get()
	if got.State != Held || !strings.Contains(detail, "ship it?") ||
		strings.Contains(detail, "old question") || strings.Contains(detail, "private") ||
		strings.Contains(detail, "someone else") || strings.Contains(detail, "text of") {
		t.Fatalf("held run resolved to %+v", got)
	}
	if len(got.Superseded) != 0 {
		t.Fatalf("an open decision a held run agrees with was reported superseded: %+v", got.Superseded)
	}
}

// Every kind in the table is reachable by its name, and the table's decision
// roles never claim a state they should not.
func TestKindTable(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range kinds {
		if seen[row.kind] {
			t.Errorf("kind %q appears twice", row.kind)
		}
		seen[row.kind] = true
		state, claimed := claims(row.kind)
		switch row.role {
		case roleDecisionClosed, roleNote:
			if claimed {
				t.Errorf("%q is decision-only or a note and claims %q", row.kind, state)
			}
		default:
			if !claimed || !state.Recognized() {
				t.Errorf("%q claims %q (%v)", row.kind, state, claimed)
			}
		}
	}
	if _, ok := lookup("not-a-kind"); ok {
		t.Fatal("an unlisted kind was recognized")
	}
}
