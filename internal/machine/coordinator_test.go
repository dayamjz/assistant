package machine_test

import (
	"slices"
	"testing"
	"time"

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
	briefed := machine.TaskPromoted{Brief: machine.Briefing{Message: &store.Message{WrittenAt: store.Known(time.Now())}}}
	refused := machine.TaskPromoted{Brief: machine.Briefing{Message: &store.Message{RefusedAt: store.Known(time.Now())}, Unsent: "refused"}}
	queue := machine.Wakes{Wakes: []store.Wake{{ID: 1}, {ID: 2}}}
	rows := map[string]machine.Phrase{
		"created":      machine.TaskCreatedPhrase(machine.TaskCreated{}),
		"behind":       machine.TaskCreatedPhrase(behind),
		"promoted":     machine.TaskPromotedPhrase(machine.TaskPromoted{}),
		"briefed":      machine.TaskPromotedPhrase(briefed),
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
	if rows["promoted"] == rows["briefed"] || machine.TaskPromotedPhrase(refused) != rows["promoted"] {
		t.Errorf("a brief written reads %q, and one refused or never sent must read apart from it", rows["briefed"])
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

// A person's listing names every wake kind internal/supervisor's table holds
// that can reach the queue, which is every kind but one whose class always
// absorbs, in a row of its own, each listed in TablePhrases so the vocabulary test
// holds it, and says whether the coordinator has been handed the row. The
// positive control is a kind outside the table, which reads as the
// unfamiliar row rather than as any kind's.
func TestAListedNoticeReadsAsItsKindsRow(t *testing.T) {
	table := machine.TablePhrases()
	unfamiliar := machine.QueuedWakePhrase(machine.QueuedWake{Wake: store.Wake{Kind: "a-kind-nobody-defined"}})
	if !slices.Contains(table, unfamiliar) {
		t.Fatalf("the unfamiliar row %q is not in TablePhrases", unfamiliar)
	}
	seen := map[machine.Phrase]supervisor.Kind{}
	for _, row := range supervisor.Kinds() {
		if row.Class == supervisor.ClassAbsorb {
			continue
		}
		got := machine.QueuedWakePhrase(machine.QueuedWake{Wake: store.Wake{Kind: string(row.Kind)}})
		if got == unfamiliar {
			t.Errorf("wake kind %s reads as the unfamiliar row", row.Kind)
		}
		if other, ok := seen[got]; ok && got != unfamiliar {
			t.Errorf("wake kinds %s and %s read as the same row %q", other, row.Kind, got)
		}
		seen[got] = row.Kind
		if !slices.Contains(table, got) {
			t.Errorf("the row for %s is not in TablePhrases: %q", row.Kind, got)
		}
	}
	handed := machine.QueuedWake{Wake: store.Wake{HandedOutAt: store.Known(time.Now())}}
	if machine.QueuedWakeStanding(handed) == machine.QueuedWakeStanding(machine.QueuedWake{}) {
		t.Error("a row handed out and one nobody was handed read alike")
	}
	if machine.QueuedWakesPhrase(machine.QueuedWakes{}) == machine.QueuedWakesPhrase(machine.QueuedWakes{Wakes: make([]machine.QueuedWake, 1)}) {
		t.Error("an empty queue and one with a notice read alike")
	}
}
