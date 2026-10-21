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
// nothing else removes it; while an entry is open, no new task in its
// project starts (internal/store's keptworkhold.go), and a push still runs.
// This build takes one of the three answers, land (keptanswer.go), so a
// row that offers an answer offers that one, names how it is given, and
// says carrying on and discarding are not yet taken.
var (
	phraseKeptWork = Phrase{
		Outcome:     "This work was kept in its local copy, because it is not yet on the shared branch or anywhere else safe.",
		Consequence: "The copy stays unless its work later turns up somewhere safe, and no new task in the same project starts until it is decided; your own pushes still go ahead.",
		Decision:    "Read what it holds with --json; to have it validated, answer land with assistant, --task and the work's identifier or --run= and the validation's, --answer land and --hold with the entry's key, which assistant --json status shows; carrying it on and discarding it are not yet something this build does.",
	}
	phraseKeptWorkPart = Phrase{
		Outcome:     "This work was kept in local copies because it is not yet anywhere safe, oldest first.",
		Consequence: "Each copy stays unless its work later turns up somewhere safe, and no new task in the same project starts until it is decided; your own pushes still go ahead.",
		Decision:    "Read what each one holds with --json.",
	}
	phraseNothingKept = Phrase{
		Outcome:     "No work is kept waiting for you to decide what becomes of it.",
		Consequence: "No local copy is waiting on a decision of yours.",
		Decision:    "Nothing to decide.",
	}
	phraseRunKeptWork = Phrase{
		Outcome:     "Work kept in a local copy of this project is waiting on your decision.",
		Consequence: "This validation goes ahead regardless, but no new task in the project starts until that copy's work is decided.",
		Decision:    "Read what is kept, and how to decide it, with assistant status.",
	}
	phraseNoticeKeptWork = Phrase{
		Outcome:     "This work's local copy was kept, because what it holds is not yet anywhere safe.",
		Consequence: "It stays unless its work later turns up somewhere safe, and no new task in its project starts until it is decided; the person's own pushes still go ahead.",
		Decision:    "Tell the person, and read what it holds with assistant --json status; it is answered land with --answer land and --hold naming the entry's key.",
	}
)

// KeptWorkPhrase is the line for one open kept work entry, in the
// catch-up read and in status alike.
func KeptWorkPhrase(store.KeptWork) Phrase { return phraseKeptWork }

// RunKeptWorkPhrase is the line a run's answer carries while its project
// holds open kept work, Run.KeptWork, and false when it holds none.
func RunKeptWorkPhrase(r Run) (Phrase, bool) {
	if len(r.KeptWork) == 0 {
		return Phrase{}, false
	}
	return phraseRunKeptWork, true
}

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
	return []Phrase{phraseKeptWork, phraseKeptWorkPart, phraseNothingKept, phraseRunKeptWork, phraseNoticeKeptWork}
}
