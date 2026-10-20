package cli_test

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// A run waiting on a decision lists as waiting on one. The listing used to
// read every unfinished run from its record alone, which cannot tell a run at
// a hold from one in validation, so a person reading assistant runs was told
// a run was in validation while it was waiting on them. The listing now
// carries each run's standing beside its record and reads it through the
// chooser a run's own answer uses.
//
// The control is the same run listed while a stage body is holding it open,
// which lists as being validated now and not as waiting on a decision, so the
// decision row is about the standing and not about every unfinished run. The
// record row that used to be printed for both appears for neither. P13 is
// both listings held to the phrase table's forbidden set.
func TestARunWaitingOnADecisionListsAsWaitingOnOne(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P13)
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)

	inside := make(chan struct{})
	release := make(chan struct{})
	var entered, released sync.Once
	let := func() { released.Do(func() { close(release) }) }
	serveHeldAtIntent(t, h, inside, release, &entered)
	t.Cleanup(let)

	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	standingOnChange(t, subject, "a-change")

	decision := machine.RunPhrase(machine.Standing{
		Record:    store.RunHeld,
		Execution: machine.ExecutionAt(graph.StatusHalted, graph.State{}),
	}).String()
	recordOnly := machine.RecordPhrase(store.Run{Status: store.RunHeld}).String()
	if decision == recordOnly {
		t.Fatalf("the decision row and the record row are the same sentence, so this test cannot tell them apart: %q", decision)
	}

	started := startInBackground(t, h, subject, "--json", "--intent", "a change with its tradeoffs stated")
	select {
	case <-inside:
	case <-time.After(30 * time.Second):
		t.Fatal("the start never reached the stage body")
	}

	// The control: the run is inside a stage body, so it lists as being
	// validated now.
	moving := run(t, h, subject, "runs")
	if moving.code != machine.ExitOK {
		t.Fatalf("assistant runs exited %s while a run was advancing:\n%s%s", moving.code, moving.stdout, moving.stderr)
	}
	t.Logf("assistant runs, while the run is inside a stage body:\n%s", moving.stdout)
	vocabtest.Assert(t, moving.stdout)
	if !strings.Contains(moving.stdout, advancingRendering) {
		t.Errorf("a run inside a stage body does not list as %q:\n%s", advancingRendering, moving.stdout)
	}
	for _, unwanted := range []string{decision, recordOnly} {
		if strings.Contains(moving.stdout, unwanted) {
			t.Errorf("a run inside a stage body lists as %q:\n%s", unwanted, moving.stdout)
		}
	}

	let()
	select {
	case got := <-started:
		if got.code != machine.ExitOK {
			t.Fatalf("the start exited %s:\n%s%s", got.code, got.stdout, got.stderr)
		}
		if advanced := decodeRun(t, got.stdout); advanced.Outcome != machine.OutcomeDecision {
			t.Fatalf("the start answered %s, want the run waiting on a decision", advanced.Outcome)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the start never returned after the stage body was released")
	}

	// The run now waits on a decision, and the listing says so.
	held := run(t, h, subject, "runs")
	if held.code != machine.ExitOK {
		t.Fatalf("assistant runs exited %s:\n%s%s", held.code, held.stdout, held.stderr)
	}
	t.Logf("assistant runs, with the run waiting on a decision:\n%s", held.stdout)
	vocabtest.Assert(t, held.stdout)
	if !strings.Contains(held.stdout, "a-change: "+decision) {
		t.Errorf("a run waiting on a decision does not list as %q:\n%s", decision, held.stdout)
	}
	for _, unwanted := range []string{recordOnly, advancingRendering} {
		if strings.Contains(held.stdout, unwanted) {
			t.Errorf("a run waiting on a decision lists as %q:\n%s", unwanted, held.stdout)
		}
	}

	// The structured listing carries the standing the line was read from,
	// beside the record and under the record's identifier.
	listed := run(t, h, subject, "--json", "runs")
	answer := decodeRuns(t, listed.stdout)
	if len(answer.Runs) != 1 || len(answer.Standings) != 1 {
		t.Fatalf("the listing carries %d run(s) and %d standing(s), want one of each:\n%s",
			len(answer.Runs), len(answer.Standings), listed.stdout)
	}
	if got := answer.Standings[0]; got.Run != answer.Runs[0].ID || got.Outcome != machine.OutcomeDecision {
		t.Fatalf("the listed standing is %+v, want %s for run %s", got, machine.OutcomeDecision, answer.Runs[0].ID)
	}
}

// decodeRuns reads a structured run listing.
func decodeRuns(t *testing.T, document string) machine.Runs {
	t.Helper()
	var answer machine.Runs
	if err := json.Unmarshal([]byte(document), &answer); err != nil {
		t.Fatalf("the runs answer does not decode: %v\n%s", err, document)
	}
	return answer
}
