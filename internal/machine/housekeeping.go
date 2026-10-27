package machine

import (
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// The rows a person reads about housekeeping, PRD section 6's housekeeping:
// the notice a housekeeping wake reads as in a listing of the queue.
// Section 6 has nothing new reach a person: the due wake is the
// coordinator's, so its row tells the coordinator what to do, and a pass's
// own holds read as every hold does. Which repository a notice is about and
// what it names are the structured rendering's.
var (
	phraseNoticeHousekeeping = Phrase{
		Outcome:     "A repository's agent context is due a housekeeping pass, or its open pass has gone on for a whole interval.",
		Consequence: "No pass starts by itself: one starts only when the coordinator creates it with its notes.",
		Decision:    "Read what the notice names with --json, then create the pass with assistant coordinator create --housekeeping, or tell the person about the open one.",
	}
)

// QueuedHousekeepingWakePhrase is the row one listed notice about a
// repository's housekeeping reads as, chosen by its kind on
// QueuedWakePhrase's terms.
func QueuedHousekeepingWakePhrase(w store.HousekeepingWake) Phrase {
	if row, ok := noticeRows[supervisor.Kind(w.Kind)]; ok {
		return row
	}
	return phraseNoticeUnfamiliar
}

// QueuedHousekeepingWakeStanding is QueuedWakeStanding for a notice about a
// repository's housekeeping.
func QueuedHousekeepingWakeStanding(w store.HousekeepingWake) string {
	return standingOf(w.HandedOut())
}

// housekeepingRows is this file's rows for TablePhrases.
func housekeepingRows() []Phrase {
	return []Phrase{phraseNoticeHousekeeping}
}
