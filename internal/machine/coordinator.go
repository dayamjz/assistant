package machine

import (
	"fmt"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// The coordinator client's rows: what a person reads of the answers to the
// coordinator's methods that the steering rows in phrases.go do not already
// phrase. A message and a stop read as MessagePhrase and StopPhrase; a
// created task, a promotion, a relaunch, a relay, a drain of the wake queue, an
// acknowledgement and arm verification read as the rows here.
//
// None names a wake's kind, a task's state or the arm's outcome, because
// each is a word the derived forbidden set keeps from a person; the
// structured rendering carries all three, and the rows that need one say to
// read it. None says a worker began either: task.create and task.relaunch
// answer once their records are written and before the service launches
// anything, so the rows say where to read whether it did.
var (
	phraseTaskCreated = Phrase{
		Outcome:     "The work was recorded, and a worker is being begun for it in a local copy of its own.",
		Consequence: "This answer comes before the worker does, so whether it began shows in the listing.",
		Decision:    "Nothing to decide now; run assistant tasks to see how it goes.",
	}
	phraseTaskCreatedBehind = Phrase{
		Outcome:     "The work was recorded behind other work it overlaps, and nothing begins on it yet.",
		Consequence: "It is begun once that other work has cleared.",
		Decision:    "Nothing to decide now; run assistant tasks to see what it is behind.",
	}
	phraseTaskPromoted = Phrase{
		Outcome:     "The investigation now delivers a change, on a branch of its own in the same local copy.",
		Consequence: "Nothing it had already done there was moved or lost, and its worker was sent what it is now to deliver.",
		Decision:    "Nothing to decide now; run assistant tasks to see how it goes.",
	}
	phraseTaskPromotedUnsent = Phrase{
		Outcome:     "The investigation now delivers a change, on a branch of its own in the same local copy.",
		Consequence: "Nothing it had already done there was moved or lost, but its worker could not be sent what it is now to deliver; a worker begun on it later is told.",
		Decision:    "Read why with --json, and send its worker the new direction with assistant coordinator message if it is still at work; run assistant tasks to see how it goes.",
	}
	phraseTaskRelaunched = Phrase{
		Outcome:     "The work was begun again in its own local copy, by a new worker.",
		Consequence: "This answer comes before the worker does, so whether it began shows in the listing.",
		Decision:    "Nothing to decide now; run assistant tasks to see how it goes.",
	}
	phraseTaskRelayed = Phrase{
		Outcome:     "The investigation is recorded as finished: its findings were passed on.",
		Consequence: "Its worker is ended and its local copy is removed once that is proven safe; its findings stay on record.",
		Decision:    "Nothing to decide.",
	}
	phraseNoNotices = Phrase{
		Outcome:     "No notices about the fleet are waiting to be handled.",
		Consequence: "Nothing about the work needs the coordinator's attention right now.",
		Decision:    "Nothing to decide.",
	}
	phraseFleetWatched = Phrase{
		Outcome:     "The service is up and watching the fleet.",
		Consequence: "A worker that stops or needs a decision will be noticed.",
		Decision:    "Nothing to decide.",
	}
	phraseFleetNoService = Phrase{
		Outcome:     "Nothing is watching the fleet, because no service could be found holding this home.",
		Consequence: "A worker that stops or needs a decision will go unnoticed until one is.",
		Decision:    "Run assistant service start.",
	}
	phrasePeekAttachable = Phrase{
		Outcome:     "The worker's recent output was read, and its terminal can be watched.",
		Consequence: "Its output is for the coordinator to read and is not repeated here; what you type in its terminal reaches the worker as its own input.",
		Decision:    "To watch it yourself, repeat with --json and run the command it names to attach; nothing to decide otherwise.",
	}
	phrasePeekUnattached = Phrase{
		Outcome:     "The worker's recent output was read; it runs without a terminal anyone can watch.",
		Consequence: "Its output is for the coordinator to read and is not repeated here.",
		Decision:    "Repeat with --json to read it; nothing to decide otherwise.",
	}
	phraseFleetNotWatched = Phrase{
		Outcome:     "The service is up, but nothing in it could be shown to be watching the fleet.",
		Consequence: "A worker that stops or needs a decision may go unnoticed.",
		Decision:    "Run assistant service restart.",
	}
	phraseDomainDelegated = Phrase{
		Outcome:     "The domain was recorded as delegated, and a coordinator of its own is being begun for it.",
		Consequence: "Notices about work in that domain that its coordinator can act on go to it; decisions still come to you.",
		Decision:    "Nothing to decide now; run assistant coordinator end-delegation to take the domain back.",
	}
	phraseDelegationEnded = Phrase{
		Outcome:     "The domain's delegation is recorded as ended.",
		Consequence: "Its work stays where it is, and every notice about it comes to you from now on.",
		Decision:    "Nothing to decide.",
	}
	phraseDomainRetried = Phrase{
		Outcome:     "Your answer was taken, and the domain's coordinator was begun again.",
		Consequence: "The domain's work stays where it is, and its coordinator picks it up from there.",
		Decision:    "Nothing to decide unless it ends again as many times as the program begins it, which asks you again.",
	}
	phraseDelegationEndedByAnswer = Phrase{
		Outcome:     "Your answer was recorded, and the domain's delegation is ended rather than begun again.",
		Consequence: "Its work stays where it is, and every notice about it comes to you from now on.",
		Decision:    "Nothing to decide.",
	}
)

// DomainAnsweredPhrase is what a person reads about an answer to a domain's
// hold, which TaskAnsweredPhrase dispatches to: a retry that began the
// domain's coordinator again, or a stop that ended the delegation. Any
// other effect reads as the hold's own row.
func DomainAnsweredPhrase(a TaskAnswered) Phrase {
	switch a.Effect {
	case AnswerRelaunched:
		return phraseDomainRetried
	case AnswerStopped:
		return phraseDelegationEndedByAnswer
	}
	return HoldPhrase(a.Hold)
}

// TaskCreatedPhrase is what a person reads about a task.create answer: the
// row for work recorded behind other work when the record names a blocker,
// which TaskCreated.Record does exactly when intake queued it, and the row
// for work a worker is being begun for otherwise.
func TaskCreatedPhrase(c TaskCreated) Phrase {
	if c.Record.BlockerTask.IsKnown() {
		return phraseTaskCreatedBehind
	}
	return phraseTaskCreated
}

// PeekPhrase is what a person reads about a task.peek answer: whether the
// worker's terminal can be watched. Neither the output nor the attach
// command is in a row: the output is never relayed to a person (P13), and
// the command names a path under the home, which the forbidden set keeps
// from a person, so the row says to read it from the structured answer.
func PeekPhrase(p TaskPeek) Phrase {
	if len(p.Attach) > 0 {
		return phrasePeekAttachable
	}
	return phrasePeekUnattached
}

// TaskPromotedPhrase is what a person reads about a task.promote answer: the
// row for a brief the record says reached the worker's input, and the row
// for one it does not say that of, which names what to do instead. A
// promotion that did not complete is a refusal and never an answer.
func TaskPromotedPhrase(p TaskPromoted) Phrase {
	if p.Brief.Delivered() {
		return phraseTaskPromoted
	}
	return phraseTaskPromotedUnsent
}

// TaskRelaunchedPhrase is what a person reads about a task.relaunch answer.
// A relaunch the records refused is a refusal and never an answer, so there
// is one row.
func TaskRelaunchedPhrase(TaskRelaunched) Phrase { return phraseTaskRelaunched }

// TaskRelayedPhrase is what a person reads about a task.relay answer. A
// relay the records refused is a refusal and never an answer, so there is
// one row.
func TaskRelayedPhrase(TaskRelayed) Phrase { return phraseTaskRelayed }

// DelegatedPhrase is what a person reads about a domain.delegate answer. A
// delegation the records refused is a refusal and never an answer, so
// there is one row, and it says the subordinate's start follows the answer,
// as TaskCreatedPhrase does of a worker.
func DelegatedPhrase(Delegated) Phrase { return phraseDomainDelegated }

// DelegationEndedPhrase is what a person reads about a domain.end answer.
func DelegationEndedPhrase(DelegationEnded) Phrase { return phraseDelegationEnded }

// WakesPhrase is what a person reads about a drain of the wake queue: how
// many rows the drain handed out, or that none were on offer. A drain closes
// nothing, so the phrase says the rows stay waiting until each is
// acknowledged, and that a row not acknowledged comes back, and how each
// shape is named on the acknowledge line, since a domain's and a kept work
// entry's notices are numbered apart from a task's. Which work each
// row is about and what kind of notice it is are the structured rendering's,
// because a kind is a word the derived forbidden set keeps from a person.
func WakesPhrase(w Wakes) Phrase {
	n := len(w.Wakes) + len(w.DomainWakes) + len(w.KeptWorkWakes)
	if n == 0 {
		return phraseNoNotices
	}
	return Phrase{
		Outcome:     fmt.Sprintf("%d notice(s) about the fleet were handed to the coordinator, oldest first, and none is recorded as handled yet.", n),
		Consequence: "Each stays waiting until it is acknowledged, and one not acknowledged is handed out again if the coordinator goes away first.",
		Decision:    "Act on each, then run assistant coordinator acknowledge with the numbers the --json form lists, a notice about a domain as dNUMBER and one about work left in a local copy as kNUMBER.",
	}
}

// QueuedWakesPhrase is what a person reads above a listing of the queue:
// how many rows wait, or that none do. The listing hands out nothing, and
// the phrase says so, because the drain it replaced for a person took what
// it showed.
func QueuedWakesPhrase(q QueuedWakes) Phrase {
	n := len(q.Wakes) + len(q.DomainWakes) + len(q.KeptWorkWakes)
	if n == 0 {
		return phraseNoNotices
	}
	return Phrase{
		Outcome:     fmt.Sprintf("%d notice(s) about the fleet are waiting, oldest first.", n),
		Consequence: "Listing them here took none of them: each stays waiting until the coordinator acts on it and acknowledges it.",
		Decision:    "Nothing to decide here; run assistant tasks to see how the work stands.",
	}
}

// The rows a listed notice reads as, one per wake kind internal/supervisor's
// table holds that can reach the queue, which is every kind but the one
// whose class always absorbs, and one for a kind this build has no row for. Each names what
// the notice says about the work in a person's words and never the kind.
var (
	phraseNoticeHold = Phrase{
		Outcome:     "A decision about this work is waiting on an answer.",
		Consequence: "The work does not go on past it until it is answered.",
		Decision:    "Run assistant watch to see the decision and the answer that closes it.",
	}
	phraseNoticeWorkerGone = Phrase{
		Outcome:     "This work's worker has ended, or could not be checked.",
		Consequence: "Nothing moves the work on until it is begun again or let go.",
		Decision:    "Run assistant tasks to see how the work stands.",
	}
	phraseNoticeMessage = Phrase{
		Outcome:     "This work's worker has not acknowledged a message sent to it.",
		Consequence: "It may not have read what it was told.",
		Decision:    "Run assistant tasks to see how the work stands.",
	}
	phraseNoticeDecisionRaised = Phrase{
		Outcome:     "This work's worker raised a decision.",
		Consequence: "It may be waiting on the answer.",
		Decision:    "Run assistant watch to see what it asks.",
	}
	phraseNoticeUnproven = Phrase{
		Outcome:     "This work changed with nothing showing that it is moving.",
		Consequence: "It may have finished, or stopped.",
		Decision:    "Run assistant tasks to see how the work stands.",
	}
	phraseNoticeIdle = Phrase{
		Outcome:     "This work's worker has shown the same screen with nothing showing that it is moving.",
		Consequence: "It may have finished, or stopped.",
		Decision:    "Run assistant tasks to see how the work stands.",
	}
	phraseNoticeOutsideWait = Phrase{
		Outcome:     "This work's worker said it was waiting on something outside the work, and that has gone on past the time set for it.",
		Consequence: "What it waits on may not clear by itself.",
		Decision:    "Run assistant tasks to see how the work stands.",
	}
	phraseNoticeCheckResult = Phrase{
		Outcome:     "A check result for this work arrived.",
		Consequence: "It may change what happens next.",
		Decision:    "Run assistant tasks to see how the work stands.",
	}
	phraseNoticeUnfamiliar = Phrase{
		Outcome:     "Something about this work needs a look, of a kind this build cannot describe.",
		Consequence: "Its structured form says what it is.",
		Decision:    "Repeat with --json to read it.",
	}
	phraseNoticeDomainCoordinatorGone = Phrase{
		Outcome:     "The coordinator of this work's domain has ended, or could not be checked.",
		Consequence: "Nothing in that domain is watched by it until it is begun again or the domain is taken back.",
		Decision:    "Run assistant tasks to see how the domain's work stands.",
	}
	phraseNoticeHandedUp = Phrase{
		Outcome:     "The coordinator of this work's domain handed a decision about it up to you.",
		Consequence: "It asked for something it is not allowed to do itself, and nothing was done.",
		Decision:    "Read what it asked with assistant --json tasks, and act on it or not.",
	}
)

// noticeRows is which row each wake kind reads as. A kind with no entry
// reads as phraseNoticeUnfamiliar, so a kind added to internal/supervisor's
// table without a row here is shown rather than dropped.
var noticeRows = map[supervisor.Kind]Phrase{
	supervisor.KindHoldOpened:            phraseNoticeHold,
	supervisor.KindTerminalGone:          phraseNoticeWorkerGone,
	supervisor.KindMessageUnacknowledged: phraseNoticeMessage,
	supervisor.KindDecisionEvent:         phraseNoticeDecisionRaised,
	supervisor.KindPlainEvent:            phraseNoticeUnproven,
	supervisor.KindTerminalUnchanged:     phraseNoticeIdle,
	supervisor.KindDeclaredWait:          phraseNoticeOutsideWait,
	supervisor.KindCheckResult:           phraseNoticeCheckResult,
	supervisor.KindSubordinateGone:       phraseNoticeDomainCoordinatorGone,
	supervisor.KindSubordinateEscalated:  phraseNoticeHandedUp,
	supervisor.KindKeptWork:              phraseNoticeKeptWork,
}

// QueuedWakePhrase is the row one listed notice reads as, chosen by its
// kind, which the row never names.
func QueuedWakePhrase(w QueuedWake) Phrase {
	if row, ok := noticeRows[supervisor.Kind(w.Wake.Kind)]; ok {
		return row
	}
	return phraseNoticeUnfamiliar
}

// QueuedWakeStanding is what a person reads after a listed notice: whether a
// drain has handed it to the coordinator. Either way it is still waiting,
// because the listing shows only rows nobody has acknowledged.
func QueuedWakeStanding(w QueuedWake) string {
	return standingOf(w.Wake.HandedOut())
}

// QueuedDomainWakePhrase is the row one listed notice about a domain reads
// as, chosen by its kind on QueuedWakePhrase's terms.
func QueuedDomainWakePhrase(w store.DomainWake) Phrase {
	if row, ok := noticeRows[supervisor.Kind(w.Kind)]; ok {
		return row
	}
	return phraseNoticeUnfamiliar
}

// QueuedDomainWakeStanding is QueuedWakeStanding for a notice about a
// domain.
func QueuedDomainWakeStanding(w store.DomainWake) string { return standingOf(w.HandedOut()) }

// QueuedKeptWorkWakePhrase is the row one listed notice about a kept work
// entry reads as, chosen by its kind on QueuedWakePhrase's terms.
func QueuedKeptWorkWakePhrase(w store.KeptWorkWake) Phrase {
	if row, ok := noticeRows[supervisor.Kind(w.Kind)]; ok {
		return row
	}
	return phraseNoticeUnfamiliar
}

// QueuedKeptWorkWakeStanding is QueuedWakeStanding for a notice about a
// kept work entry.
func QueuedKeptWorkWakeStanding(w store.KeptWorkWake) string { return standingOf(w.HandedOut()) }

// standingOf is the sentence after a listed notice, for any shape.
func standingOf(handedOut bool) string {
	if handedOut {
		return "The coordinator has been handed it and has not yet said it acted on it."
	}
	return "The coordinator has not been handed it yet."
}

// AcknowledgedPhrase is what a person reads once wakes were acknowledged:
// how many rows the batch closed. An empty batch is refused before it is
// answered, so every answer names at least one.
func AcknowledgedPhrase(a Acknowledged) Phrase {
	return Phrase{
		Outcome:     fmt.Sprintf("%d notice(s) about the fleet are recorded as handled.", len(a.Wakes)+len(a.DomainWakes)+len(a.KeptWorkWakes)),
		Consequence: "They are not handed out again.",
		Decision:    "Nothing to decide.",
	}
}

// ArmPhrase is what a person reads about arm verification's answer. A loop
// that came up while the verification watched and one that was already live
// read alike, because the decision is the same; a verification that found
// no service holding the home reads as a service to start, and every other
// failure as one to restart, which is the grouping the pull guard's rows
// make. An outcome outside internal/supervisor's set reads as not shown to
// be watching, never as watched.
func ArmPhrase(a supervisor.Arm) Phrase {
	switch {
	case a.Outcome == supervisor.ArmStarted, a.Outcome == supervisor.ArmAttached:
		return phraseFleetWatched
	case a.Outcome == supervisor.ArmFailed && a.Reason == supervisor.ArmServiceDown:
		return phraseFleetNoService
	}
	return phraseFleetNotWatched
}

// coordinatorRows is every row this file holds, as TablePhrases lists them,
// with the counted rows' placeholder filled in.
func coordinatorRows() []Phrase {
	return []Phrase{
		phraseTaskCreated, phraseTaskCreatedBehind, phraseTaskPromoted, phraseTaskPromotedUnsent, phraseTaskRelaunched, phraseTaskRelayed,
		phraseNoNotices, WakesPhrase(Wakes{Wakes: make([]store.Wake, 1)}), AcknowledgedPhrase(Acknowledged{Wakes: []int64{1}}),
		QueuedWakesPhrase(QueuedWakes{Wakes: make([]QueuedWake, 1)}),
		phraseNoticeHold, phraseNoticeWorkerGone, phraseNoticeMessage, phraseNoticeDecisionRaised,
		phraseNoticeUnproven, phraseNoticeIdle, phraseNoticeOutsideWait, phraseNoticeCheckResult, phraseNoticeUnfamiliar,
		phraseNoticeDomainCoordinatorGone, phraseNoticeHandedUp,
		phraseFleetWatched, phraseFleetNoService, phraseFleetNotWatched, phraseWorkerRecorded,
		phrasePeekAttachable, phrasePeekUnattached, phraseDomainDelegated, phraseDelegationEnded,
		phraseDomainRetried, phraseDelegationEndedByAnswer,
	}
}

// phraseWorkerRecorded is what a person reads of a worker's task.event
// answer, the one answer the worker's route has. It names neither the kind
// nor the task's state, each a word the derived forbidden set keeps from a
// person; the structured rendering carries the entry.
var phraseWorkerRecorded = Phrase{
	Outcome:     "What the worker said about its work is recorded in the work's history.",
	Consequence: "The coordinator reads it from there.",
	Decision:    "Nothing to decide.",
}

// AppendedPhrase is what a person reads about a task.event answer. An entry
// the service refused is a refusal and never an answer, so there is one
// row.
func AppendedPhrase(Appended) Phrase { return phraseWorkerRecorded }
