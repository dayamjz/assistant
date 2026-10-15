package machine_test

import (
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// Every answer the coordinator client reads out reads as a row TablePhrases
// lists, so the vocabulary test holds it, and the rows that decide between
// two situations do decide: a task recorded behind a blocker reads apart
// from one a worker is being begun for, an empty queue apart from one with
// notices, and arm verification as a live loop exactly for the two
// outcomes that say so, with no service apart from every other failure.
// The positive control is a row nobody listed, which the membership check
// must report as missing.
func TestEveryCoordinatorAnswerReadsAsAListedRow(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	listed := machine.TablePhrases()
	if slices.Contains(listed, machine.Phrase{Outcome: "Recorded.", Consequence: "Nothing.", Decision: "Nothing to decide."}) {
		t.Fatal("the control row nobody listed reads as listed, so the membership check checks nothing")
	}
	behind := machine.TaskCreated{Record: store.Task{BlockerTask: store.Known("abcdefghij23")}}
	queue := machine.Wakes{Wakes: []store.Wake{{ID: 1}, {ID: 2}}}
	rows := map[string]machine.Phrase{
		"created":      machine.TaskCreatedPhrase(machine.TaskCreated{}),
		"behind":       machine.TaskCreatedPhrase(behind),
		"promoted":     machine.TaskPromotedPhrase(machine.TaskPromoted{}),
		"relaunched":   machine.TaskRelaunchedPhrase(machine.TaskRelaunched{}),
		"no notices":   machine.WakesPhrase(machine.Wakes{}),
		"acknowledged": machine.AcknowledgedPhrase(machine.Acknowledged{Wakes: []int64{1}}),
	}
	for name, row := range rows {
		vocabtest.Assert(t, row.String())
		if !slices.Contains(listed, row) {
			t.Errorf("the %s row %q is not one TablePhrases lists", name, row)
		}
	}
	// The counted row is listed with its placeholder filled, so it is held
	// as a template and checked for the count it was given.
	counted := machine.WakesPhrase(queue)
	vocabtest.Assert(t, counted.String())
	if counted == rows["no notices"] || !slices.Contains(listed, machine.WakesPhrase(machine.Wakes{Wakes: make([]store.Wake, 1)})) {
		t.Errorf("a queue with notices reads %q, and the table must list its row apart from the empty queue's", counted)
	}
	if rows["created"] == rows["behind"] {
		t.Errorf("a task recorded behind a blocker reads the same as one a worker is begun for: %q", rows["created"])
	}

	watched := machine.ArmPhrase(supervisor.Arm{Outcome: supervisor.ArmAttached})
	noService := machine.ArmPhrase(supervisor.Arm{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmServiceDown})
	for _, arm := range []supervisor.Arm{
		{Outcome: supervisor.ArmStarted}, {Outcome: supervisor.ArmAttached},
		{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmServiceDown},
		{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmNoBeacon},
		{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmInstanceMismatch},
		{Outcome: supervisor.ArmFailed, Reason: supervisor.ArmLoopStale},
		{Outcome: "never-an-outcome"},
	} {
		row := machine.ArmPhrase(arm)
		if !slices.Contains(listed, row) {
			t.Errorf("%+v reads %q, which TablePhrases does not list", arm, row)
		}
		live := arm.Outcome == supervisor.ArmStarted || arm.Outcome == supervisor.ArmAttached
		if live != (row == watched) {
			t.Errorf("%+v reads %q; only a live loop reads as watched", arm, row)
		}
		if (arm.Reason == supervisor.ArmServiceDown) != (row == noService) {
			t.Errorf("%+v reads %q; only a verification that found no service reads as one to start", arm, row)
		}
	}
}
