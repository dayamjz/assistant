package machine

import (
	"strings"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Phrase is one row of the vocabulary a person is spoken to in: what happened
// to their work, what it costs them, and what they decide next, as three
// sentences a renderer prints together. P13 asks for all three, and holding
// each in a field of its own is what makes that half of the rule structural
// rather than a substring somebody remembered to check: a row with an empty
// field fails the vocabulary test without anyone reading the prose.
//
// A row carries no task identifier, path, stage name, state label or watcher's
// word. The vocabulary test derives that forbidden set from the packages that
// own each kind of word and holds every row Phrases lists to it.
type Phrase struct {
	// Outcome is what happened to the person's work.
	Outcome string
	// Consequence is what that costs them, or that it costs nothing.
	Consequence string
	// Decision is what they decide next, or that there is nothing to decide.
	Decision string
}

// String is the phrase as a person reads it: the three sentences in order,
// separated by a space.
func (p Phrase) String() string {
	return strings.Join([]string{p.Outcome, p.Consequence, p.Decision}, " ")
}

// The pull guard's rows, composed as the string constants vocabulary.go
// exports and the guard's callers compare against.
var (
	phraseServiceDown = Phrase{
		Outcome:     "Work is under way and nothing is watching it,",
		Consequence: "so a worker that stops or needs a decision will go unnoticed until the service is up.",
		Decision:    "Run assistant service start.",
	}
	phraseServiceNotWatching = Phrase{
		Outcome:     "Work is under way and the service is up, but nothing in it is watching the fleet,",
		Consequence: "so a worker that stops or needs a decision will go unnoticed.",
		Decision:    "Run assistant service restart.",
	}
	phraseUndecided = Phrase{
		Outcome:     "Whether work is under way could not be decided, because this home could not be read,",
		Consequence: "so a worker that stops or needs a decision may go unnoticed.",
		Decision:    "Run assistant doctor.",
	}
)

// phraseByState is the one table from a task's resolved state to what a person
// reads about it. Every state internal/taskstate can resolve has a row, which
// the vocabulary test derives from taskstate.States and checks, so a state
// added there fails that test until it is phrased here. Two states are
// translated from more than the word: Paused is never shown as a word, which
// PRD section 6 and its translation row require, and renders as what the
// worker said it is waiting on through WaitingPhrase; and Unknown after a
// note the program appended, a promotion or a relaunch, is not a failure
// but a worker that has not reported yet, which NoWordYet says. TaskPhrase
// is the one reader, and it is where those two translations are chosen.
//
// No row carries an outcome word. A task's state is PRD section 6's own
// vocabulary and not a second rendering of a run's outcome, so a task with no
// run shows its phrase and nothing from the outcome set, which the vocabulary
// test holds every row here to.
var phraseByState = map[taskstate.State]Phrase{
	taskstate.Queued: {
		Outcome:     "This work is waiting its turn behind other work on the same branch.",
		Consequence: "Nothing happens to it until that work is out of the way.",
		Decision:    "Nothing to decide; it starts on its own.",
	},
	taskstate.Dispatched: {
		Outcome:     "A worker is being launched on this work.",
		Consequence: "It has said nothing yet.",
		Decision:    "Nothing to decide yet.",
	},
	taskstate.Running: {
		Outcome:     "A worker is on this work.",
		Consequence: "It has not asked you for anything.",
		Decision:    "Nothing to decide yet.",
	},
	taskstate.Held: {
		Outcome:     "This work is waiting on you.",
		Consequence: "Nothing moves until you answer.",
		Decision:    "Answer it to let the work continue.",
	},
	taskstate.Paused: {
		// The row is a template: TaskPhrase fills the worker's own words in
		// through WaitingPhrase, and this is what the table lists for the
		// vocabulary test.
		Outcome:     waitingOutcome(waitingPlaceholder),
		Consequence: waitingConsequence,
		Decision:    waitingDecision,
	},
	taskstate.Validating: {
		Outcome:     "The work is committed and is being validated.",
		Consequence: "It reaches a shared branch only if every check clears.",
		Decision:    "Nothing to decide until a check asks you.",
	},
	taskstate.Ready: {
		Outcome:     "The work was validated and can be merged.",
		Consequence: "It is not on the shared branch until you merge it.",
		Decision:    "Decide whether to merge it.",
	},
	taskstate.Landed: {
		Outcome:     "The work is merged.",
		Consequence: "Its local copy stays until removing it is proven safe.",
		Decision:    "Nothing to decide.",
	},
	taskstate.Reported: {
		Outcome:     "The worker finished and its report is in.",
		Consequence: "Nothing more happens on its own.",
		Decision:    "Read the report and decide what to do with it.",
	},
	taskstate.Complete: {
		Outcome:     "This work is finished and its local copy is gone.",
		Consequence: "Nothing of it is left here.",
		Decision:    "Nothing to decide.",
	},
	taskstate.Failed: {
		Outcome:     "This work stopped and cannot go on by itself.",
		Consequence: "Nothing more happens until it is picked up.",
		Decision:    "Read what stopped it, then launch it again or let it go.",
	},
	taskstate.Unknown: {
		Outcome:     "What is happening to this work could not be decided.",
		Consequence: "It may be moving or stuck without anyone noticing.",
		Decision:    "Look at it directly, or run assistant doctor.",
	},
}

// NoWordYet is the row for a task whose newest history entry is a note the
// program appended, a promotion or a relaunch, and which therefore resolves
// Unknown: the worker was launched, or launched again, and has not reported
// since. It is not a failure and is not phrased as one.
var NoWordYet = Phrase{
	Outcome:     "There is no word from the worker yet.",
	Consequence: "It was launched, or launched again, and has said nothing since.",
	Decision:    "Nothing to decide until it does.",
}

// The Paused translation. The worker's own words are filled into the outcome,
// because PRD section 6 has the state shown as what the worker said it is
// waiting on and never as the word.
const (
	waitingPlaceholder = "something it named"
	waitingConsequence = "Nothing moves until that clears."
	waitingDecision    = "Nothing to decide unless you can clear it."
)

func waitingOutcome(on string) string {
	return "The worker is waiting on something outside this work: " + on + "."
}

// WaitingPhrase is the Paused row filled in with what the worker said it is
// waiting on. The text is the worker's and is relayed as the one thing a
// person needs to know about the wait; a renderer escapes it as it does every
// line an agent wrote. A worker that said nothing is phrased with the
// placeholder the table lists, so the row never reads as waiting on nothing.
func WaitingPhrase(on string) Phrase {
	if strings.TrimSpace(on) == "" {
		on = waitingPlaceholder
	}
	return Phrase{Outcome: waitingOutcome(on), Consequence: waitingConsequence, Decision: waitingDecision}
}

// TaskPhrase is the one translation from a task's resolved state to what a
// person reads. It is phraseByState's row for the state, with the two
// translations that row table describes: Paused is WaitingPhrase over
// Task.Waiting, and Unknown resolved at the event tier from a note the
// program appended is NoWordYet. A state outside the table, which the
// vocabulary test keeps from being one of internal/taskstate's, reads as the
// Unknown row rather than as nothing.
func TaskPhrase(t Task) Phrase {
	state := t.State.State
	switch {
	case state == taskstate.Paused:
		return WaitingPhrase(t.Waiting)
	case state == taskstate.Unknown && t.State.Source == taskstate.SourceNone && noteTailed(t.State.Basis.EventKind):
		return NoWordYet
	}
	if p, ok := phraseByState[state]; ok {
		return p
	}
	return phraseByState[taskstate.Unknown]
}

// noteTailed reports whether kind is one of internal/taskstate's note kinds the
// program appends about its own decisions: a kind that claims no state and
// that a worker may not write. A worker's own note is a different thing, a
// worker that is reporting, and is not read as no word yet.
func noteTailed(kind string) bool {
	row, ok := taskstate.Lookup(kind)
	return ok && row.Role == taskstate.RoleNote && row.Writer == taskstate.WriterProgram
}

// phraseByOutcome is the one table from a run's outcome to what a person reads
// about it. Every member of the closed set has a row, which the vocabulary
// test derives from Outcomes and checks. OutcomeExecuting covers three runs an
// outcome alone cannot tell apart, on Run.Advancing's and Execution's terms,
// so it has three rows beside this one: RunPhrase chooses between them from
// the same Standing NextActionOf reads, and this table's row is the weaker
// claim that holds for all three, which is what a listing with no advancing
// fact says.
var phraseByOutcome = map[Outcome]Phrase{
	OutcomeChecksPassed: {
		Outcome:     "This change cleared the gate.",
		Consequence: "It is not on the shared branch until it is merged.",
		Decision:    "Decide whether to merge it.",
	},
	OutcomePassed: {
		Outcome:     "This change is merged.",
		Consequence: "Nothing more happens to it here.",
		Decision:    "Nothing to decide.",
	},
	OutcomeFailed: {
		Outcome:     "The gate could not reach a verdict on this change.",
		Consequence: "Nothing moves until it is picked up.",
		Decision:    "Read what stopped it; then submit a fix, or carry the run on with more room.",
	},
	OutcomeCancelled: {
		Outcome:     "This run was ended before the gate reached a verdict.",
		Consequence: "Nothing was undone.",
		Decision:    "Start a fresh run when the change is fit to go again.",
	},
	OutcomeDecision: {
		Outcome:     "This change is waiting on a decision from you.",
		Consequence: "Nothing moves until you answer.",
		Decision:    "Answer it with assistant --answer from the change's branch.",
	},
	OutcomeExecuting: {
		Outcome:     "This change is in validation.",
		Consequence: "It has not reached a verdict.",
		Decision:    "Ask assistant status from its branch for where it stands.",
	},
}

// The three rows OutcomeExecuting splits into when the standing is known:
// a run a service is advancing, a run standing at a position nothing is
// advancing, and a run with no recorded position at all. They are the
// person's reading of the three next actions NextActionOf chooses between,
// and RunPhrase chooses between them on the same facts.
var (
	phraseRunAdvancing = Phrase{
		Outcome:     "This change is being validated now.",
		Consequence: "It stops at the next decision or at the end of the gate.",
		Decision:    "Nothing to decide yet; ask again in a while.",
	}
	phraseRunStalled = Phrase{
		Outcome:     "Validation of this change stopped partway and nothing is carrying it on.",
		Consequence: "It stays where it is until something resumes it.",
		Decision:    "Run assistant from the change's branch to carry it on.",
	}
	phraseRunUnrecorded = Phrase{
		Outcome:     "Validation of this change was begun and left no record of where it got to.",
		Consequence: "There is nothing to carry it on from.",
		Decision:    "End it with assistant --cancel and start a fresh run.",
	}
)

// RunPhrase is the one translation from a run's standing to what a person
// reads: phraseByOutcome's row for OutcomeOf the standing, sharpened for
// OutcomeExecuting into the three rows above on the facts NextActionOf reads,
// so the phrase and the next action a driving agent is given cannot describe
// different runs. Run.Phrase makes the same choice from an answer already
// decided, through the same chooser.
func RunPhrase(s Standing) Phrase {
	return outcomePhrase(OutcomeOf(s), s.Advancing, s.Execution.Recorded())
}

// Phrase is what a person reads about this run, chosen from the outcome,
// the advancing fact and whether a position was recorded, which is what
// Decide wrote and Progress carries, through the chooser RunPhrase uses.
func (r Run) Phrase() Phrase {
	return outcomePhrase(r.Outcome, r.Advancing, r.Progress != nil)
}

// outcomePhrase is the one chooser behind RunPhrase and Run.Phrase. An
// outcome outside the set, which the vocabulary test keeps from being one of
// Outcomes, reads as the failed row, on OutcomeOf's own terms for a standing
// it cannot interpret.
func outcomePhrase(outcome Outcome, advancing, recorded bool) Phrase {
	if outcome == OutcomeExecuting {
		switch {
		case advancing:
			return phraseRunAdvancing
		case !recorded:
			return phraseRunUnrecorded
		default:
			return phraseRunStalled
		}
	}
	if p, ok := phraseByOutcome[outcome]; ok {
		return p
	}
	return phraseByOutcome[OutcomeFailed]
}

// RecordPhrase is what a person reads about a run of which only the record is
// in hand: a listing, or a run's state travelling as an event. The record
// carries no merge record, no checkpoint and no advancing fact, so this is
// phraseByOutcome's row for the record's status read through OutcomeOf with
// the execution unrecorded, and OutcomeExecuting is left at the weaker row
// rather than sharpened on facts the caller does not have. Two readings are
// therefore the record's and not the run's, and RunPhrase is where the run's
// are: a passed record reads as having cleared the gate, which is true of a
// merged one too, and a terminated record reads as ended before a verdict,
// which is true of a run a bound parked too; the merged and the parked
// readings need the merge record and the checkpoint.
func RecordPhrase(r store.Run) Phrase {
	outcome := OutcomeOf(Standing{Record: r.Status, Execution: ExecutionUnrecorded()})
	if p, ok := phraseByOutcome[outcome]; ok {
		return p
	}
	return phraseByOutcome[OutcomeFailed]
}

// Hold rows, with the decision's subject filled in: the subject is the
// program's own sentence about what is being decided, never a worker's text.
const holdPlaceholder = "a decision"

// HoldPhrase is what a person reads about a hold as it stands: one row while
// it is open, which is a decision waiting on them, and one once it carries a
// resolution. The subject is filled into the outcome; a hold with no subject,
// which internal/store refuses to register, is phrased with a placeholder so
// the row never reads as waiting on nothing.
func HoldPhrase(h store.Hold) Phrase {
	subject := strings.TrimSpace(h.Subject)
	if subject == "" {
		subject = holdPlaceholder
	}
	if h.Open() {
		return Phrase{
			Outcome:     "A decision is waiting on you: " + subject + ".",
			Consequence: "The work it holds does not move until you answer.",
			Decision:    "Answer it to let that work continue.",
		}
	}
	return Phrase{
		Outcome:     "A decision was answered: " + subject + ".",
		Consequence: "The work it was holding carries on.",
		Decision:    "Nothing to decide.",
	}
}

// The stream's own rows: what a person reads when the fleet view is told
// something about the channel rather than about the work.
var (
	// PhraseUpdatesMissed is the row for a gap in the stream: some updates
	// were discarded, and what was shown before them may be stale.
	PhraseUpdatesMissed = Phrase{
		Outcome:     "Some updates were missed.",
		Consequence: "What was shown before them may be out of date.",
		Decision:    "Run assistant tasks for the current picture.",
	}
	// PhraseServiceStopping is the row for the service going away under the
	// view.
	PhraseServiceStopping = Phrase{
		Outcome:     "The service is stopping.",
		Consequence: "This view ends with it, and work under way goes unwatched until it is back.",
		Decision:    "Run assistant service start to resume.",
	}
	// PhraseWorkerSpoke is the row for a worker appending to its history:
	// the entry is history and its words are not relayed, and what the task
	// now resolves to is the listing's to say once it has been read.
	PhraseWorkerSpoke = Phrase{
		Outcome:     "A worker added to its history.",
		Consequence: "What that means for its work shows once it has been read.",
		Decision:    "Nothing to decide until it does.",
	}
	// PhraseActivity is the row for a worker's or a stage's output arriving
	// on the stream. P13 makes that output something to read and never
	// something to relay, so the row says that something was produced and
	// relays none of it.
	PhraseActivity = Phrase{
		Outcome:     "A worker produced some output.",
		Consequence: "It is kept to be read, not shown here.",
		Decision:    "Nothing to decide.",
	}
	// PhraseUnfamiliarUpdate is the row for an update of a kind this view
	// cannot describe: one a newer peer added, which the stream retained
	// rather than dropped, or one this build names and nothing here yet
	// produces, so no shape for it is owned.
	PhraseUnfamiliarUpdate = Phrase{
		Outcome:     "Something changed that this view cannot describe.",
		Consequence: "The picture here may be behind.",
		Decision:    "Run assistant tasks for the current picture.",
	}
)

// Cleanup rows, for a run's cleanup record: the copy was kept on a refusal,
// or removed on a proof. A record with a proof and no removal yet is the
// proof awaiting its removal, which no row phrases because nothing a person
// decides turns on it.
var (
	phraseCopyKept = Phrase{
		Outcome:     "The local copy of this run's work was kept.",
		Consequence: "Nothing yet proves the work is safely somewhere else, so removing it could lose it.",
		Decision:    "Nothing to do; it is removed once the work lands.",
	}
	phraseCopyRemoved = Phrase{
		Outcome:     "The local copy of this run's work was removed.",
		Consequence: "The work is safely somewhere else, so nothing was lost.",
		Decision:    "Nothing to decide.",
	}
)

// CleanupPhrase is what a person reads about a run's cleanup record, and
// false for a record no row phrases: the kept-copy line for a refusal, which
// is where a person learns a local copy stays because its work is not yet
// safely elsewhere, and the removed line once the proof's removal is
// recorded.
func CleanupPhrase(c store.RunCleanup) (Phrase, bool) {
	switch {
	case c.Kept():
		return phraseCopyKept, true
	case c.RemovedAt.IsKnown():
		return phraseCopyRemoved, true
	default:
		return Phrase{}, false
	}
}

// Status rows: the parts of assistant status that are not a run.
var (
	// PhraseServiceUp is the row for a service that answered.
	PhraseServiceUp = Phrase{
		Outcome:     "The service is up.",
		Consequence: "Work under way is watched and a change sent to the gate is validated.",
		Decision:    "Nothing to do.",
	}
	// PhraseServiceAbsent is the row for a home no service answered for.
	PhraseServiceAbsent = Phrase{
		Outcome:     "No service is up for this home.",
		Consequence: "Nothing is validated or watched until one is.",
		Decision:    "Run assistant service start.",
	}
	// PhraseGated is the row for a working copy bound to a gate.
	PhraseGated = Phrase{
		Outcome:     "This checkout sends its changes through a gate.",
		Consequence: "A change sent to it is validated before it goes anywhere.",
		Decision:    "Nothing to do.",
	}
	// PhraseUngated is the row for a working copy bound to no gate, or with
	// no repository record at all; either way the next decision is the same.
	PhraseUngated = Phrase{
		Outcome:     "This checkout is not bound to a gate.",
		Consequence: "Nothing validates a change sent from here.",
		Decision:    "Run assistant init here.",
	}
	// PhraseNoRun is the row for a branch with no run in flight.
	PhraseNoRun = Phrase{
		Outcome:     "No run is in flight for this branch.",
		Consequence: "Nothing is being validated.",
		Decision:    "Send a change to the gate to start one.",
	}
	// PhraseNoRuns is the row for a repository with no run recorded.
	PhraseNoRuns = Phrase{
		Outcome:     "No run is recorded for this repository.",
		Consequence: "Nothing has been validated here yet.",
		Decision:    "Send a change to the gate to start one.",
	}
	// PhraseNoFleetWork is the row for a home with no task recorded.
	PhraseNoFleetWork = Phrase{
		Outcome:     "No fleet work is recorded.",
		Consequence: "There is nothing to watch.",
		Decision:    "Nothing to decide.",
	}
)

// TablePhrases returns every row this table holds, as a person reads each,
// which is what the vocabulary test walks: the pull guard's three rows, a row
// per task state and NoWordYet, a row per outcome and the three OutcomeExecuting
// splits into, the two hold rows and the two cleanup rows, the stream's four,
// and the status rows. A templated row is listed with its placeholder filled
// in. The result is a copy. Phrases is the same list as strings.
func TablePhrases() []Phrase {
	out := []Phrase{phraseServiceDown, phraseServiceNotWatching, phraseUndecided}
	for _, state := range taskstate.States() {
		if p, ok := phraseByState[state]; ok {
			out = append(out, p)
		}
	}
	out = append(out, NoWordYet)
	for _, outcome := range Outcomes() {
		if p, ok := phraseByOutcome[outcome]; ok {
			out = append(out, p)
		}
	}
	out = append(out, phraseRunAdvancing, phraseRunStalled, phraseRunUnrecorded)
	out = append(out, HoldPhrase(store.Hold{Subject: holdPlaceholder}), HoldPhrase(store.Hold{Subject: holdPlaceholder, Resolution: store.Known("answered")}))
	out = append(out, phraseCopyKept, phraseCopyRemoved)
	out = append(out, PhraseUpdatesMissed, PhraseServiceStopping, PhraseWorkerSpoke, PhraseActivity, PhraseUnfamiliarUpdate)
	out = append(out, PhraseServiceUp, PhraseServiceAbsent, PhraseGated, PhraseUngated, PhraseNoRun, PhraseNoRuns, PhraseNoFleetWork)
	return out
}
