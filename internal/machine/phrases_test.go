package machine_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Every row of the table is three sentences, each present, and none carries a
// word from the derived forbidden set. The first half is structural: a row is
// a Phrase with three fields, and a field left empty fails here without
// anyone reading the prose. The second is vocabtest's, with its positive
// control run beside it so a derivation that produced nothing cannot hold the
// rows to nothing. The claim is the same one vocabulary_test.go cites P13
// for, the phrases in this table; which renderers speak through them is
// internal/cli's, and the citation does not widen here.
func TestEveryRowHasThreeSentencesAndNoMechanics(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	rows := machine.TablePhrases()
	if len(rows) == 0 {
		t.Fatal("the table is empty, so nothing is held to anything")
	}
	for _, row := range rows {
		for name, part := range map[string]string{"outcome": row.Outcome, "consequence": row.Consequence, "decision": row.Decision} {
			if strings.TrimSpace(part) == "" {
				t.Errorf("the row %q has no %s", row, name)
			}
		}
		vocabtest.Assert(t, row.String())
	}
	// Phrases is the same rows as strings, so a caller walking one walks
	// the other.
	if got, want := machine.Phrases(), rows; len(got) != len(want) {
		t.Fatalf("Phrases lists %d rows and TablePhrases %d", len(got), len(want))
	} else {
		for i := range want {
			if got[i] != want[i].String() {
				t.Errorf("row %d: Phrases says %q and TablePhrases %q", i, got[i], want[i])
			}
		}
	}
	// A row with a sentence missing is caught by the structural half, in the
	// shape a careless row would take.
	careless := machine.Phrase{Outcome: "The work merged."}
	if careless.Consequence != "" || careless.Decision != "" {
		t.Fatal("the positive control row is not missing anything")
	}
}

// Every state internal/taskstate can resolve has a row, with the domain
// derived from its table rather than listed here, so a state added there
// fails until it is phrased. No row carries the state's own word, which is
// the forbidden set's half, nor an outcome word, which is PRD section 6's
// rule that a task's state is its own vocabulary and not a second rendering
// of a run's outcome: a task with no run shows its phrase and no outcome
// word, and the outcome set is derived from its owner.
func TestEveryTaskStateHasAPhraseWithNoStateOrOutcomeWord(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	states := taskstate.States()
	if len(states) < 12 {
		t.Fatalf("taskstate.States lists %d states; the lifecycle has twelve", len(states))
	}
	for _, state := range states {
		task := machine.Task{State: taskstate.Resolution{State: state, Source: taskstate.SourceEvent}}
		phrase := machine.TaskPhrase(task)
		if strings.TrimSpace(phrase.Outcome) == "" || strings.TrimSpace(phrase.Consequence) == "" || strings.TrimSpace(phrase.Decision) == "" {
			t.Errorf("%s has no complete phrase: %+v", state, phrase)
		}
		text := phrase.String()
		vocabtest.Assert(t, text)
		if wholeWord(text, string(state)) {
			t.Errorf("%s's phrase %q carries the state's own word", state, text)
		}
		for _, outcome := range machine.Outcomes() {
			if wholeWord(text, string(outcome)) {
				t.Errorf("%s's phrase %q carries the outcome word %q; a task's state is not a run's outcome", state, text, outcome)
			}
		}
	}
	// A state outside the set reads as the Unknown row, never as nothing.
	if got := machine.TaskPhrase(machine.Task{State: taskstate.Resolution{State: "never-a-state"}}); got != machine.TaskPhrase(machine.Task{State: taskstate.Resolution{State: taskstate.Unknown}}) {
		t.Errorf("a state outside the set reads as %q, want the undecided row", got)
	}
	// The derivation catches a line in the shape the renderer this table
	// replaced produced: identifier, shape, state label, source, project.
	careless := "abcdefghij23  delivery  paused  event  the project (waiting-external)"
	if n := len(vocabtest.Violations(t, careless)); n < 3 {
		t.Fatalf("the old task line was caught %d times, want its identifier, state and event-kind words: %v", n, vocabtest.Violations(t, careless))
	}
}

// Paused is never shown as a word: the row is what the worker said it is
// waiting on, relayed, with the placeholder when the worker said nothing. And
// a task whose newest entry is a note the program appended, which resolves
// Unknown at the event tier, is not a failure: it is no word from the worker
// yet, and only a program's note reads that way.
func TestPausedIsTheWorkersWaitAndANoteTailIsNoWordYet(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	paused := machine.Task{
		State:   taskstate.Resolution{State: taskstate.Paused, Source: taskstate.SourceEvent},
		Waiting: "an upstream release of the parser",
	}
	got := machine.TaskPhrase(paused).String()
	if !strings.Contains(got, "an upstream release of the parser") {
		t.Fatalf("the phrase %q does not say what the worker is waiting on", got)
	}
	if wholeWord(got, string(taskstate.Paused)) {
		t.Fatalf("the phrase %q shows the state as a word", got)
	}
	vocabtest.Assert(t, got)
	if silent := machine.TaskPhrase(machine.Task{State: paused.State}).String(); !strings.Contains(silent, "something it named") {
		t.Fatalf("a worker that said nothing is phrased %q, want the placeholder", silent)
	}

	for _, kind := range []string{store.PromotedEventKind, "relaunched"} {
		row, ok := taskstate.Lookup(kind)
		if !ok || row.Role != taskstate.RoleNote || row.Writer != taskstate.WriterProgram {
			t.Fatalf("%q is not a program note in internal/taskstate's table; this is about those", kind)
		}
		noted := machine.Task{State: taskstate.Resolution{
			State: taskstate.Unknown, Source: taskstate.SourceNone, Basis: taskstate.Basis{EventKind: kind},
		}}
		if got := machine.TaskPhrase(noted); got != machine.NoWordYet {
			t.Errorf("after %q the task is phrased %q, want no word from the worker yet", kind, got)
		}
	}
	// A worker's own note is the worker reporting, and the undecided row
	// stands; so does Unknown decided by a tier other than the log.
	workerNote := machine.Task{State: taskstate.Resolution{
		State: taskstate.Unknown, Source: taskstate.SourceNone, Basis: taskstate.Basis{EventKind: "progress-note"},
	}}
	if got := machine.TaskPhrase(workerNote); got == machine.NoWordYet {
		t.Error("a worker's own note reads as no word from the worker")
	}
	dead := machine.Task{State: taskstate.Resolution{
		State: taskstate.Unknown, Source: taskstate.SourceProcess, Basis: taskstate.Basis{EventKind: "relaunched"},
	}}
	if got := machine.TaskPhrase(dead); got == machine.NoWordYet {
		t.Error("a terminal the process tier found stopped reads as no word from the worker")
	}
	if !strings.Contains(machine.NoWordYet.String(), "no word from the worker") {
		t.Errorf("the row says %q", machine.NoWordYet)
	}
}

// Every outcome has a row, derived from the closed set, and RunPhrase chooses
// the same way NextActionOf does: an advancing run, a stalled one at a
// position, and one with no position read differently, and every other
// standing reads by its outcome. RecordPhrase, over a record alone, never
// sharpens.
func TestEveryOutcomeHasAPhraseAndTheStandingSharpensIt(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, phrase := range machine.Phrases() {
		seen[phrase] = true
	}
	for _, outcome := range machine.Outcomes() {
		standing, ok := standingFor(outcome)
		if !ok {
			t.Fatalf("no standing here produces %s; the table is derived from Outcomes and this test has to reach each", outcome)
		}
		if got := machine.OutcomeOf(standing); got != outcome {
			t.Fatalf("the standing for %s reads as %s", outcome, got)
		}
		phrase := machine.RunPhrase(standing)
		if !seen[phrase.String()] {
			t.Errorf("%s's phrase %q is not in Phrases, so nothing held it to the rule", outcome, phrase)
		}
	}
	running := machine.Standing{Record: store.RunRunning, Execution: machine.ExecutionAt(graph.StatusRunning, graph.State{})}
	advancing := machine.RunPhrase(machine.Standing{Record: running.Record, Execution: running.Execution, Advancing: true})
	stalled := machine.RunPhrase(running)
	unrecorded := machine.RunPhrase(machine.Standing{Record: store.RunRunning, Execution: machine.ExecutionUnrecorded()})
	if advancing == stalled || stalled == unrecorded || advancing == unrecorded {
		t.Fatalf("the three executing runs read alike:\n%q\n%q\n%q", advancing, stalled, unrecorded)
	}
	for _, p := range []machine.Phrase{advancing, stalled, unrecorded} {
		if !seen[p.String()] {
			t.Errorf("the executing row %q is not in Phrases", p)
		}
	}
	listed := machine.RecordPhrase(store.Run{Status: store.RunRunning})
	if listed == advancing || listed == stalled || listed == unrecorded {
		t.Fatalf("a record alone reads as one of the sharpened rows: %q", listed)
	}
	if !seen[listed.String()] {
		t.Errorf("the record row %q is not in Phrases", listed)
	}
	if got := machine.RecordPhrase(store.Run{Status: "never-a-status"}); got != machine.RecordPhrase(store.Run{Status: store.RunFailed}) {
		t.Errorf("a status outside the set reads as %q, want the row for a run without a verdict", got)
	}
}

// standingFor is one standing per outcome, written here so the table's
// domain is reached through OutcomeOf rather than by naming rows.
func standingFor(outcome machine.Outcome) (machine.Standing, bool) {
	halted := machine.ExecutionAt(graph.StatusHalted, graph.State{})
	switch outcome {
	case machine.OutcomeChecksPassed:
		return machine.Standing{Record: store.RunPassed}, true
	case machine.OutcomePassed:
		return machine.Standing{Record: store.RunPassed, PullRequestEnd: store.PullRequestMerged}, true
	case machine.OutcomeFailed:
		return machine.Standing{Record: store.RunFailed}, true
	case machine.OutcomeCancelled:
		return machine.Standing{Record: store.RunTerminated}, true
	case machine.OutcomeDecision:
		return machine.Standing{Record: store.RunHeld, Execution: halted}, true
	case machine.OutcomeExecuting:
		return machine.Standing{Record: store.RunRunning, Execution: machine.ExecutionUnrecorded()}, true
	}
	return machine.Standing{}, false
}

// A hold reads as a decision waiting while it is open and as one answered
// once it carries a resolution, with the subject filled in; a cleanup record
// reads as the copy kept on a refusal and removed on a proof's removal, and a
// proof awaiting removal reads as nothing.
func TestHoldAndCleanupRowsFollowTheRecord(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	open := store.Hold{Key: "k", Subject: "whether the migration may drop the column"}
	answered := open
	answered.Resolution = store.Known("approved")
	if got := machine.HoldPhrase(open).String(); !strings.Contains(got, open.Subject) || !strings.Contains(got, "waiting on you") {
		t.Errorf("an open hold reads %q", got)
	}
	if got := machine.HoldPhrase(answered).String(); !strings.Contains(got, open.Subject) || !strings.Contains(got, "answered") {
		t.Errorf("an answered hold reads %q", got)
	}
	if machine.HoldPhrase(open) == machine.HoldPhrase(answered) {
		t.Error("an open and an answered hold read alike")
	}
	for _, h := range []store.Hold{open, answered} {
		vocabtest.Assert(t, machine.HoldPhrase(h).String())
	}

	kept := store.RunCleanup{Run: "r", Refusal: store.Known("unproven")}
	removed := store.RunCleanup{Run: "r", Proof: store.Known(store.CleanupProof("pushed")), RemovedAt: store.Known(kept.RecordedAt)}
	pending := store.RunCleanup{Run: "r", Proof: store.Known(store.CleanupProof("pushed"))}
	if p, ok := machine.CleanupPhrase(kept); !ok || !strings.Contains(p.String(), "was kept") {
		t.Errorf("a kept copy reads %q, %v", p, ok)
	}
	if p, ok := machine.CleanupPhrase(removed); !ok || !strings.Contains(p.String(), "was removed") {
		t.Errorf("a removed copy reads %q, %v", p, ok)
	}
	if p, ok := machine.CleanupPhrase(pending); ok {
		t.Errorf("a proof awaiting its removal reads %q, want nothing", p)
	}
}

// wholeWord reports whether text carries word as a whole word, without
// regard to case, which is how vocabtest matches a term.
func wholeWord(text, word string) bool {
	return regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(word) + `\b`).MatchString(text)
}

// A task the hold tier resolved Held waits on a hold naming the task, which
// is answered given the task, so its row says that it waits on the person and
// names --task and --answer. The control is Held from the run tier, a run's
// decision answered from its branch, which keeps the table's row and names no
// --task; and an open hold naming a task says the same command where one
// naming a run does not.
func TestAHeldTaskSaysItWaitsOnYouAndHowToAnswer(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	byHold := machine.TaskPhrase(machine.Task{State: taskstate.Resolution{State: taskstate.Held, Source: taskstate.SourceHold}}).String()
	for _, want := range []string{"waiting on an answer from you", "assistant --task", "--answer", "assistant tasks --json"} {
		if !strings.Contains(byHold, want) {
			t.Errorf("the hold tier's row %q does not say %q", byHold, want)
		}
	}
	vocabtest.Assert(t, byHold)
	if wholeWord(byHold, string(taskstate.Held)) {
		t.Errorf("the row %q carries the state's own word", byHold)
	}
	byRun := machine.TaskPhrase(machine.Task{State: taskstate.Resolution{State: taskstate.Held, Source: taskstate.SourceRun}}).String()
	if byRun == byHold || strings.Contains(byRun, "--task") {
		t.Errorf("a held run's task reads %q, which is the hold tier's row", byRun)
	}

	taskHold := machine.HoldPhrase(store.Hold{Subject: "whether to begin this work again", TaskID: store.Known("abcdefghij23")}).String()
	if !strings.Contains(taskHold, "assistant --task") || !strings.Contains(taskHold, "--answer") {
		t.Errorf("an open hold naming a task reads %q, which does not say how to answer it", taskHold)
	}
	vocabtest.Assert(t, taskHold)
	runHold := machine.HoldPhrase(store.Hold{Subject: "approve", TaskID: store.Known("abcdefghij23"), RunID: store.Known("r")}).String()
	if strings.Contains(runHold, "--task") {
		t.Errorf("an open hold naming a run reads %q, which points at the task's answer", runHold)
	}
}
