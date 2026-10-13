package journey_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// leaked is the text of the open-decision event. A resolution that carries it
// has let a worker's words into the answer.
const leaked = "the run is holding on a decision nobody has answered"

// resolvedState is what the surface answered about a task whose event log ends
// on an open decision and whose attributed validation run has moved past it.
type resolvedState struct {
	// lastEvent is the kind of the newest entry of the append-only log, which
	// says the decision is open.
	lastEvent string
	// tailClaims is the state that newest entry would claim if it were read as
	// the answer.
	tailClaims taskstate.State
	// events is how many entries the log holds.
	events int
	// openDecision is the sequence of the decision-open entry.
	openDecision int64
	// reported is the state the surface reported.
	reported taskstate.State
	// source says which tier decided it.
	source taskstate.Source
	// detail is the detail the surface reported.
	detail string
	// superseded is what the surface said it overrode.
	superseded []taskstate.Superseded
}

// TestAnEventLogIsNotCurrentState drives PRD principle P8 through the binary
// that ships.
//
// Section 13's test is exactly this shape: given an event log whose last line
// says a decision is open, and a validation run that has moved past it, the
// resolver reports the run's state and marks the event superseded. The task is
// attributed to the run by repository and branch, the run is moved through
// internal/store's own transition, and the surface is asked; the state is not
// written anywhere, so the only way the surface can answer is by resolving.
//
// Nothing in this build creates a task or dispatches a worker, so the task
// and its history are written through internal/store's accessors, which are the
// ones anything creating a task would use. The run's moves are the store's
// TransitionRun rather than a run of the gate, which this harness reaches no
// review or push through. What is driven end to end is the reading.
func TestAnEventLogIsNotCurrentState(t *testing.T) {
	principles.Cite(t, principles.P8)

	j := inClone(t)
	written := records(t, j)

	const id, branch = "journey-task", "journey/p8"
	if _, err := written.UpsertRepository(t.Context(), store.Repository{
		ID: "p8-repo", WorkingPath: t.TempDir(), UpstreamURL: "https://example.test/p8.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	if _, err := written.CreateTask(t.Context(), store.Task{
		ID:           id,
		Shape:        store.TaskDelivery,
		Project:      "assistant",
		Mode:         "journey",
		WorktreePath: j.Dir(),
		RepositoryID: store.Known("p8-repo"),
		Branch:       store.Known(branch),
	}); err != nil {
		t.Fatalf("recording the task: %v", err)
	}

	// The worker's own history. The last thing it appended says it is waiting
	// on a decision, which is the note a coordinator reading the tail would
	// act on.
	for _, event := range []struct{ kind, detail string }{
		{"started", "the worker began"},
		{"handed-off", "the branch was pushed to the gate"},
		{"decision-open", leaked},
	} {
		if _, err := written.AppendTaskEvent(t.Context(), id, event.kind, event.detail); err != nil {
			t.Fatalf("appending %s: %v", event.kind, err)
		}
	}

	// The run on the task's branch moved on and finished. The log is not
	// touched, because an event log is history.
	if _, err := written.CreateRun(t.Context(), store.Run{
		ID: "p8-run", RepositoryID: "p8-repo", Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "journey", Revision: "0123456789abcdef", Go: "go"},
	}); err != nil {
		t.Fatalf("recording the run: %v", err)
	}
	for _, move := range []struct{ from, to store.RunStatus }{
		{store.RunPending, store.RunRunning},
		{store.RunRunning, store.RunHeld},
		{store.RunHeld, store.RunRunning},
		{store.RunRunning, store.RunPassed},
	} {
		if _, err := written.TransitionRun(t.Context(), "p8-run", []store.RunStatus{move.from}, move.to); err != nil {
			t.Fatalf("moving the run to %s: %v", move.to, err)
		}
	}

	log, err := written.TaskEvents(t.Context(), id, 0, 0)
	if err != nil {
		t.Fatalf("reading the task's history: %v", err)
	}
	if len(log) == 0 {
		t.Fatalf("the task's history is empty, so there is no tail for this to be about")
	}
	tail := log[len(log)-1]
	claim, _ := taskstate.Lookup(tail.Kind)

	var reported machine.Task
	if err := succeeds(t, j.Command("tasks", id)).Decode(&reported); err != nil {
		t.Fatalf("reading the task off the surface: %v", err)
	}
	observed := resolvedState{
		lastEvent:    tail.Kind,
		tailClaims:   claim.State,
		events:       len(log),
		openDecision: tail.Sequence,
		reported:     reported.State.State,
		source:       reported.State.Source,
		detail:       reported.State.Detail.Or(""),
		superseded:   reported.State.Superseded,
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
				States: "the surface reported a state this build defines",
				Holds: func(r resolvedState) error {
					if !r.reported.Recognized() {
						return fmt.Errorf("the surface reported %q, which is no state of the lifecycle", r.reported)
					}
					return nil
				},
			},
			{
				States: "the state the surface reported is not the one the newest entry of the log claims",
				Holds: func(r resolvedState) error {
					if r.reported == r.tailClaims {
						return fmt.Errorf("the surface reported %q, which is what the newest entry of the log "+
							"claims; reading the last line to decide what is true now is always wrong", r.reported)
					}
					return nil
				},
			},
			{
				States: "the state the surface reported is the one the finished run puts the task in",
				Holds: func(r resolvedState) error {
					if r.reported != taskstate.Ready {
						return fmt.Errorf("the surface reported %q and the run passed, which is %q",
							r.reported, taskstate.Ready)
					}
					return nil
				},
			},
			{
				States: "the state carries the run as the source that resolved it",
				Holds: func(r resolvedState) error {
					if r.source != taskstate.SourceRun {
						return fmt.Errorf("the state came from %q, not from the run that outranks the log", r.source)
					}
					return nil
				},
			},
			{
				States: "the open decision is reported as superseded, and only it",
				Holds: func(r resolvedState) error {
					if len(r.superseded) != 1 || r.superseded[0].Sequence != r.openDecision ||
						r.superseded[0].By != taskstate.SourceRun {
						return fmt.Errorf("the surface reported %+v superseded, want the open decision "+
							"(sequence %d) overridden by the run", r.superseded, r.openDecision)
					}
					return nil
				},
			},
			{
				States: "the detail does not carry the text of the decision event",
				Holds: func(r resolvedState) error {
					if strings.Contains(r.detail, leaked) {
						return fmt.Errorf("the detail %q carries the worker's decision text", r.detail)
					}
					return nil
				},
			},
		},
		Counterfeits: []journey.Counterfeit[resolvedState]{
			{Named: "the surface answered with what the newest entry of the log claims", Break: func(r resolvedState) resolvedState {
				r.reported = r.tailClaims
				return r
			}},
			{Named: "the surface answered with a state nobody defined", Break: func(r resolvedState) resolvedState {
				r.reported = "waiting"
				return r
			}},
			{Named: "the surface answered with another state of the lifecycle", Break: func(r resolvedState) resolvedState {
				r.reported = taskstate.Running
				return r
			}},
			{Named: "the surface said the log decided", Break: func(r resolvedState) resolvedState {
				r.source = taskstate.SourceEvent
				return r
			}},
			{Named: "the surface marked nothing superseded", Break: func(r resolvedState) resolvedState {
				r.superseded = nil
				return r
			}},
			{Named: "the surface let the decision event's text into the detail", Break: func(r resolvedState) resolvedState {
				r.detail += leaked
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
