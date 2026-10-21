package machine

import "github.com/dayamjz/assistant/internal/store"

// KeptWorkAnswerRequest answers one kept work entry, PRD section 9's hold
// answer given a kept work entry: Hold is the entry's key
// (store.KeptWork.Key), and exactly one of Task and Run names the entry's
// owner, the task whose copy it is or, for an entry a person's push left,
// the run. Answer is one of store.KeptAnswers.
//
// It carries no field saying who answered, on RespondRequest's terms: who
// answered is derived from the surface by internal/ipc's
// Request.HoldResolver and never read from a request.
type KeptWorkAnswerRequest struct {
	// Task names the task whose copy the entry is, and is empty when Run
	// names the run.
	Task string `json:"task,omitempty"`
	// Run names the run whose copy the entry is, and is empty when Task
	// names the task.
	Run string `json:"run,omitempty"`
	// Hold is the entry's key, kept-work/ and its identifier.
	Hold string `json:"hold"`
	// Answer is land, carry-on or discard.
	Answer string `json:"answer"`
}

// KeptWorkEffect is what an answer to a kept work entry did. The set is
// closed.
type KeptWorkEffect string

const (
	// KeptWorkLanded is a land answer recorded and carried out: the copy's
	// commit was brought into the gate and a run of the entry's branch was
	// started at it, as a push to the gate starts one.
	KeptWorkLanded KeptWorkEffect = "landed"
	// KeptWorkCarriedOn is a carry-on answer recorded and carried out: a new
	// task of the same project, shape and branch was created through
	// intake, the kept copy is its copy, and its instructions carry a note
	// saying what is already there. Its worker starts on the dispatch that
	// follows.
	KeptWorkCarriedOn KeptWorkEffect = "carried-on"
)

// KeptWorkAnswered is the answer to a KeptWorkAnswerRequest: the entry as
// it stands after the answer, with the answer recorded on it, what the
// answer did, and for a landing the run it started.
type KeptWorkAnswered struct {
	// Entry is the entry, answered.
	Entry store.KeptWork `json:"entry"`
	// Effect is what the answer did.
	Effect KeptWorkEffect `json:"effect"`
	// Run is the run a landing started, empty for every other effect.
	Run string `json:"run,omitempty"`
	// Task is the task a carry on created, empty for every other effect.
	Task string `json:"task,omitempty"`
}

// phraseKeptWorkLanded is the row a landed answer reads as.
var phraseKeptWorkLanded = Phrase{
	Outcome:     "The work in that local copy was taken into validation, as if you had sent it yourself.",
	Consequence: "The copy stays until its work is somewhere safe, and new work in the same project no longer waits on it.",
	Decision:    "Follow the validation with assistant status; it asks you only what it would ask of work you sent.",
}

// phraseKeptWorkCarriedOn is the row a carried-on answer reads as.
var phraseKeptWorkCarriedOn = Phrase{
	Outcome:     "New work was begun in that local copy, picking up what was already there.",
	Consequence: "The work it continues stays as it finished, and new work in the same project no longer waits on that copy.",
	Decision:    "Follow the new work with assistant tasks; it asks you what any work would.",
}

// KeptWorkAnsweredPhrase is the line a person reads for an answered kept
// work entry: the carried-on row for a carry on and the landed row for a
// landing.
func KeptWorkAnsweredPhrase(a KeptWorkAnswered) Phrase {
	if a.Effect == KeptWorkCarriedOn {
		return phraseKeptWorkCarriedOn
	}
	return phraseKeptWorkLanded
}

// keptAnswerRows is this file's rows for TablePhrases.
func keptAnswerRows() []Phrase { return []Phrase{phraseKeptWorkLanded, phraseKeptWorkCarriedOn} }
