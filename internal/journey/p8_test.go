package journey_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// resolvedState is what the surface answered about a task whose event log ends
// on an open decision and whose authoritative state has moved past it.
type resolvedState struct {
	// lastEvent is the newest entry of the append-only log, which says the
	// decision is open.
	lastEvent string
	// events is how many entries the log holds.
	events int
	// reported is the state the surface reported.
	reported string
	// revision is the revision the surface reported, which increases on every
	// write of the authoritative record and never on an append to the log.
	revision int64
	// source says what resolved the reported state.
	source string
}

// TestAnEventLogIsNotCurrentState drives PRD principle P8 through the binary
// that ships.
//
// Section 13's test is exactly this shape: given an event log whose last line
// says a decision is open, and a validation run that has moved past it, the
// resolver reports the run's state. The half about marking the event
// superseded has no mechanism here - internal/store's events carry no state
// field at all, which is how they are kept from being a second answer to the
// question - so what is driven is the half that exists, and the assertion that
// the answer is not the tail is made against a log whose tail says something
// else.
//
// Nothing in this build creates a task, so the records are written through
// internal/store's own accessors, which are the ones anything creating a task
// would use. What is driven end to end is the reading: the surface is asked,
// and the surface answers from the authoritative record.
func TestAnEventLogIsNotCurrentState(t *testing.T) {
	principles.Cite(t, principles.P8)

	j := inClone(t)
	written := records(t, j)

	const id = "journey-task"
	if _, err := written.CreateTask(t.Context(), store.Task{
		ID:           id,
		Shape:        store.TaskDelivery,
		Project:      "assistant",
		Mode:         "journey",
		WorktreePath: j.Dir(),
	}, "dispatched", "journey"); err != nil {
		t.Fatalf("recording the task: %v", err)
	}

	// The worker's own history. The last thing it appended says it is waiting
	// on a decision, which is the note a coordinator reading the tail would
	// act on.
	for _, event := range []struct{ kind, detail string }{
		{"started", "the worker began"},
		{"validation-started", "a run was started for this branch"},
		{"decision-open", "the run is holding on a decision nobody has answered"},
	} {
		if _, err := written.AppendTaskEvent(t.Context(), id, event.kind, event.detail); err != nil {
			t.Fatalf("appending %s: %v", event.kind, err)
		}
	}

	// The run moved on, and whatever owns the task's state recorded that. The
	// event log is not touched, because an event log is history.
	if _, err := written.SetTaskState(t.Context(), id, "validated", "run",
		store.Known("the run reached the end of the gate twenty minutes ago")); err != nil {
		t.Fatalf("recording the resolved state: %v", err)
	}

	log, err := written.TaskEvents(t.Context(), id, 0, 0)
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
	observed := resolvedState{
		lastEvent: log[len(log)-1].Kind,
		events:    len(log),
		reported:  reported.State.State,
		revision:  reported.State.Revision,
		source:    reported.State.Source,
	}

	authoritative := journey.Check[resolvedState]{
		What: "the surface reports a task's authoritative current state and its source, not the newest " +
			"entry of the append-only log, which says something else",
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
					if r.reported != "validated" {
						return fmt.Errorf("the surface reported %q and the authoritative record says %q",
							r.reported, "validated")
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
