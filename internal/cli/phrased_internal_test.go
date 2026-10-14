package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// fixtureTask is a task whose record carries every word the old rendering
// printed and P13 forbids: an identifier in the generator's format, a copy
// path under a home, a state label and its source.
func fixtureTask() machine.Task {
	return machine.Task{
		Record: store.Task{
			ID: "abcdefghij23", Shape: store.TaskDelivery, Project: "the parser",
			Mode: "headless", WorktreePath: filepath.Join(vocabtest.SampleRoot(), "tasks", "abcdefghij23", "copy"),
			Branch: store.Known("feature/parser"),
		},
		State: taskstate.Resolution{
			TaskID: "abcdefghij23", State: taskstate.Paused, Source: taskstate.SourceEvent,
			Basis: taskstate.Basis{EventSequence: 3, EventKind: "waiting-external", Process: taskstate.Unobserved},
		},
		Waiting: "an upstream release of the parser",
	}
}

// fixtureRun is a run at a hold, with no position, no stage views and no
// decision, so what it renders is the phrased head alone; the details and
// the decision are the two renderers that are not phrased.
func fixtureRun() machine.Run {
	return machine.Run{
		Record: store.Run{
			ID: "abcdefghij23", RepositoryID: "repo", Branch: "feature/parser", Status: store.RunHeld,
			ResolvedAgent: store.Known("claude"), CreatedAt: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC),
		},
		Outcome: machine.OutcomeDecision,
		Cleanup: &store.RunCleanup{Run: "abcdefghij23", Refusal: store.Known("unproven"), Detail: "the branch is not on any remote"},
	}
}

// fixtureStatus is a status carrying the home root, the socket, the
// repository's and the gate's identifiers and the head, with the fixture run
// active.
func fixtureStatus() machine.Status {
	root := vocabtest.SampleRoot()
	run := fixtureRun()
	return machine.Status{
		Home:       root,
		Service:    machine.Service{Running: true, Socket: filepath.Join(root, "socket")},
		Repository: &store.Repository{ID: "repo", WorkingPath: "/somewhere/parser", UpstreamURL: "https://example.test/parser.git", DefaultBranch: "main"},
		Gate:       &machine.Gate{Present: true, ID: "gate"},
		Branch:     &machine.Branch{WorkingPath: "/somewhere/parser", Name: "feature/parser", Head: "0123456789abcdef0123456789abcdef01234567"},
		ActiveRun:  &run,
	}
}

// Every phrased renderer, over a fixture carrying the words it must not
// print, writes nothing from the derived forbidden set; the lines the old
// renderers wrote for the same fixtures are caught, which is the other
// direction; and the structured rendering of the same values keeps every
// identifier, because the machine interface keeps them by design. The
// enumeration is the renderer table's, so a renderer added there as phrased
// is held here without being named.
func TestThePhrasedRenderersSpeakTheTableAndTheJSONKeepsIdentifiers(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	task := fixtureTask()
	run := fixtureRun()
	status := fixtureStatus()
	fixtures := map[string]any{
		"status":  status,
		"run":     run,
		"runs":    machine.Runs{Runs: []store.Run{run.Record}},
		"tasks":   machine.Tasks{Tasks: []machine.Task{task}},
		"task":    task,
		"service": status.Service,
		"event":   mustEvent(t, ipc.TypeTaskState, task),
	}
	phrased := 0
	for _, r := range Renderings() {
		if !r.Phrased {
			continue
		}
		phrased++
		value, ok := fixtures[r.Name]
		if !ok {
			t.Fatalf("the renderer %q is phrased and this test has no fixture for it", r.Name)
		}
		var out bytes.Buffer
		readOut(&out, value)
		if out.Len() == 0 {
			t.Errorf("%s rendered nothing", r.Name)
		}
		for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
			vocabtest.Assert(t, line)
		}
		t.Logf("%s:\n%s", r.Name, out.String())

		var document bytes.Buffer
		if err := machine.NewEncoder(&document).Encode(value); err != nil {
			t.Fatalf("encoding the %s fixture: %v", r.Name, err)
		}
		if n := len(vocabtest.Violations(t, document.String())); n == 0 {
			t.Errorf("the structured rendering of %s carries no identifier, path or label, so this fixture holds the human rendering to nothing", r.Name)
		}
	}
	if phrased < 7 {
		t.Fatalf("%d renderers are phrased; the table claims seven", phrased)
	}

	// The other direction: the lines these renderers used to write, built
	// here from the same fixtures the way the old renderers built them, are
	// caught by the derivation.
	for name, old := range map[string]string{
		"task":    fmt.Sprintf("%s  %-10s %-12s %-8s %s", task.Record.ID, task.Record.Shape, task.State.State, task.State.Source, task.Record.Project),
		"run":     fmt.Sprintf("Run        %s on %s (%s)", run.Record.ID, run.Record.Branch, run.Record.Status),
		"runs":    fmt.Sprintf("%s  %-10s %-24s", run.Record.ID, run.Record.Status, run.Record.Branch),
		"status":  fmt.Sprintf("Home       %s", status.Home),
		"service": fmt.Sprintf("Service    running on %s", status.Service.Socket),
		"event":   fmt.Sprintf("%s (revision %d) %s", ipc.TypeTaskState, 1, mustEvent(t, ipc.TypeTaskState, task).Payload),
	} {
		if len(vocabtest.Violations(t, old)) == 0 {
			t.Errorf("the old %s line %q is not caught, so passing the new one proves nothing", name, old)
		}
	}
}

// A task with no run shows its own state and no outcome word; a paused task
// shows what the worker said it is waiting on and never the word; and the
// task line carries neither the identifier nor the copy path.
func TestATaskLineIsItsPhraseAndNothingOfTheRecord(t *testing.T) {
	t.Parallel()
	task := fixtureTask()
	var out bytes.Buffer
	readOut(&out, task)
	line := out.String()
	for _, want := range []string{"the parser: ", task.Waiting} {
		if !strings.Contains(line, want) {
			t.Errorf("the task line %q does not carry %q", line, want)
		}
	}
	for _, forbidden := range []string{task.Record.ID, task.Record.WorktreePath, string(task.State.State), string(task.State.Source)} {
		if strings.Contains(line, forbidden) {
			t.Errorf("the task line %q carries %q", line, forbidden)
		}
	}
	for _, outcome := range machine.Outcomes() {
		if strings.Contains(" "+line+" ", " "+string(outcome)+" ") || strings.Contains(line, string(outcome)+".") {
			t.Errorf("the task line %q carries the outcome word %q, and a task with no run has no outcome", line, outcome)
		}
	}
	// The same task through --json keeps all of it.
	var document bytes.Buffer
	if err := machine.NewEncoder(&document).Encode(task); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{task.Record.ID, task.Record.WorktreePath, string(task.State.State), task.Waiting} {
		if !strings.Contains(document.String(), want) {
			t.Errorf("the structured rendering lost %q:\n%s", want, document.String())
		}
	}
}

// A run's head is its phrase, its branch, its agent and the kept-copy line
// from its cleanup refusal; the details beneath it are the renderer that is
// not phrased, and this says so by showing exactly which lines carry a stage
// name once stage views are added.
func TestARunsHeadIsPhrasedAndItsDetailsAreNot(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	run := fixtureRun()
	var out bytes.Buffer
	readOut(&out, run)
	head := out.String()
	kept, ok := machine.CleanupPhrase(*run.Cleanup)
	if !ok || !strings.Contains(head, kept.String()) {
		t.Fatalf("the head does not carry the kept-copy line:\n%s", head)
	}
	if !strings.Contains(head, run.Phrase().String()) || !strings.Contains(head, "Branch     feature/parser") || !strings.Contains(head, "Agent      claude") {
		t.Fatalf("the head is missing the phrase, the branch or the agent:\n%s", head)
	}
	for _, forbidden := range []string{run.Record.ID, string(run.Record.Status), "Outcome    ", "Next:"} {
		if strings.Contains(head, forbidden) {
			t.Errorf("the head carries %q:\n%s", forbidden, head)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(head, "\n"), "\n") {
		vocabtest.Assert(t, line)
	}

	// With a position and stage views, the details carry a node name and
	// stage names: that renderer is the one Renderings lists unphrased, and
	// this is the line that says so.
	halted := graph.StatusHalted
	withStages := run
	withStages.Progress, withStages.Position = &halted, "hold:test"
	withStages.Stages = []machine.Stage{{Stage: "test", Outcome: "passed", Ran: true}}
	out.Reset()
	readOut(&out, withStages)
	if !strings.Contains(out.String(), "Position   hold:test") || !strings.Contains(out.String(), "\nStages\n  test ") {
		t.Fatalf("the details do not carry the position and the stage:\n%s", out.String())
	}
	if len(vocabtest.Violations(t, "Position   hold:test after 0 of 0 steps")) == 0 || len(vocabtest.Violations(t, "  test      passed")) == 0 {
		t.Fatal("neither detail line carries a forbidden word, so the details renderer could be claimed phrased")
	}
	for _, r := range Renderings() {
		if r.Name == "details" && r.Phrased {
			t.Fatal("the details renderer is listed as phrased and prints stage names")
		}
	}
}

// Every event type this build classifies has a phrase, with the domain
// derived from internal/ipc's table: the three records read as their
// phrases, a worker's history entry as added and not relayed, the stream's
// two control events as theirs, activity as output that arrived, and the
// two state types nothing here produces, like a type this build does not
// know, as an update the view cannot describe. A payload
// that does not read as its type reads the same way rather than as nothing.
func TestEveryEventTypeHasAPhrase(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	task := fixtureTask()
	run := fixtureRun().Record
	hold := store.Hold{Key: "k", RunID: store.Known(run.ID), Subject: "whether the migration may drop the column"}
	payloads := map[ipc.Type]any{ipc.TypeRunState: run, ipc.TypeTaskState: task, ipc.TypeHoldState: hold}
	types := ipc.Types()
	if len(types) == 0 {
		t.Fatal("internal/ipc classifies no types")
	}
	for _, typ := range types {
		event := ipc.Event{Type: typ, Revision: 7}
		if payload, ok := payloads[typ]; ok {
			event = mustEvent(t, typ, payload)
		}
		line := eventLine(event)
		if line == "" {
			t.Errorf("%s renders as nothing", typ)
		}
		vocabtest.Assert(t, line)
		if strings.Contains(line, string(typ)) || strings.Contains(line, "revision") {
			t.Errorf("%s renders its own type or revision: %q", typ, line)
		}
		switch typ {
		case ipc.TypeRunState:
			if !strings.Contains(line, "feature/parser: ") || !strings.Contains(line, machine.RecordPhrase(run).String()) {
				t.Errorf("a run's state renders %q", line)
			}
		case ipc.TypeTaskState:
			if !strings.Contains(line, "the parser: ") || !strings.Contains(line, task.Waiting) {
				t.Errorf("a task's state renders %q", line)
			}
		case ipc.TypeHoldState:
			if !strings.Contains(line, hold.Subject) || !strings.Contains(line, "waiting on you") {
				t.Errorf("a hold renders %q", line)
			}
		case ipc.TypeTaskEvent:
			if line != machine.PhraseWorkerSpoke.String() {
				t.Errorf("a worker's history entry renders %q", line)
			}
		case ipc.TypeGap:
			if line != machine.PhraseUpdatesMissed.String() {
				t.Errorf("a gap renders %q", line)
			}
		case ipc.TypeServiceStopping:
			if line != machine.PhraseServiceStopping.String() {
				t.Errorf("the service stopping renders %q", line)
			}
		default:
			want := machine.PhraseUnfamiliarUpdate.String()
			if ipc.Classify(typ) == ipc.ClassActivity {
				want = machine.PhraseActivity.String()
			}
			if line != want {
				t.Errorf("%s renders %q, want %q", typ, line, want)
			}
		}
	}
	for _, typ := range []ipc.Type{ipc.TypeRunState, ipc.TypeTaskState, ipc.TypeHoldState} {
		if got := eventLine(ipc.Event{Type: typ, Revision: 1, Payload: json.RawMessage(`"not the record"`)}); got != machine.PhraseUnfamiliarUpdate.String() {
			t.Errorf("%s over a payload that is not its record renders %q", typ, got)
		}
	}
	if got := eventLine(ipc.Event{Type: "never.a.type", Revision: 1}); got != machine.PhraseUnfamiliarUpdate.String() {
		t.Errorf("a type this build does not know renders %q", got)
	}
}

// mustEvent builds an event carrying a record, the way a publisher does.
func mustEvent(t *testing.T, typ ipc.Type, payload any) ipc.Event {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encoding a %s payload: %v", typ, err)
	}
	return ipc.Event{Type: typ, Revision: 1, Payload: body}
}
