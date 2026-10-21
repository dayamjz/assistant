package machine

import "github.com/dayamjz/assistant/internal/store"

// The rows a person reads about kept work, PRD section 6's "kept work" and
// principle P15's "kept work is never silent": the catch-up read's part
// that lists every unanswered entry, the line for one entry, which status
// prints too, and the notice a kept-work wake reads as. The entry itself,
// its copy's path and the commits it holds, is the structured rendering's,
// which each line points at.
//
// Answering an entry is #411's second part and is not in this build, so no
// row offers an answer this build would refuse; the rows say the copy stays
// until it is decided, which is what the build does.
var (
	phraseKeptWork = Phrase{
		Outcome:     "This work was kept in its local copy, because it is not yet on the shared branch or anywhere else safe.",
		Consequence: "Nothing removes it, and it stays until you decide what becomes of it.",
		Decision:    "Read what it holds with --json; landing it, carrying it on or discarding it is not yet something this build does for you.",
	}
	phraseKeptWorkPart = Phrase{
		Outcome:     "This work was kept in local copies because it is not yet anywhere safe, oldest first.",
		Consequence: "Each copy stays where it is until you decide what becomes of it.",
		Decision:    "Read what each one holds with --json.",
	}
	phraseNothingKept = Phrase{
		Outcome:     "No work is kept waiting for you to decide what becomes of it.",
		Consequence: "No local copy is waiting on a decision of yours.",
		Decision:    "Nothing to decide.",
	}
	phraseNoticeKeptWork = Phrase{
		Outcome:     "This work's local copy was kept, because what it holds is not yet anywhere safe.",
		Consequence: "It stays until a person decides what becomes of it.",
		Decision:    "Tell the person, and read what it holds with assistant --json status.",
	}
)

// KeptWorkPhrase is the line for one unanswered kept work entry, in the
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
