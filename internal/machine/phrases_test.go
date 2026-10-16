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
	// A passed record with NoCodeHost known true phrases as pushed from the
	// row alone; without the flag it stays the checks-passed listing row.
	pushed := machine.RecordPhrase(store.Run{Status: store.RunPassed, NoCodeHost: store.Known(true)})
	cleared := machine.RecordPhrase(store.Run{Status: store.RunPassed})
	if pushed == cleared {
		t.Fatalf("a no-code-host passed record reads as the checks-passed listing: %q", pushed)
	}
	if !strings.Contains(pushed.String(), "on the remote") {
		t.Errorf("a no-code-host passed record reads %q, want the pushed row", pushed)
	}
	if !seen[pushed.String()] || !seen[cleared.String()] {
		t.Errorf("listing rows missing from Phrases: pushed %q cleared %q", pushed, cleared)
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
	case machine.OutcomePushed:
		// The durable half: a listing that has only the record still reaches
		// pushed. The checkpoint-state half remains covered by machine_test's
		// completed-with-no-code-host case.
		return machine.Standing{Record: store.RunPassed, NoCodeHost: store.Known(true)}, true
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
// names --task, --answer, --hold and --note. The control is Held from the run
// tier, a run's decision answered from its branch, which keeps the table's
// row and names no --task; and an open hold naming a task says the same
// command where one naming a run does not.
func TestAHeldTaskSaysItWaitsOnYouAndHowToAnswer(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	byHold := machine.TaskPhrase(machine.Task{State: taskstate.Resolution{State: taskstate.Held, Source: taskstate.SourceHold}}).String()
	for _, want := range []string{"waiting on an answer from you", "assistant --task", "--answer", "assistant tasks --json", "--hold", "--note"} {
		if !strings.Contains(byHold, want) {
			t.Errorf("the hold tier's row %q does not say %q", byHold, want)
		}
	}
	if strings.Contains(byHold, "--run") {
		t.Errorf("the hold tier's row %q names --run, which answers a run rather than a hold of the task's own", byHold)
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
	for _, want := range []string{"assistant --task", "--answer", "--hold", "--note"} {
		if !strings.Contains(taskHold, want) {
			t.Errorf("an open hold naming a task reads %q, which does not say %q", taskHold, want)
		}
	}
	vocabtest.Assert(t, taskHold)
	runHold := machine.HoldPhrase(store.Hold{Subject: "approve", TaskID: store.Known("abcdefghij23"), RunID: store.Known("r")}).String()
	if strings.Contains(runHold, "--task") {
		t.Errorf("an open hold naming a run reads %q, which points at the task's answer", runHold)
	}
}

// Every effect an answer to a task's hold can report reads as a row the
// table lists, so the vocabulary test holds it, and every effect that does
// more than close the hold has a row of its own, so two effects whose
// mechanisms differ cannot share a sentence. The rows are held to what
// their mechanisms do: a retry started by dispatch claims nothing about
// carrying on, because a dispatch gives the worker no note about the
// earlier attempt; a deferred retry promises nothing about the next start,
// because what that start does depends on why the retry was refused; and a
// retry the isolation assertion refused says nothing begins it again, which
// is what the recorded refusal makes true; and a retry refused on a record
// that cannot be dispatched says nothing begins it until the record is
// changed, which is what every path that begins an attempt reading that
// record first makes true, and promises nothing of the next start. The domain is AnswerEffects, so
// an effect added there fails here until it is phrased and listed. The
// positive control is a row nobody listed, which the membership check must
// report as missing.
func TestEveryAnswerEffectReadsAsItsOwnListedRow(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	listed := map[machine.Phrase]bool{}
	for _, row := range machine.TablePhrases() {
		listed[row] = true
	}
	unlisted := machine.Phrase{Outcome: "Your answer was taken.", Consequence: "Something happened.", Decision: "Nothing to decide."}
	if listed[unlisted] {
		t.Fatal("the control row nobody listed reads as listed, so the membership check checks nothing")
	}
	// The placeholder subject is the one TablePhrases fills HoldPhrase's rows
	// with, so an answer that only closed its hold is found in the table.
	hold := store.Hold{Subject: "a decision", Resolution: store.Known(machine.AnswerRetry)}
	seen := map[machine.Phrase]machine.AnswerEffect{}
	for _, effect := range machine.AnswerEffects() {
		row := machine.TaskAnsweredPhrase(machine.TaskAnswered{Hold: hold, Effect: effect})
		if !listed[row] {
			t.Errorf("%s reads %q, which TablePhrases does not list", effect, row)
		}
		vocabtest.Assert(t, row.String())
		if effect == machine.AnswerRecorded {
			if row != machine.HoldPhrase(hold) {
				t.Errorf("%s reads %q, want the answered hold's row", effect, row)
			}
			continue
		}
		if other, dup := seen[row]; dup {
			t.Errorf("%s and %s read alike: %q", effect, other, row)
		}
		seen[row] = effect
	}
	phrase := func(e machine.AnswerEffect) string {
		return machine.TaskAnsweredPhrase(machine.TaskAnswered{Hold: hold, Effect: e}).String()
	}
	for effect, rules := range map[machine.AnswerEffect]struct{ says, never []string }{
		machine.AnswerRedispatched: {
			says:  []string{"as that copy stands", "told nothing about the earlier attempt"},
			never: []string{"where it left off", "carries on", "already done"},
		},
		machine.AnswerRetryDeferred: {
			says:  []string{"could not be begun again just now", "may or may not begin it"},
			never: []string{"is begun again the next time"},
		},
		machine.AnswerRetryRefused: {
			says:  []string{"was not begun again", "did not pass the check", "Nothing begins it again on its own"},
			never: []string{"next time the service comes up", "just now"},
		},
		machine.AnswerLanded: {
			says:  []string{"without validation", "Nothing was shared"},
			never: []string{"merged", "cleared", "validated and"},
		},
		machine.AnswerRetryUndispatchable: {
			says:  []string{"was not begun again", "Nothing will begin it until that record is changed"},
			never: []string{"next time the service comes up", "just now", "may or may not", "did not pass the check"},
		},
	} {
		got := phrase(effect)
		for _, want := range rules.says {
			if !strings.Contains(got, want) {
				t.Errorf("%s reads %q, which does not say %q", effect, got, want)
			}
		}
		for _, bad := range rules.never {
			if strings.Contains(got, bad) {
				t.Errorf("%s reads %q, which promises %q", effect, got, bad)
			}
		}
	}
}

// RetryNotBegun is true exactly for the retries that began no work, with
// the oracle written from each effect's meaning rather than read off the
// method, and the domain AnswerEffects, so an effect added without a
// decision here fails.
func TestRetryNotBegunIsTheRetriesThatStartedNothing(t *testing.T) {
	t.Parallel()
	oracle := map[machine.AnswerEffect]bool{
		machine.AnswerRecorded:            false,
		machine.AnswerRelaunched:          false,
		machine.AnswerRedispatched:        false,
		machine.AnswerRetryDeferred:       true,
		machine.AnswerRetryHeld:           true,
		machine.AnswerRetryAtBound:        true,
		machine.AnswerRetryRefused:        true,
		machine.AnswerRetryUndispatchable: true,
		machine.AnswerStopped:             false,
		machine.AnswerLanded:              false,
	}
	effects := machine.AnswerEffects()
	if len(effects) != len(oracle) {
		t.Errorf("AnswerEffects lists %d effects and the oracle %d", len(effects), len(oracle))
	}
	for _, effect := range effects {
		want, ok := oracle[effect]
		if !ok {
			t.Errorf("%s has no entry in the oracle", effect)
			continue
		}
		if got := effect.RetryNotBegun(); got != want {
			t.Errorf("%s.RetryNotBegun() = %v, want %v", effect, got, want)
		}
	}
}

// A copy sweep record is phrased exactly when it did not establish the copy
// clear, so a person is spoken to about it only when something the run set
// going may still be at work there. The set is walked from store's closed
// set, so an outcome added there is decided here or fails.
func TestACopySweepIsPhrasedExactlyWhenItDidNotClear(t *testing.T) {
	t.Parallel()
	quiet := map[store.SweepOutcome]bool{store.SweepCleared: true, store.SweepNothingStanding: true}
	for _, outcome := range store.SweepOutcomes() {
		phrase, ok := machine.CopySweepPhrase(store.RunCopySweep{Outcome: outcome})
		if ok == quiet[outcome] {
			t.Errorf("a %s sweep phrased = %v", outcome, ok)
		}
		if ok && phrase.Decision == "" {
			t.Errorf("a %s sweep's row names no decision", outcome)
		}
	}
}
