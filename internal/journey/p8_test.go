package journey_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// resolvedState is what the surface answered about a task whose event log ends
// on an open decision and whose validation run has moved past it.
type resolvedState struct {
	// lastEvent is the newest entry of the append-only log, which says the
	// decision is open.
	lastEvent string
	// events is how many entries the log holds.
	events int
	// reported is the state the surface reported.
	reported string
	// revision is the revision the surface reported, which increases each time
	// the answer changes and never on an append that leaves it alone.
	revision int64
	// source says what resolved the reported state.
	source string
	// superseded is what the surface said the answer outranked.
	superseded []taskstate.Superseded
	// detail is the detail the surface reported.
	detail string
}

// leak is text only the open decision's event carries.
const leak = "nobody-has-answered-this-decision"

// TestAnEventLogIsNotCurrentState drives PRD principle P8 through the binary
// that ships.
//
// Section 13's test is exactly this shape: given an event log whose last line
// says a decision is open, and a validation run that has moved past it, the
// resolver reports the run's state and marks the event superseded. The
// records are written the way the service writes them: the task and its events
// through taskstate.Settler, and the run's moves through the store followed by
// the settle the service's run hook makes. What is driven end to end is the
// reading: the surface is asked, and answers from the settled record, which the
// resolver derived from the run and not from the log's tail.
//
// What this does not reach is the service doing those writes itself: no run in
// this walk is attributed to a task by a push, so the attribution is the
// task's recorded repository and branch, and the settle after each move is the
// test's call at the point the service's hook makes it.
func TestAnEventLogIsNotCurrentState(t *testing.T) {
	principles.Cite(t, principles.P8)

	j := inClone(t)
	written := records(t, j)
	settler := taskstate.NewSettler(written, nil)
	ctx := t.Context()

	if _, err := written.UpsertRepository(ctx, store.Repository{
		ID: "journey-repository", WorkingPath: "/journey/p8-repository",
		UpstreamURL: "https://example.test/p8.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	const id = "journey-task"
	if _, err := settler.Create(ctx, store.Task{
		ID:           id,
		Shape:        store.TaskDelivery,
		Project:      "assistant",
		Mode:         "journey",
		WorktreePath: j.Dir(),
		RepositoryID: store.Known("journey-repository"),
		Branch:       store.Known("journey-branch"),
	}); err != nil {
		t.Fatalf("recording the task: %v", err)
	}

	// The worker's own history. The last thing it appended says it is waiting
	// on a decision, which is the note a coordinator reading the tail would
	// act on.
	for _, event := range []struct{ kind, detail string }{
		{"started", "the worker began"},
		{"validation-started", "a run was started for this branch"},
		{"decision-open", leak},
	} {
		if _, _, err := settler.Append(ctx, id, event.kind, event.detail); err != nil {
			t.Fatalf("appending %s: %v", event.kind, err)
		}
	}

	// The run, on the task's branch, moves through the gate and passes. The log
	// is not touched, because an event log is history.
	if _, err := written.CreateRun(ctx, store.Run{
		ID: "journey-run", RepositoryID: "journey-repository", Branch: "journey-branch",
		SubmittedHead: "aaaa", Base: "bbbb", Intent: "journey", IntentSource: "test",
		Build:        store.Build{Version: "v0.0.0", Revision: "0123456789abcdef", Go: "go1.25.0"},
		ConfigDigest: "journey",
	}); err != nil {
		t.Fatalf("recording the run: %v", err)
	}
	for _, move := range []struct {
		from []store.RunStatus
		to   store.RunStatus
	}{
		{[]store.RunStatus{store.RunPending}, store.RunRunning},
		{[]store.RunStatus{store.RunRunning}, store.RunHeld},
		{[]store.RunStatus{store.RunHeld}, store.RunRunning},
		{[]store.RunStatus{store.RunRunning}, store.RunPassed},
	} {
		if _, err := written.TransitionRun(ctx, "journey-run", move.from, move.to); err != nil {
			t.Fatalf("moving the run to %s: %v", move.to, err)
		}
		if err := settler.SettleForRun(ctx, "journey-run"); err != nil {
			t.Fatalf("settling the task after the run reached %s: %v", move.to, err)
		}
	}

	log, err := written.TaskEvents(ctx, id, 0, 0)
	if err != nil {
		t.Fatalf("reading the task's history: %v", err)
	}
	if len(log) == 0 {
		t.Fatalf("the task's history is empty, so there is no tail for this to be about")
	}

	var reported machine.Task
	if err := succeeds(t, j.Command("tasks", id)).Decode(&reported); err != nil {
		t.Fatalf("reading the task off the surface: %v", err)
	}
	var superseded []taskstate.Superseded
	if text, known := reported.State.Superseded.Get(); known {
		if err := json.Unmarshal([]byte(text), &superseded); err != nil {
			t.Fatalf("decoding what the surface said was superseded: %v", err)
		}
	}
	detail, _ := reported.State.Detail.Get()
	observed := resolvedState{
		lastEvent:  log[len(log)-1].Kind,
		events:     len(log),
		reported:   reported.State.State,
		revision:   reported.State.Revision,
		source:     reported.State.Source,
		superseded: superseded,
		detail:     detail,
	}

	authoritative := journey.Check[resolvedState]{
		What: "P8: a task whose event log ends on a decision",
		Clauses: []journey.Clause[resolvedState]{
			{
				States: "the log holds more than one entry",
				Holds: func(r resolvedState) error {
					if r.events < 2 {
						return fmt.Errorf("the log holds %d entries, and a tail nothing precedes says "+
							"nothing about whether the tail was read", r.events)
					}
					return nil
				},
			},
			{
				States: "the newest entry of the log says a decision is open",
				Holds: func(r resolvedState) error {
					if !strings.Contains(r.lastEvent, "decision-open") {
						return fmt.Errorf("the newest entry is %q, and this is about a log whose tail "+
							"says a decision is open", r.lastEvent)
					}
					return nil
				},
			},
			{
				States: "the surface reported a state",
				Holds: func(r resolvedState) error {
					if r.reported == "" {
						return errors.New("the surface reported no state at all")
					}
					return nil
				},
			},
			{
				States: "the state the surface reported is not the newest entry of the log",
				Holds: func(r resolvedState) error {
					if r.reported == r.lastEvent {
						return fmt.Errorf("the surface reported %q, which is the newest entry of the log; "+
							"reading the last line to decide what is true now is always wrong", r.reported)
					}
					return nil
				},
			},
			{
				States: "the state the surface reported is the one the authoritative record holds",
				Holds: func(r resolvedState) error {
					if r.reported != string(taskstate.Ready) {
						return fmt.Errorf("the surface reported %q and the run passed, which is %q",
							r.reported, taskstate.Ready)
					}
					return nil
				},
			},
			{
				States: "the state carries the source that resolved it",
				Holds: func(r resolvedState) error {
					if r.source == "" {
						return errors.New("the surface reported the state with no source, and a state " +
							"whose source is unrecorded cannot be told from one somebody guessed")
					}
					return nil
				},
			},
			{
				States: "the answer came from the run",
				Holds: func(r resolvedState) error {
					if r.source != string(taskstate.SourceRun) {
						return fmt.Errorf("the answer came from %q, not the run that moved past the decision", r.source)
					}
					return nil
				},
			},
			{
				States: "the open decision is marked superseded",
				Holds: func(r resolvedState) error {
					for _, s := range r.superseded {
						if s.Kind == "decision-open" && s.Claimed == taskstate.Held && s.By == taskstate.SourceRun {
							return nil
						}
					}
					return fmt.Errorf("the surface marked %+v superseded, and the open decision is not among them", r.superseded)
				},
			},
			{
				States:  "the text of the superseded event is not in the detail",
				Absence: true,
				// The event carrying the text was appended, so a resolver that
				// copied the tail could have put it in the detail.
				Possible: func(resolvedState) error { return nil },
				Holds: func(r resolvedState) error {
					if strings.Contains(r.detail, leak) {
						return fmt.Errorf("the detail %q carries the superseded event's text", r.detail)
					}
					return nil
				},
			},
			{
				States: "the authoritative record moved after the log did",
				Holds: func(r resolvedState) error {
					if r.revision < 2 {
						return fmt.Errorf("the state is at revision %d, so nothing here shows it moved "+
							"after the log did", r.revision)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[resolvedState]{
			{Named: "the surface answered with the newest entry of the log", Break: func(r resolvedState) resolvedState {
				r.reported = r.lastEvent
				return r
			}},
			{Named: "the surface answered with a state nobody recorded", Break: func(r resolvedState) resolvedState {
				r.reported = "waiting"
				return r
			}},
			{Named: "the surface answered nothing at all", Break: func(r resolvedState) resolvedState {
				r.reported = ""
				return r
			}},
			{Named: "the state carries no source, so it cannot be told from a guess",
				Break: func(r resolvedState) resolvedState {
					r.source = ""
					return r
				}},
			{Named: "the answer came from the log rather than the run", Break: func(r resolvedState) resolvedState {
				r.source = string(taskstate.SourceEvent)
				return r
			}},
			{Named: "the open decision was not marked superseded", Break: func(r resolvedState) resolvedState {
				r.superseded = nil
				return r
			}},
			{Named: "the detail was copied from the superseded event", Break: func(r resolvedState) resolvedState {
				r.detail = leak
				return r
			}},
			{Named: "the state never moved after the log did", Break: func(r resolvedState) resolvedState {
				r.revision = 1
				return r
			}},
			{Named: "the log has one entry, so a tail read and a whole read are the same read",
				Break: func(r resolvedState) resolvedState {
					r.events = 1
					return r
				}},
			{Named: "the log this was read against ends on something other than an open decision",
				Break: func(r resolvedState) resolvedState {
					r.lastEvent = "started"
					return r
				}},
		},
	}
	if err := authoritative.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
}
