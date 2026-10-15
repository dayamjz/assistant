package machine

import (
	"fmt"

	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// The coordinator client's rows: what a person reads of the answers to the
// coordinator's methods that the steering rows in phrases.go do not already
// phrase. A message and a stop read as MessagePhrase and StopPhrase; a
// created task, a promotion, a relaunch, a drain of the wake queue, an
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
		Consequence: "Nothing it had already done there was moved or lost.",
		Decision:    "Nothing to decide now; run assistant tasks to see how it goes.",
	}
	phraseTaskRelaunched = Phrase{
		Outcome:     "The work was begun again in its own local copy, by a new worker.",
		Consequence: "This answer comes before the worker does, so whether it began shows in the listing.",
		Decision:    "Nothing to decide now; run assistant tasks to see how it goes.",
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
	phraseFleetNotWatched = Phrase{
		Outcome:     "The service is up, but nothing in it could be shown to be watching the fleet.",
		Consequence: "A worker that stops or needs a decision may go unnoticed.",
		Decision:    "Run assistant service restart.",
	}
)

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

// TaskPromotedPhrase is what a person reads about a task.promote answer. A
// promotion that did not complete is a refusal and never an answer, so
// there is one row.
func TaskPromotedPhrase(TaskPromoted) Phrase { return phraseTaskPromoted }

// TaskRelaunchedPhrase is what a person reads about a task.relaunch answer.
// A relaunch the records refused is a refusal and never an answer, so there
// is one row.
func TaskRelaunchedPhrase(TaskRelaunched) Phrase { return phraseTaskRelaunched }

// WakesPhrase is what a person reads about a drain of the wake queue: how
// many rows are queued, or that none are. Which work each row is about and
// what kind of notice it is are the structured rendering's, because a
// kind is a word the derived forbidden set keeps from a person.
func WakesPhrase(w Wakes) Phrase {
	if len(w.Wakes) == 0 {
		return phraseNoNotices
	}
	return Phrase{
		Outcome:     fmt.Sprintf("%d notice(s) about the fleet are waiting to be handled, oldest first.", len(w.Wakes)),
		Consequence: "Each stays, and is listed again on every read, until it is acknowledged.",
		Decision:    "Repeat with --json to read what each is about, then acknowledge the ones you handled.",
	}
}

// AcknowledgedPhrase is what a person reads once wakes were acknowledged:
// how many rows the batch closed. An empty batch is refused before it is
// answered, so every answer names at least one.
func AcknowledgedPhrase(a Acknowledged) Phrase {
	return Phrase{
		Outcome:     fmt.Sprintf("%d notice(s) about the fleet are recorded as handled.", len(a.Wakes)),
		Consequence: "They are not listed again.",
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
		phraseTaskCreated, phraseTaskCreatedBehind, phraseTaskPromoted, phraseTaskRelaunched,
		phraseNoNotices, WakesPhrase(Wakes{Wakes: make([]store.Wake, 1)}), AcknowledgedPhrase(Acknowledged{Wakes: []int64{1}}),
		phraseFleetWatched, phraseFleetNoService, phraseFleetNotWatched,
	}
}
