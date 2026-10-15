package machine

import (
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/pipeline"
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

// The stop guard's rows, composed as the string constants host.go exports
// and the host hook relays. Each is the first half of a composite: the host's
// message is the turn row followed by the pull guard's row for the verdict,
// through HostStopOf, so the turn row carries the outcome and the consequence
// and its Decision names no step of its own. It says the concrete next step
// is the sentence after it, which is the pull guard's row, so one message
// never carries two competing decisions and the two tables cannot say
// different things about one situation.
var (
	phraseTurnBlocked = Phrase{
		Outcome:     "This turn cannot end yet, because work is under way and nothing is watching it.",
		Consequence: "Ending it now would leave that work unwatched.",
		Decision:    "What to do next is the sentence after this one, and it comes before ending the turn.",
	}
	phraseTurnReEntered = Phrase{
		Outcome:     "This turn may end although work is under way and nothing is watching it, because the host re-entered it after a block.",
		Consequence: "The next turn is judged afresh.",
		Decision:    "What to do next is the sentence after this one.",
	}
)

// The stop guard installer's rows, one per result and one per refusal,
// chosen by HostInstallPhrase. None names the file or the line: both are
// machine text a person copies exactly, so the renderer prints them beside
// the row as they are. None says when the host reads its settings either,
// because that is the host's behaviour and nothing here establishes it; the
// rows say what this command did to the file and nothing past it.
var (
	phraseHostInstallPrinted = Phrase{
		Outcome:     "The line below is what the coordinator's host needs in the settings file named below to check, as each turn ends, whether work would be left unwatched.",
		Consequence: "Nothing was written, so the host's settings are as they were.",
		Decision:    "Add the line to that file yourself, beside any hooks it already has, or run this again with --write to have it added for you.",
	}
	phraseHostInstallWritten = Phrase{
		Outcome:     "The settings file named below now holds the line the coordinator's host needs to check, as each turn ends, whether work would be left unwatched.",
		Consequence: "Every other setting in the file was kept, in its order, though the file is laid out afresh.",
		Decision:    "Nothing to decide.",
	}
	phraseHostInstallPresent = Phrase{
		Outcome:     "The settings file named below already holds the line the coordinator's host needs.",
		Consequence: "Nothing was written, so the file is as it was.",
		Decision:    "Nothing to decide.",
	}
	phraseHostInstallConflict = Phrase{
		Outcome:     "The line was not added, because the settings file named below already runs this program's turn-end check with a different command, shown after the line.",
		Consequence: "Nothing was written, so the file is as it was; adding a second check would judge another binary or another home.",
		Decision:    "Decide which of the two the host should run, edit the file yourself, and run this again with --write if you still want the line below.",
	}
	phraseHostInstallUnreadable = Phrase{
		Outcome:     "The line was not added, because the settings file named below could not be read as settings the line can be merged into.",
		Consequence: "Nothing was written, so the file is as it was.",
		Decision:    "Repair the file, or add the line below yourself, and run this again with --write if you still want it added for you.",
	}
	phraseHostInstallChanged = Phrase{
		Outcome:     "The line was not added, because the settings file named below changed while it was being merged.",
		Consequence: "Nothing was written, so whatever changed it was kept.",
		Decision:    "Run this again with --write.",
	}
)

// HostInstallPhrase is what a person reads about a request to install the
// stop guard's hook: the row for the result, and for a refusal the row for
// why. A refused answer whose refusal is not one HostInstallRefusals lists
// reads as unreadable, which says nothing was written; it never reads as
// written.
func HostInstallPhrase(h HostInstall) Phrase {
	switch h.Result {
	case HostInstallPrinted:
		return phraseHostInstallPrinted
	case HostInstallWritten:
		return phraseHostInstallWritten
	case HostInstallPresent:
		return phraseHostInstallPresent
	}
	switch h.Refusal {
	case HostInstallConflict:
		return phraseHostInstallConflict
	case HostInstallChanged:
		return phraseHostInstallChanged
	}
	return phraseHostInstallUnreadable
}

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

// phraseTaskLandedLocally is the row for a task its local landing record
// landed: PRD section 6's local posture, which starts no run, so the row words
// it as a change made on this machine without validation and never with the
// words a run that cleared the gate uses. TaskPhrase chooses it from
// Basis.LandedLocally when the terminal tier answered Landed.
var phraseTaskLandedLocally = Phrase{
	Outcome:     "This change was made on your machine without validation: your local default branch was moved to it.",
	Consequence: "Nothing was shared, and its local copy stays until removing it is proven safe.",
	Decision:    "Nothing to decide.",
}

// phraseLandedLocallyNow is the row for an approval whose fast-forward was
// made, AnswerLanded, on phraseTaskLandedLocally's terms.
var phraseLandedLocallyNow = Phrase{
	Outcome:     "Your approval was taken, and your local default branch was moved to the change without validation.",
	Consequence: "Nothing was shared, and nothing checked the change before it moved.",
	Decision:    "Nothing to decide.",
}

// phraseTaskHeld is the row for a task internal/taskstate resolved Held from
// its hold tier: an open hold names the task itself, so it is answered given
// the task, with --task and --answer, rather than from a branch as a run's
// decision is. The identifier is the structured rendering's, which P13 keeps
// off this line, so the row says where to read it. The tier outranks every
// run, so this is the row whatever the task's run or terminal says.
var phraseTaskHeld = Phrase{
	Outcome:     "This work is waiting on an answer from you.",
	Consequence: "Nothing more is done with it until you answer.",
	Decision:    taskAnswerDecision,
}

// taskAnswerDecision is how a hold naming a task is answered, which the task
// row above and HoldPhrase's open row for such a hold share, so the two
// cannot name different commands.
const taskAnswerDecision = "Answer it with assistant --task and the work's identifier, which assistant tasks --json shows, " +
	"followed by --answer and your answer."

// The rows for an answer given a task that reaches no run's decision, which
// internal/service's refusal carries after the identifiers it names. An
// answer given a task closes the task's own decision while one is open, and
// otherwise the decision its current validation is waiting on; these say why
// neither was answered, and the decision says what to read before answering
// again, since the identifiers are the refusal's and not the row's.
var (
	// PhraseTaskOwnHoldFirst is the row for an answer meant for the task's
	// run, refused because the task has a decision of its own open, which
	// is answered first.
	PhraseTaskOwnHoldFirst = Phrase{
		Outcome:     "Nothing was answered, because this work is waiting on a decision of its own, and that one is answered before anything its validation waits on.",
		Consequence: "Both decisions stay open, and the work does not move until you answer.",
		Decision:    "Answer the work's own decision first, the same way and without the options only a fix round takes, then give this answer again.",
	}
	// PhraseTaskNothingWaiting is the row for an answer given a task when
	// neither the task nor its current validation is waiting on one.
	PhraseTaskNothingWaiting = Phrase{
		Outcome:     "Nothing was answered, because neither this work nor a validation of it is waiting on a decision.",
		Consequence: "Nothing changed.",
		Decision:    "Read where the work stands with assistant tasks, and answer when it waits on you.",
	}
)

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
// program appended is NoWordYet. Held resolved from the hold tier is
// phraseTaskHeld, which says how a hold naming the task is answered; Held
// from a run or from the worker's own history keeps the table's row. A state outside the table, which the
// vocabulary test keeps from being one of internal/taskstate's, reads as the
// Unknown row rather than as nothing.
func TaskPhrase(t Task) Phrase {
	state := t.State.State
	switch {
	case state == taskstate.Paused:
		return WaitingPhrase(t.Waiting)
	case state == taskstate.Held && t.State.Source == taskstate.SourceHold:
		return phraseTaskHeld
	case state == taskstate.Landed && t.State.Source == taskstate.SourceRecord && t.State.Basis.LandedRun == "" &&
		t.State.Basis.LandedLocally != "":
		return phraseTaskLandedLocally
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

// phraseByPark is what a person reads about a run whose record names the
// bound that parked it, one row per bound in store.ParkBounds, which the
// vocabulary test derives and checks. They sharpen OutcomeFailed's row the way
// the executing rows sharpen that outcome's: the failed row has to cover a run
// that could not proceed as well as all three parks, and a person deciding
// what to do about a park needs to know which bound it was, because what
// changes the outcome differs by bound. ParkAction is the driving agent's
// reading of the same three, with the stage and the key a row may not name.
var phraseByPark = map[store.ParkBound]Phrase{
	store.ParkRoundLimit: {
		Outcome:     "Automatic fixing of this change reached its round limit with findings still to fix.",
		Consequence: "Nothing more is done with it, and what the last check found stays with the run.",
		Decision:    "Fix those findings and send the change again, or allow more fix rounds and send it again.",
	},
	store.ParkConvergence: {
		Outcome:     "Automatic fixing of this change was stopped because its last round changed nothing.",
		Consequence: "Another round would change nothing either, and what the last check found stays with the run.",
		Decision:    "Fix those findings yourself and send the change again.",
	},
	store.ParkStepBudget: {
		Outcome:     "This run used its whole step budget before the gate reached a verdict.",
		Consequence: "Nothing more is done with it, and nothing was undone.",
		Decision:    "Allow a larger step budget and send the change again.",
	},
}

// RunPhrase is the one translation from a run's standing to what a person
// reads: phraseByOutcome's row for OutcomeOf the standing, sharpened for
// OutcomeExecuting into the three rows above on the facts NextActionOf reads,
// and for a failed run whose record names its park into that bound's row, so
// the phrase and the next action a driving agent is given cannot describe
// different runs. Run.Phrase makes the same choice from an answer already
// decided, through the same chooser.
func RunPhrase(s Standing) Phrase {
	return outcomePhrase(OutcomeOf(s), s.Park, s.Advancing, s.Execution.Recorded())
}

// Phrase is what a person reads about this run, chosen from the outcome,
// the park its record names, the advancing fact and whether a position was
// recorded, which is what Decide wrote and Progress carries, through the
// chooser RunPhrase uses.
func (r Run) Phrase() Phrase {
	return outcomePhrase(r.Outcome, r.Record.Park, r.Advancing, r.Progress != nil)
}

// outcomePhrase is the one chooser behind RunPhrase and Run.Phrase. An outcome outside the set, which the vocabulary test keeps
// from being one of Outcomes, reads as the failed row, on OutcomeOf's own
// terms for a standing it cannot interpret.
func outcomePhrase(outcome Outcome, park store.Optional[store.RunPark], advancing, recorded bool) Phrase {
	if row, ok := parkPhrase(outcome, park); ok {
		return row
	}
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
// carries its status and the park that ended it, if a bound did, and no merge
// record, no checkpoint and no advancing fact, so this is the row for the
// record read through OutcomeOf with the execution unrecorded, and
// OutcomeExecuting is left at the weaker row rather than sharpened on facts
// the caller does not have. A terminated record naming a park reads as that
// bound's row, as RunPhrase reads it. Two readings are still the record's and
// not the run's, and RunPhrase is where the run's are: a passed record reads
// as having cleared the gate, which is true of a merged one too, and a
// terminated record parked before the park record existed reads as ended
// before a verdict; those readings need the merge record and the checkpoint.
func RecordPhrase(r store.Run) Phrase {
	outcome := OutcomeOf(Standing{Record: r.Status, Park: r.Park, Execution: ExecutionUnrecorded()})
	if p, ok := parkPhrase(outcome, r.Park); ok {
		return p
	}
	if p, ok := phraseByOutcome[outcome]; ok {
		return p
	}
	return phraseByOutcome[OutcomeFailed]
}

// parkPhrase is the bound's row for a failed run whose record names its park,
// and false for any other run. It is the one place that choice is made, for
// outcomePhrase and RecordPhrase both.
func parkPhrase(outcome Outcome, park store.Optional[store.RunPark]) (Phrase, bool) {
	p, ok := park.Get()
	if !ok || outcome != OutcomeFailed {
		return Phrase{}, false
	}
	row, ok := phraseByPark[p.Bound]
	return row, ok
}

// Hold rows, with the decision's subject filled in: the subject is the
// program's own sentence about what is being decided, never a worker's text.
const holdPlaceholder = "a decision"

// HoldPhrase is what a person reads about a hold as it stands: one row while
// it is open, which is a decision waiting on them, and one once it carries a
// resolution. The subject is filled into the outcome; a hold with no subject,
// which internal/store refuses to register, is phrased with a placeholder so
// the row never reads as waiting on nothing. An open hold naming a task and
// no run, which internal/service answers given the task, says how: with
// --task and --answer, as phraseTaskHeld does.
func HoldPhrase(h store.Hold) Phrase {
	subject := strings.TrimSpace(h.Subject)
	if subject == "" {
		subject = holdPlaceholder
	}
	if h.Open() {
		decision := "Answer it to let that work continue."
		if h.TaskID.IsKnown() && !h.RunID.IsKnown() {
			decision = taskAnswerDecision
		}
		return Phrase{
			Outcome:     "A decision is waiting on you: " + subject + ".",
			Consequence: "The work it holds does not move until you answer.",
			Decision:    decision,
		}
	}
	return Phrase{
		Outcome:     "A decision was answered: " + subject + ".",
		Consequence: "The work it was holding carries on.",
		Decision:    "Nothing to decide.",
	}
}

// The rows for an answer to a task's hold that did more than close it: a
// retry that began the work again where it left off, one that began it
// again in its copy as it stands, one recorded and not acted on yet, one
// that raised a further decision, one the check that keeps the work apart
// refused, one whose record cannot be used to begin the work, and a stop. A hold whose answer acts on nothing further reads as
// HoldPhrase's answered row. Each row claims only what its effect's doc in
// wire.go says the mechanism does: the deferred row promises nothing about
// the next start, because what that start does depends on why the work was
// not begun now, and the row for a dispatch says nothing
// about carrying on, because a dispatch gives the worker no note.
var (
	phraseRetried = Phrase{
		Outcome:     "Your answer was taken, and the work was begun again where it left off.",
		Consequence: "It carries on from what was already done there.",
		Decision:    "Nothing to decide unless it ends again without finishing, which asks you again.",
	}
	phraseRedispatched = Phrase{
		Outcome:     "Your answer was taken, and the work was begun again in its local copy as that copy stands.",
		Consequence: "The new worker is told nothing about the earlier attempt or what it may have left there.",
		Decision:    "Nothing to decide now; run assistant tasks to see how it goes.",
	}
	phraseRetryDeferred = Phrase{
		Outcome:     "Your answer was recorded, and the work could not be begun again just now.",
		Consequence: "The service looks at this work again the next time it comes up, and that may or may not begin it.",
		Decision:    "Repeat the command with --json to read why, and run assistant service restart once that is dealt with.",
	}
	phraseRetryHeld = Phrase{
		Outcome:     "Your answer was recorded, and the work needs one more decision from you before it is begun again.",
		Consequence: "Nothing here can check that the earlier attempt left no process behind where the work is kept, so it waits on you.",
		Decision:    "Answer the new decision to begin the work again anyway, or to leave it as it is.",
	}
	phraseRetryAtBound = Phrase{
		Outcome:     "Your answer was recorded, and the work needs one more decision from you before it is begun again.",
		Consequence: "It has already been begun again as many times as the program does on its own.",
		Decision:    "Answer the new decision to begin the work again anyway, or to leave it as it is.",
	}
	phraseRetryRefused = Phrase{
		Outcome:     "Your answer was recorded, and the work was not begun again: its local copy did not pass the check that keeps the work apart.",
		Consequence: "Nothing begins it again on its own.",
		Decision:    "Repeat the command with --json to read what the check found, then decide whether to start this work afresh or let it go.",
	}
	phraseRetryUndispatchable = Phrase{
		Outcome:     "Your answer was recorded, and the work was not begun again: what this task was recorded with cannot be used to begin it.",
		Consequence: "Nothing will begin it until that record is changed.",
		Decision:    "Repeat the command with --json to read what in the record was refused, then decide whether to put it right or let this work go.",
	}
	phraseNotRetried = Phrase{
		Outcome:     "Your answer was recorded, and the work will not be begun again.",
		Consequence: "It stays as it was left, and nothing more is done with it.",
		Decision:    "Nothing to decide.",
	}
)

// TaskAnsweredPhrase is what a person reads about an answer to a task's
// hold: the row for what the answer did, and HoldPhrase's answered row for
// an answer that only closed the hold.
func TaskAnsweredPhrase(a TaskAnswered) Phrase {
	switch a.Effect {
	case AnswerRelaunched:
		return phraseRetried
	case AnswerRedispatched:
		return phraseRedispatched
	case AnswerRetryDeferred:
		return phraseRetryDeferred
	case AnswerRetryHeld:
		return phraseRetryHeld
	case AnswerRetryAtBound:
		return phraseRetryAtBound
	case AnswerRetryRefused:
		return phraseRetryRefused
	case AnswerRetryUndispatchable:
		return phraseRetryUndispatchable
	case AnswerStopped:
		return phraseNotRetried
	case AnswerLanded:
		return phraseLandedLocallyNow
	}
	return HoldPhrase(a.Hold)
}

// The steering rows, PRD section 6's "what you hear": a message as its
// record stands, a stopped worker waiting on a decision, and a stop that
// could not be shown to have ended the worker. None relays the message's
// text or the worker's output, which are evidence and never relayed (P13).
var (
	phraseMessageUnread = Phrase{
		Outcome:     "Your message reached the worker, which has not yet said it read it.",
		Consequence: "Nothing sends it again; if it stays unread for a while, you are told once.",
		Decision:    "Nothing to decide unless you are told it went unread.",
	}
	phraseMessageRead = Phrase{
		Outcome:     "The worker said it read your message.",
		Consequence: "What it does with it shows in its work.",
		Decision:    "Nothing to decide.",
	}
	phraseMessageRefused = Phrase{
		Outcome:     "Your message did not reach the worker.",
		Consequence: "Nothing sends it again.",
		Decision:    "Send it again while a worker is on the work, or end the worker and begin the work again with a note.",
	}
	phraseMessageUnknown = Phrase{
		Outcome:     "Whether your message reached the worker is not known.",
		Consequence: "Nothing sends it again.",
		Decision:    "Look at the work directly before sending it again.",
	}
	phraseWorkerStopped = Phrase{
		Outcome:     "The worker was ended, and what it had done was left where it was.",
		Consequence: "Nothing more is done with the work until you answer, and nothing begins it again on its own.",
		Decision:    taskAnswerDecision,
	}
	phraseFinishedWorkerStopped = Phrase{
		Outcome:     "The worker was ended after its work was already finished.",
		Consequence: "No decision was opened and nothing begins it again; its local copy is removed only once that is proven safe.",
		Decision:    "Nothing to decide.",
	}
	// PhraseStopUnverified is the row for a stop whose end the program could
	// not show: the worker may still be running, and no decision was opened.
	PhraseStopUnverified = Phrase{
		Outcome:     "The worker could not be shown to have ended.",
		Consequence: "No decision was opened, so the work is as it was and may still be moving.",
		Decision:    "Look at the work directly, then ask for the stop again.",
	}
)

// MessagePhrase is what a person reads about a message as its record
// stands: read, once the worker acknowledged it; otherwise by its delivery,
// written and not yet read, refused, or not known.
func MessagePhrase(m store.Message) Phrase {
	switch {
	case m.Acknowledged():
		return phraseMessageRead
	case m.Delivery() == store.DeliveryWritten:
		return phraseMessageUnread
	case m.Delivery() == store.DeliveryRefused:
		return phraseMessageRefused
	}
	return phraseMessageUnknown
}

// StopPhrase is what a person reads about a stop that ended the worker: the
// row for the hold it registered, or, for a task whose work was already
// finished and so was owed no hold, the row saying nothing waits on them.
func StopPhrase(stopped TaskStopped) Phrase {
	if stopped.Finished != "" {
		return phraseFinishedWorkerStopped
	}
	return phraseWorkerStopped
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
	// PhraseNoticeQueued is the row for a wake pushed on the coordinator's
	// wake stream: a notice is waiting, and it stays waiting until it is
	// acknowledged, because neither the push nor a drain closes it. Which
	// work it is about and its kind are the structured rendering's.
	PhraseNoticeQueued = Phrase{
		Outcome:     "A notice about the fleet is waiting to be handled.",
		Consequence: "It stays waiting until it is acknowledged after it is acted on.",
		Decision:    "Run assistant --json coordinator wakes to take it, act on it, then acknowledge it.",
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

// The catching-up rows: what a person reads of PRD section 9's catch-up
// read, one heading per part with a row for the part when it is empty, the
// rows for a landed record and for the landed records a read left out, and
// the rows for a mark. Each heading is a row rather than a label, because a
// part's name is not a sentence P13 lets reach a person on its own, and the
// landed part's name is a state's word besides.
var (
	phraseWaitingOnYou = Phrase{
		Outcome:     "These decisions are waiting on you, oldest first.",
		Consequence: "The work each one holds does not move until it is answered.",
		Decision:    "Answer each as its line says.",
	}
	phraseNothingWaiting = Phrase{
		Outcome:     "Nothing is waiting on you.",
		Consequence: "No work is stopped for an answer of yours.",
		Decision:    "Nothing to decide.",
	}
	phraseLandedSince = Phrase{
		Outcome:     "This work was merged or cleared away since you last marked what you had seen, newest first.",
		Consequence: "It is shown here until you mark it as seen.",
		Decision:    "Once you have read it, run assistant watch --mark-seen to mark it.",
	}
	phraseNothingLanded = Phrase{
		Outcome:     "Nothing was merged or cleared away since you last marked what you had seen.",
		Consequence: "There is nothing new to read here.",
		Decision:    "Nothing to decide.",
	}
	phraseUnderWay = Phrase{
		Outcome:     "This work is under way.",
		Consequence: "It moves without an answer from you, unless its line says otherwise.",
		Decision:    "Nothing to decide unless a line says so.",
	}
	phraseNothingUnderWay = Phrase{
		Outcome:     "Nothing is under way.",
		Consequence: "No work is moving right now.",
		Decision:    "Nothing to decide.",
	}
	phraseStoppedUndecided = Phrase{
		Outcome:     "This work stopped without asking you anything.",
		Consequence: "Nothing more happens to it until it is picked up or let go.",
		Decision:    "Read each line for what to do with it.",
	}
	phraseNothingStopped = Phrase{
		Outcome:     "No work stopped without asking you anything.",
		Consequence: "Nothing is left stuck without a decision.",
		Decision:    "Nothing to decide.",
	}
	phraseWasMerged = Phrase{
		Outcome:     "This work was merged.",
		Consequence: "It is on the shared branch.",
		Decision:    "Nothing to decide.",
	}
	phraseCopyCleared = Phrase{
		Outcome:     "This work's local copy was removed after it was merged.",
		Consequence: "Nothing of it is left here, and nothing was lost.",
		Decision:    "Nothing to decide.",
	}
	phraseNothingMarked = Phrase{
		Outcome:     "Nothing was marked as seen, because nothing merged or cleared away was shown.",
		Consequence: "What you marked before stands.",
		Decision:    "Nothing to decide.",
	}
)

// CatchUpPhrases is what a person reads at the head of each of the catch-up
// read's four parts, in the order the read gives them: the row for the
// part, or the part's empty row when it lists nothing. MoreLanded is not a
// part and has a row of its own, LandedMorePhrase.
func CatchUpPhrases(c CatchUp) (waiting, landed, underWay, stopped Phrase) {
	waiting, landed, underWay, stopped = phraseWaitingOnYou, phraseLandedSince, phraseUnderWay, phraseStoppedUndecided
	if len(c.Waiting) == 0 {
		waiting = phraseNothingWaiting
	}
	if len(c.Landed) == 0 {
		landed = phraseNothingLanded
	}
	if len(c.UnderWay.Tasks)+len(c.UnderWay.Runs) == 0 {
		underWay = phraseNothingUnderWay
	}
	if len(c.Stopped) == 0 {
		stopped = phraseNothingStopped
	}
	return waiting, landed, underWay, stopped
}

// LandedPhrase is what a person reads about one landed record: the merge
// row for a merge record and the cleared-away row for a cleanup record.
// It is the record's row and not the task's, so a task whose copy was
// removed after it merged reads as merged on its merge record's line.
func LandedPhrase(l Landed) Phrase {
	if l.Cleanup != nil && l.Merge == nil {
		return phraseCopyCleared
	}
	return phraseWasMerged
}

// LandedMorePhrase is the row for the landed records a read counted and did
// not show, because LandedLimit bounds a read. They stay unseen, so marking
// what was shown is what brings them into the next read.
func LandedMorePhrase(more int) Phrase {
	return Phrase{
		Outcome:     fmt.Sprintf("And %d more were merged or cleared away that are not shown here.", more),
		Consequence: "They stay unseen, and are shown once those above are marked as seen.",
		Decision:    "Mark those above as seen to read the rest.",
	}
}

// MarkedPhrase is what a person reads once a mark was made: how many of
// the records the read showed are now marked, or that nothing was, because
// a read that showed nothing landed has nothing to mark.
func MarkedPhrase(m Marked) Phrase {
	if len(m.Marks) == 0 {
		return phraseNothingMarked
	}
	return Phrase{
		Outcome:     fmt.Sprintf("What was shown above as merged or cleared away, %d item(s), is now marked as seen.", len(m.Marks)),
		Consequence: "It is not shown here again; anything that arrived after this read still is.",
		Decision:    "Nothing to decide.",
	}
}

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

// The copy sweep row, for the record of the latest sweep of a run's local
// copy that did not establish the copy clear: the platform has no sweep, the
// sweep could not be made, or it left something it could not end. A cleared
// sweep and a run with no copy standing have no row, because nothing a person
// decides turns on either.
var phraseCopyNotSwept = Phrase{
	Outcome:     "Something this run set going may still be at work in its local copy after the run ended.",
	Consequence: "It could keep using this machine or changing the files left there.",
	Decision:    "Look at what this machine has going and stop anything left in that copy that should not be.",
}

// CopySweepPhrase is what a person reads about the latest sweep of a run's
// local copy, and false for an outcome no row phrases: the one line for a
// sweep that did not establish the copy clear, whichever of the three ways
// that happened, because what a person does about each is the same. Which
// way, and the counts, are the structured rendering's.
func CopySweepPhrase(w store.RunCopySweep) (Phrase, bool) {
	switch w.Outcome {
	case store.SweepUnsupported, store.SweepFailed, store.SweepNotCleared:
		return phraseCopyNotSwept, true
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

// Decision rows: what a person reads about a hold a run stopped at. The
// halt point's own question is not one of them, because internal/pipeline
// writes it naming the stage that holds and a stage's name is mechanics; the
// question stays on the structured rendering. The options are filled in,
// because they are the words a person types to answer and an answer the
// command line takes is not a word P13 keeps from them.
const optionsPlaceholder = "one of the answers it offers"

// DecisionPhrase is what a person reads about a decision a run is waiting
// on: that it waits on them, that nothing moves until they answer, and the
// answers it takes. A decision listing no options, which a halt point the
// engine accepts does not produce, is phrased with a placeholder rather than
// as an answer with nothing after it.
func DecisionPhrase(d Decision) Phrase {
	options := strings.Join(d.Options, ", ")
	if strings.TrimSpace(options) == "" {
		options = optionsPlaceholder
	}
	return Phrase{
		Outcome:     "Waiting on you: a decision about this change.",
		Consequence: "Nothing about it moves until you answer.",
		Decision:    "Answer it with assistant --answer followed by one of: " + options + ".",
	}
}

// FixRoundPhrase is the row for a hold that also takes a request for a fix
// round, with the answer that asks for one filled in. It points at the
// structured rendering for the findings it does not list, because only the
// findings holding the change are relayed beneath it and --select takes any
// the stage reported.
func FixRoundPhrase(option string) Phrase {
	return Phrase{
		Outcome:     "You may ask for a fix round instead of deciding.",
		Consequence: "Only the findings holding this change are listed below; every finding behind it, with the name to select it by, is in assistant --json status.",
		Decision:    "Ask with assistant --answer " + option + " --select and the findings to fix, separated by commas; --note and --finding add your own words.",
	}
}

// Evidence rows: the message that points a person at one of the two
// detailed evidence documents PRD section 9 names, the two notices about a
// request and the one about the run's standing consent that sit beside a
// run's evidence without being part of it. P13
// lets an evidence document keep exact identifiers and stage names and keeps
// the message that points at one phrased, so these rows are what a person
// reads before the evidence, and the evidence itself is not a row here.
var (
	// PhraseRunRecordFollows points at a run's own record: the detail view's
	// evidence, which names where execution stands and each stage as this
	// build records them.
	PhraseRunRecordFollows = Phrase{
		Outcome:     "This run's own record follows, in the exact terms it was kept in.",
		Consequence: "It is there to be read and changes nothing said above.",
		Decision:    "Nothing to decide from it alone.",
	}
	// PhraseFindingsFollow points at the findings holding a decision, which
	// are relayed in full beneath it with their exact names and locations,
	// because a finding a person is asked to decide on is relayed unsummarized.
	PhraseFindingsFollow = Phrase{
		Outcome:     "The findings holding this decision follow in full, as they were written.",
		Consequence: "Each keeps its exact name and, where it has one, its location, so nothing in them was summarized away.",
		Decision:    "Decide with them in view.",
	}
	// PhraseUsageFollows points at section 9's usage view, an evidence
	// document whose groups carry agent names, model identifiers, purposes,
	// run identifiers and stage names as the invocation record holds them.
	// It says what the view leaves out, because the view is read as a cost
	// and a cost that silently omits a worker's turns or reads a missing
	// count as nothing would be read wrong.
	PhraseUsageFollows = Phrase{
		Outcome:     "What the agent calls made to vet your changes cost follows, in the exact terms each call was recorded in.",
		Consequence: "It counts those calls and nothing else: a worker's own turns are not in it, it carries no price, and a count the agent did not report is shown as unreported rather than as nothing.",
		Decision:    "Nothing to decide from it alone.",
	}
	// PhraseStageLogFollows points at the end of one stage's log in one run,
	// an evidence document of its own: what the stage and its agent calls
	// wrote, with each call's account and its full reply, under the exact
	// names the log was written with. It says the text is cut to its end and
	// has had credentials removed, because a reader who took either for the
	// whole verbatim output would read it wrong.
	PhraseStageLogFollows = Phrase{
		Outcome:     "What this step of the run wrote while it worked follows, in the exact words it was recorded in.",
		Consequence: "Only its end is shown, with a line saying where the whole of it is, and credentials were removed before it was kept.",
		Decision:    "Nothing to decide from it alone.",
	}
	// PhraseInputsNotApplied is the row for a request whose run-starting
	// inputs went to no run, because the branch already had one and the
	// request attached to it. Which inputs they were is the structured
	// rendering's.
	PhraseInputsNotApplied = Phrase{
		Outcome:     "What you gave to start a run with was not used, because this branch already had a run and this is that run.",
		Consequence: "That run goes on as it was begun, without it.",
		Decision:    "Give it again when this branch next starts a run; assistant --json names what was not used.",
	}
	// PhraseStandingConsent is the row beside a run whose record says it was
	// put under standing consent: the program answered the decisions about
	// findings that come before the change is sent upstream, each answer is
	// recorded as the program's, and the decisions it never answers still
	// come to the person. When consent was given is the structured
	// rendering's.
	PhraseStandingConsent = Phrase{
		Outcome:     "This change runs under standing consent, so the program itself answers the decisions about its findings that come before it is sent upstream.",
		Consequence: "Each of those answers is recorded as the program's rather than yours, and a decision about sending the change, its pull request or its checks still comes to you.",
		Decision:    "Nothing to decide from it alone; assistant --json says when consent was given.",
	}
	// PhraseAnswerNotApplied is the row for an answer the service did not
	// apply because the decision it answered had been closed first, on a
	// re-read of the run's pull request. The service's own sentence for why
	// is the structured rendering's.
	PhraseAnswerNotApplied = Phrase{
		Outcome:     "Your answer was not applied, because the decision it answered was closed first when the pull request was read again.",
		Consequence: "The run ended on that, as said above, so there was nothing left for the answer to act on.",
		Decision:    "Nothing to decide; assistant --json says why it was closed.",
	}
)

// The checks assistant doctor makes, by the name Check carries. The names are
// the structured rendering's and travel as they always have; they are
// constants here so that the command that makes a check and the row that
// phrases it name it from one place. A check made under a name with no row
// reads as phraseCheckUnfamiliar here, and internal/cli's command-line test
// fails on it, because it reads the checks assistant doctor actually made and
// holds each name to CheckNames.
const (
	// CheckHome is whether the home can be used.
	CheckHome = "home"
	// CheckDatabase is whether the home's records can be opened.
	CheckDatabase = "database"
	// CheckWorkingCopy is whether the command was run inside a checkout.
	CheckWorkingCopy = "working copy"
	// CheckGate is whether the checkout is bound to a gate and has the
	// repository record a run is resolved against.
	CheckGate = "gate"
	// CheckCommitIdentity is whether git names an author and a committer
	// for a commit made in the checkout's gate, which is where a run's copy
	// takes its identity from.
	CheckCommitIdentity = "commit identity"
	// CheckService is whether a service answered for the home.
	CheckService = "service"
	// CheckAgent is whether a coding agent resolves for a run.
	CheckAgent = "agent"
	// CheckNamedOnlyAgents is whether any adapter resolves that a run uses
	// only when configuration names it.
	CheckNamedOnlyAgents = "named-only agents"
	// CheckCodeHost is whether the code host's command line resolves.
	CheckCodeHost = "code host"
	// CheckStages is whether every stage has an implementation in this build.
	CheckStages = "stages"
)

// CheckNames is every check name phraseByCheck has rows for, in the order
// assistant doctor makes them. The result is a copy.
func CheckNames() []string {
	return []string{CheckHome, CheckDatabase, CheckWorkingCopy, CheckGate, CheckCommitIdentity, CheckService,
		CheckAgent, CheckNamedOnlyAgents, CheckCodeHost, CheckStages}
}

// readDoctor is the decision every row for a check that found something
// points at when what was found is the thing to read: the detail is a path
// or an error, which is the structured rendering's.
const readDoctor = "Run assistant --json doctor for what was found."

// phraseByCheck is the one table from a check and its result to what a person
// reads, the passing row first. The detail a check carries - a path, a
// socket, an error, an agent's name - is mechanics, and the structured
// rendering keeps it; a row that needs it to be acted on says so.
var phraseByCheck = map[string][2]Phrase{
	CheckHome: {
		{Outcome: "This home can be used.", Consequence: "What is recorded about your work is kept in it.", Decision: "Nothing to do."},
		{Outcome: "This home cannot be used.", Consequence: "Nothing can be recorded or validated until it can.", Decision: readDoctor},
	},
	CheckDatabase: {
		{Outcome: "This home's records can be read.", Consequence: "Runs and fleet work can be recorded.", Decision: "Nothing to do."},
		{Outcome: "This home's records could not be opened.", Consequence: "Nothing can be recorded, so no run can start.", Decision: readDoctor},
	},
	CheckWorkingCopy: {
		{Outcome: "This directory is inside a checkout.", Consequence: "There is a change here a run can validate.", Decision: "Nothing to do."},
		{Outcome: "This directory is not inside a checkout.", Consequence: "There is nothing here for a run to validate.", Decision: "Run assistant from inside the checkout you want validated."},
	},
	CheckGate: {
		{Outcome: "This checkout is set up for validation.", Consequence: "A change sent from here is validated before it goes anywhere.", Decision: "Nothing to do."},
		{Outcome: "This checkout is not set up for validation.", Consequence: "A run cannot start from it.", Decision: "Run assistant init here."},
	},
	CheckCommitIdentity: {
		{Outcome: "Git names who a change made during a run is recorded under.", Consequence: "A step that has to record a change can do so.", Decision: "Nothing to do."},
		{Outcome: "Git could not name who a change made during a run is recorded under.",
			Consequence: "A run can still start, and a step that has to record a change, such as a fix round or bringing your branch up to date, fails until git has both.",
			Decision: `Run git config --global user.name "Your Name" and git config --global user.email "you@example.com", then run assistant doctor again; ` +
				"if both are already set, run assistant --json doctor for what git said."},
	},
	CheckService: {PhraseServiceUp, PhraseServiceAbsent},
	CheckAgent: {
		{Outcome: "A coding agent this build can drive is available.", Consequence: "A run has an agent to examine and fix the change with.", Decision: "Nothing to do."},
		{Outcome: "No coding agent this build can drive is available here.", Consequence: "A run cannot start without one.", Decision: readDoctor},
	},
	CheckNamedOnlyAgents: {
		{Outcome: "No agent is installed here that is used only when named.", Consequence: "A run uses the agent it chooses on its own.", Decision: "Nothing to do."},
		{Outcome: "Some agents installed here are used only when your configuration names them.", Consequence: "A run never chooses one on its own, and nothing here has checked them against their own command lines.", Decision: "Name one in your configuration only if you mean to try it."},
	},
	CheckCodeHost: {
		{Outcome: "The code host's command line was found here.", Consequence: "Whether it can reach your repository is learned when a run first needs it.", Decision: "Nothing to do."},
		{Outcome: "The code host's command line was not found here.", Consequence: "A run can still start, and a step that needs the code host stops and says so.", Decision: readDoctor},
	},
	CheckStages: {
		{Outcome: "Every step of the gate has an implementation in this build.", Consequence: "No step stops for your decision only for want of one.", Decision: "Nothing to do."},
		{Outcome: "Some steps of the gate have no implementation in this build.", Consequence: "A run stops for your decision at each of them, and what they would check is not checked.", Decision: "Answer each as it comes, and run assistant --json doctor for which they are."},
	},
}

// phraseCheckUnfamiliar is the row for a check under a name the table has
// none for, which internal/cli's command-line test keeps assistant doctor
// from making.
var phraseCheckUnfamiliar = Phrase{
	Outcome:     "Something this build checked is not one it can describe.",
	Consequence: "Whether it stops a run is in the verdict below.",
	Decision:    readDoctor,
}

// CheckPhrase is what a person reads about one check: phraseByCheck's row for
// its name and result, or the unfamiliar row for a name it has none for.
func CheckPhrase(c Check) Phrase {
	rows, ok := phraseByCheck[c.Name]
	if !ok {
		return phraseCheckUnfamiliar
	}
	if c.OK {
		return rows[0]
	}
	return rows[1]
}

// The doctor's verdict, which is the decision PRD section 9 asks it to make.
var (
	phraseRunCanStart = Phrase{
		Outcome:     "A run can start here.",
		Consequence: "Everything a run needs is in place.",
		Decision:    "Nothing to do.",
	}
	phraseRunCannotStart = Phrase{
		Outcome:     "A run cannot start here.",
		Consequence: "Something it needs is missing, as the lines above say.",
		Decision:    "Do what the first of them says, then run assistant doctor again.",
	}
)

// DoctorPhrase is the doctor's verdict as a person reads it, read off the
// decision the report carries rather than inferred from its checks.
func DoctorPhrase(d Doctor) Phrase {
	if d.CanStartRun {
		return phraseRunCanStart
	}
	return phraseRunCannotStart
}

// Lifecycle rows: what a person reads when they asked the service to stop or
// restart. A restart is answered only once the replacement has answered a
// readiness check, so its row says the service is back rather than going.
var (
	phraseServiceRestarted = Phrase{
		Outcome:     "The service restarted and is answering again.",
		Consequence: "A change sent to the gate is validated as before.",
		Decision:    "Nothing to do.",
	}
	phraseServiceStopped = Phrase{
		Outcome:     "The service is stopping.",
		Consequence: "Nothing is validated or watched until it is brought back up.",
		Decision:    "Run assistant service start when you want it back.",
	}
)

// LifecyclePhrase is what a person reads about a request to stop or restart
// the service: the restarted or stopping row when it was accepted, and when it
// was refused because runs are under way, a row counting them. The runs
// themselves are the renderer's to list beside it, each by its branch.
func LifecyclePhrase(l Lifecycle) Phrase {
	switch {
	case l.Accepted && l.Restarting:
		return phraseServiceRestarted
	case l.Accepted:
		return phraseServiceStopped
	}
	return Phrase{
		Outcome:     fmt.Sprintf("The service was left up, because %d run(s) are under way.", len(l.Active)),
		Consequence: "Stopping it now would interrupt them.",
		Decision:    "Let them finish, or repeat the command with --force to interrupt them anyway.",
	}
}

// subcommandsPlaceholder stands in for a command's subcommands in
// SubcommandPhrase's row when it is listed without them, as TablePhrases
// lists it.
const subcommandsPlaceholder = "the subcommands it takes"

// SubcommandPhrase is what a person reads when a command that does its work
// through a subcommand was given none of its subcommands: given says whether
// a word stood where one belongs, and subcommands are the ones it takes, in
// the order the command's table holds them. The decision names them, because
// they are the words a person types next, and says --help lists what each
// one does. It is the refusal's sentence and decides nothing about the exit
// code, which is the caller's.
func SubcommandPhrase(given bool, subcommands []string) Phrase {
	listed := strings.Join(subcommands, ", ")
	if strings.TrimSpace(listed) == "" {
		listed = subcommandsPlaceholder
	}
	outcome := "Nothing was done, because this command needs a subcommand to say what to do and was given none."
	if given {
		outcome = "Nothing was done, because this command needs a subcommand to say what to do and none it takes was given."
	}
	return Phrase{
		Outcome:     outcome,
		Consequence: "Nothing changed.",
		Decision:    "Repeat it with one of " + listed + " right after the command; --help lists what each one does.",
	}
}

// The rows for a rerun refused because the checkout stands at a commit other
// than the one the rerun would validate, chosen by RerunMoved.Phrase. Which
// two commits, and the commands for the remedy, are RerunMoved's to print
// beside the row.
var (
	// phraseRerunMoved is RerunMovedElsewhere's row: the remedy validates the
	// person's own commit.
	phraseRerunMoved = Phrase{
		Outcome:     "No run was begun, because the commit this checkout stands at is not the one a fresh run of its branch would validate.",
		Consequence: "A fresh run would vet the commit the gate last recorded for this branch rather than yours, so none was begun and nothing in this checkout was changed.",
		Decision:    "Send your commit to the gate, or start a run on it with the command named after this.",
	}
	// phraseRerunBehind is RerunMovedBehind's row: the commit a rerun would
	// validate is the last run's work and already contains the person's, so
	// the remedy brings that work into the checkout rather than validating
	// the older commit.
	phraseRerunBehind = Phrase{
		Outcome:     "No run was begun, because this checkout is behind: the last run's work goes past the commit it stands at.",
		Consequence: "A fresh run would vet that work rather than what you have here, so none was begun and nothing in this checkout was changed.",
		Decision:    "Bring the last run's work into this checkout with the command named after this, which shows its plan before it changes anything.",
	}
)

// The rows for a cancellation refused because there was nothing to end,
// chosen by CancelRefused.Phrase. Which run was named, and the status an
// ended one was recorded in, are CancelRefused's to print beside the row.
var (
	// phraseCancelUnknown is CancelUnknownRun's row.
	phraseCancelUnknown = Phrase{
		Outcome:     "Nothing was ended, because no run in this home has the identifier you gave.",
		Consequence: "Nothing changed.",
		Decision:    "Check the identifier, which assistant runs prints beside each run it lists, and repeat the command with it.",
	}
	// phraseCancelEnded is CancelRunEnded's row.
	phraseCancelEnded = Phrase{
		Outcome:     "Nothing was ended, because the run you named had already ended.",
		Consequence: "What it recorded stands as it was, and nothing changed.",
		Decision:    "There is nothing to end; to validate the change again, start a fresh run of its branch.",
	}
)

// InitPhrase is what a person reads once a gate was created or reattached for
// their checkout. The next decision names the remote a change is sent to,
// which is internal/gate's, because sending a branch there is how a person
// starts a run and nothing else on the line says so.
func InitPhrase(i Init) Phrase {
	decision := "Send your branch to the remote named " + gate.RemoteName + " to have it validated."
	if i.Reattached {
		return Phrase{
			Outcome:     "This checkout was reattached to the gate it had before.",
			Consequence: "The runs recorded through it carried over, and a change sent to it is validated before it goes anywhere.",
			Decision:    decision,
		}
	}
	return Phrase{
		Outcome:     "This checkout now sends its changes through a new gate.",
		Consequence: "A change sent to it is validated before it goes anywhere, and nothing has been validated through it yet.",
		Decision:    decision,
	}
}

// EjectPhrase is what a person reads once a gate was removed, with how many
// run records went with it. An answer that removed nothing, which eject does
// not return, reads as nothing removed rather than as a removal.
func EjectPhrase(e Eject) Phrase {
	if !e.Removed {
		return Phrase{
			Outcome:     "Nothing was removed.",
			Consequence: "The gate and its records are as they were.",
			Decision:    "Nothing to decide.",
		}
	}
	return Phrase{
		Outcome:     fmt.Sprintf("The gate was removed from this checkout, with %d recorded run(s).", e.Runs),
		Consequence: "Nothing sent from here is validated any more.",
		Decision:    "Run assistant init here to set one up again.",
	}
}

// HealthPhrase is what a person reads about a readiness answer: a service
// that answered is the status row for one that is up, and anything else the
// row for one that is not.
func HealthPhrase(h Health) Phrase {
	if h.Ready {
		return PhraseServiceUp
	}
	return PhraseServiceAbsent
}

// The gate's rows: what a person sending a change reads, relayed by git from
// the gate's hooks. The references and branches beside them are the person's
// own names; the gate's identifier, a run's and the commit are the structured
// rendering's.
var (
	// PhraseAdmitted is the row for a change the gate let in.
	PhraseAdmitted = Phrase{
		Outcome:     "The gate let this change in.",
		Consequence: "It is validated before it goes anywhere.",
		Decision:    "Nothing to decide yet.",
	}
	// PhraseValidationBegun is the row for a branch a change started a run
	// for. The run goes on in the service, which is why the command that
	// sent the change returns at once.
	PhraseValidationBegun = Phrase{
		Outcome:     "Validation of this change has begun.",
		Consequence: "It goes on in the service, so nothing here waits for it.",
		Decision:    "Ask assistant status from its branch for where it stands.",
	}
	// PhraseSupersededEarlier is the row for a run of the same branch the new
	// one displaced. It says what the record says and no more: the earlier
	// run is recorded as ended, which is the only verdict it gives.
	PhraseSupersededEarlier = Phrase{
		Outcome:     "The earlier version of this branch that was being validated was set aside for this one.",
		Consequence: "Its run is recorded as ended and gives no verdict.",
		Decision:    "Nothing to decide.",
	}
	// PhraseNothingStartedFor is the row for a reference a change carried
	// that started no run: one that is not a branch, or a branch being
	// deleted. A run that branch already had is left as it was, which is
	// the one thing a person might want to act on.
	PhraseNothingStartedFor = Phrase{
		Outcome:     "No run was begun for this, because the gate begins one only for a branch moved to a new commit.",
		Consequence: "Nothing new is validated, and a run already under way for it carries on.",
		Decision:    "If that run should not carry on, end it with assistant --cancel from its branch.",
	}
	// PhraseCopyBusy is the row for a change that arrived while a run's
	// copy was being set up or cleaned up in the gate, and is held back,
	// for internal/gate's bound at most, until that is done. Git shows it
	// to the person sending the change as it happens, so a pause is never
	// silent.
	PhraseCopyBusy = Phrase{
		Outcome:     "A local copy for a run is being set up or cleaned up where this change is received.",
		Consequence: "The change is taken in once that is done, within a bounded time, and nothing you sent is lost meanwhile.",
		Decision:    "Nothing to decide yet.",
	}
	// PhraseCopyStillBusy is the row for a change refused because a run's
	// copy was still being set up or cleaned up when the bound ran out. It
	// names that state, because the person sending did nothing wrong and
	// the cure is to send again.
	PhraseCopyStillBusy = Phrase{
		Outcome:     "A local copy for a run was still being set up or cleaned up when the time allowed for it ran out, so this change was not taken in.",
		Consequence: "Nothing was received, and your branch is as you left it.",
		Decision:    "Send it again in a moment.",
	}
	// PhraseNothingSent is the row for a change that moved no reference at
	// all.
	PhraseNothingSent = Phrase{
		Outcome:     "Nothing that was sent moved a branch, so no run was begun.",
		Consequence: "Nothing is validated.",
		Decision:    "Nothing to decide.",
	}
)

// PhraseNoReading is what a person reads when an answer reaches the human
// rendering with no renderer for its type. It is a refusal rather than a
// guess: printing the value as Go prints it would hand a person this build's
// field names and identifiers, which is what P13 keeps from them.
var PhraseNoReading = Phrase{
	Outcome:     "This answer has no reading for a person in this build.",
	Consequence: "Nothing was printed in its place rather than a guess at what it meant.",
	Decision:    "Repeat the command with --json to read it.",
}

// TablePhrases returns every row this table holds, as a person reads each,
// which is what the vocabulary test walks: the pull guard's three rows, the
// stop guard's two turn rows, a row per task state, the hold tier's row,
// NoWordYet, the local landing row and the two rows for an answer given a
// task that reached no decision, a row per outcome and the three OutcomeExecuting splits into,
// the three hold rows (open, answered, and open naming a task), the six
// rows for an answer to a task's hold and the two cleanup rows, the
// stream's four, the status rows, and the rows for the rest of the command
// surface: a decision and a fix round's offer, a row per doctor check and
// result and the verdict, the service lifecycle, the two rows for a command
// given none of its subcommands, the two rows for a rerun refused because
// the checkout moved, a gate created, reattached and removed, a row per source of the
// default branch an init recorded with the row for an answer naming none and
// a row per refusal to record one, the row for an init that could not learn
// where changes go, a row per refusal of a fork and the row for one
// recorded, a row per refusal internal/managed makes, the refusal of an
// install beside a service that is up, and the rows for a managed service
// set up, removed and ending for a restart, the gate's own rows, and the rows pointing
// at a run's evidence
// and the three notices beside it, and the catch-up read's: a heading and an
// empty row per part, a row per kind of landed record, the row for what a
// read left out, and the two rows for a mark, and custody's: a row per class
// planned and carried out and as status prints it, a row per refusal, and a
// row per step, and the coordinator client's: a created task begun and one
// recorded behind other work, a promotion, a relaunch, a drain with and
// without notices, an acknowledgement, and arm verification's three. A templated row is listed with its
// placeholder filled in. The result is a copy. Phrases is the same list as
// strings.
func TablePhrases() []Phrase {
	out := []Phrase{phraseServiceDown, phraseServiceNotWatching, phraseUndecided}
	out = append(out, phraseTurnBlocked, phraseTurnReEntered)
	for _, result := range HostInstallResults() {
		if result != HostInstallRefused {
			out = append(out, HostInstallPhrase(HostInstall{Result: result}))
		}
	}
	for _, refusal := range HostInstallRefusals() {
		out = append(out, HostInstallPhrase(HostInstall{Result: HostInstallRefused, Refusal: refusal}))
	}
	for _, state := range taskstate.States() {
		if p, ok := phraseByState[state]; ok {
			out = append(out, p)
		}
	}
	out = append(out, phraseTaskHeld, NoWordYet, phraseTaskLandedLocally, PhraseTaskOwnHoldFirst, PhraseTaskNothingWaiting)
	for _, outcome := range Outcomes() {
		if p, ok := phraseByOutcome[outcome]; ok {
			out = append(out, p)
		}
	}
	out = append(out, phraseRunAdvancing, phraseRunStalled, phraseRunUnrecorded)
	for _, bound := range store.ParkBounds() {
		if p, ok := phraseByPark[bound]; ok {
			out = append(out, p)
		}
	}
	out = append(out, HoldPhrase(store.Hold{Subject: holdPlaceholder}), HoldPhrase(store.Hold{Subject: holdPlaceholder, Resolution: store.Known("answered")}))
	out = append(out, HoldPhrase(store.Hold{Subject: holdPlaceholder, TaskID: store.Known("a task")}))
	out = append(out, phraseRetried, phraseRedispatched, phraseRetryDeferred, phraseRetryHeld, phraseRetryAtBound, phraseRetryRefused, phraseRetryUndispatchable, phraseNotRetried, phraseLandedLocallyNow)
	out = append(out, phraseMessageUnread, phraseMessageRead, phraseMessageRefused, phraseMessageUnknown,
		phraseWorkerStopped, phraseFinishedWorkerStopped, PhraseStopUnverified)
	out = append(out, phraseCopyKept, phraseCopyRemoved, phraseCopyNotSwept)
	out = append(out, PhraseUpdatesMissed, PhraseServiceStopping, PhraseWorkerSpoke, PhraseNoticeQueued, PhraseActivity, PhraseUnfamiliarUpdate)
	out = append(out, PhraseServiceUp, PhraseServiceAbsent, PhraseGated, PhraseUngated, PhraseNoRun, PhraseNoRuns, PhraseNoFleetWork)
	out = append(out, DecisionPhrase(Decision{}), FixRoundPhrase(string(pipeline.OutcomeFixRequested)))
	for _, name := range CheckNames() {
		out = append(out, CheckPhrase(Check{Name: name, OK: true}), CheckPhrase(Check{Name: name}))
	}
	out = append(out, phraseCheckUnfamiliar, phraseRunCanStart, phraseRunCannotStart)
	out = append(out, LifecyclePhrase(Lifecycle{Accepted: true, Restarting: true}), LifecyclePhrase(Lifecycle{Accepted: true}), LifecyclePhrase(Lifecycle{}))
	out = append(out, SubcommandPhrase(false, nil), SubcommandPhrase(true, nil), phraseRerunMoved, phraseRerunBehind,
		phraseCancelUnknown, phraseCancelEnded)
	out = append(out, InitPhrase(Init{}), InitPhrase(Init{Reattached: true}), EjectPhrase(Eject{Removed: true}), EjectPhrase(Eject{}))
	out = append(out, PhraseAdmitted, PhraseValidationBegun, PhraseSupersededEarlier, PhraseNothingStartedFor, PhraseNothingSent, PhraseNoReading)
	out = append(out, PhraseCopyBusy, PhraseCopyStillBusy)
	out = append(out, PhraseRunRecordFollows, PhraseFindingsFollow, PhraseUsageFollows, PhraseStageLogFollows, PhraseInputsNotApplied, PhraseAnswerNotApplied,
		PhraseStandingConsent)
	out = append(out, phraseWaitingOnYou, phraseNothingWaiting, phraseLandedSince, phraseNothingLanded,
		phraseUnderWay, phraseNothingUnderWay, phraseStoppedUndecided, phraseNothingStopped)
	out = append(out, phraseWasMerged, phraseCopyCleared, LandedMorePhrase(LandedLimit), MarkedPhrase(Marked{}),
		MarkedPhrase(Marked{Marks: []store.SeenMark{{}}}))
	out = append(out, defaultBranchRows()...)
	out = append(out, upstreamRows()...)
	out = append(out, forkRows()...)
	out = append(out, managedRows()...)
	out = append(out, commandsRows()...)
	out = append(out, custodyRows()...)
	return append(out, coordinatorRows()...)
}
