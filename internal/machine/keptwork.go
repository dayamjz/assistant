package machine

import "github.com/dayamjz/assistant/internal/store"

// The rows a person reads about kept work, PRD section 6's "kept work" and
// principle P15's "kept work is never silent": the catch-up read's part
// that lists every open entry, the line for one entry, which status
// prints too, and the notice a kept-work wake reads as. The entry itself,
// its copy's path and the commits it holds, is the structured rendering's,
// which each line points at.
//
// What the rows claim is what this build does: the copy stays unless the
// cleanup decision later removes it on a proof, which closes the entry, and
// nothing else removes it. Answering an entry and holding new task work in
// its project until it is answered are #411's second part and are not in
// this build, so no row offers an answer this build would refuse and every
// row that speaks of new work says it still starts.
var (
	phraseKeptWork = Phrase{
		Outcome:     "This work was kept in its local copy, because it is not yet on the shared branch or anywhere else safe.",
		Consequence: "The copy stays unless its work later turns up somewhere safe, and new work in the same project still starts, because this build does not yet hold it off until you decide.",
		Decision:    "Read what it holds with --json; landing it, carrying it on or discarding it is not yet something this build does for you.",
	}
	phraseKeptWorkPart = Phrase{
		Outcome:     "This work was kept in local copies because it is not yet anywhere safe, oldest first.",
		Consequence: "Each copy stays unless its work later turns up somewhere safe, and new work in the same project still starts, because this build does not yet hold it off until you decide.",
		Decision:    "Read what each one holds with --json.",
	}
	phraseNothingKept = Phrase{
		Outcome:     "No work is kept waiting for you to decide what becomes of it.",
		Consequence: "No local copy is waiting on a decision of yours.",
		Decision:    "Nothing to decide.",
	}
	phraseNoticeKeptWork = Phrase{
		Outcome:     "This work's local copy was kept, because what it holds is not yet anywhere safe.",
		Consequence: "It stays unless its work later turns up somewhere safe, and new work in its project still starts, because this build does not yet hold it off until you decide.",
		Decision:    "Tell the person, and read what it holds with assistant --json status.",
	}
)

// KeptWorkPhrase is the line for one open kept work entry, in the
// catch-up read and in status alike.
func KeptWorkPhrase(store.KeptWork) Phrase { return phraseKeptWork }

// KeptWorkPartPhrase is the row at the head of the catch-up read's kept work
// part, or that part's empty row when it lists nothing.
func KeptWorkPartPhrase(c CatchUp) Phrase {
	if len(c.Kept) == 0 {
		return phraseNothingKept
	}
	return phraseKeptWorkPart
}

// keptWorkRows is this file's rows for TablePhrases.
func keptWorkRows() []Phrase {
	return []Phrase{phraseKeptWork, phraseKeptWorkPart, phraseNothingKept, phraseNoticeKeptWork}
}
