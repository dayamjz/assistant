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
	return []string{CheckHome, CheckDatabase, CheckWorkingCopy, CheckGate, CheckService,
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
// stop guard's two turn rows, a row per task state and NoWordYet, a row per
// outcome and the three OutcomeExecuting splits into, the two hold rows and
// the two cleanup rows, the stream's four, the status rows, and the rows for
// the rest of the command surface: a decision and a fix round's offer, a row
// per doctor check and result and the verdict, the service lifecycle, a gate
// created, reattached and removed, and the gate's own rows. A templated
// row is listed with its placeholder filled in. The result is a copy. Phrases
// is the same list as strings.
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
	out = append(out, DecisionPhrase(Decision{}), FixRoundPhrase(string(pipeline.OutcomeFixRequested)))
	for _, name := range CheckNames() {
		out = append(out, CheckPhrase(Check{Name: name, OK: true}), CheckPhrase(Check{Name: name}))
	}
	out = append(out, phraseCheckUnfamiliar, phraseRunCanStart, phraseRunCannotStart)
	out = append(out, LifecyclePhrase(Lifecycle{Accepted: true, Restarting: true}), LifecyclePhrase(Lifecycle{Accepted: true}), LifecyclePhrase(Lifecycle{}))
	out = append(out, InitPhrase(Init{}), InitPhrase(Init{Reattached: true}), EjectPhrase(Eject{Removed: true}), EjectPhrase(Eject{}))
	out = append(out, PhraseAdmitted, PhraseValidationBegun, PhraseSupersededEarlier, PhraseNothingStartedFor, PhraseNothingSent, PhraseNoReading)
	out = append(out, PhraseCopyBusy, PhraseCopyStillBusy)
	return out
}
