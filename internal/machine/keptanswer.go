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
	// Confirm is, with a discard answer, the digest a discard shown
	// beforehand carried (KeptWorkLoss.Digest): without it a discard shows
	// what would be lost and removes nothing, and with it the copy is
	// removed only when it still holds exactly what that digest was shown
	// for. It is refused with any other answer.
	Confirm string `json:"confirm,omitempty"`
}

// KeptWorkLoss is what discarding a kept copy would lose, as the program
// read it: the commits the default branch lacks, every path whose content
// nothing else holds, the three indicators git reports, the commit HEAD
// stands on, and the digest of all of it, which a discard is confirmed
// with.
type KeptWorkLoss struct {
	// Head is the commit the copy's HEAD stands on.
	Head string `json:"head"`
	// Lacks is the commits the copy holds that the default branch lacks.
	Lacks []string `json:"lacks"`
	// Files is every path the removal destroys whose content nothing else
	// holds, decided from what the copy holds and not from what git
	// reports, a file git ignores among them: each with why it is lost,
	// one of changed (a file HEAD holds other content at), new (nothing in
	// HEAD at that path), staged (index content neither HEAD nor the
	// working tree holds) and repository (a file of the repository
	// directory the removal deletes that differs from what git writes into
	// a new repository, its path relative to that directory). A path
	// ending in a slash is a directory lost whole.
	Files []KeptWorkLostPath `json:"files"`
	// Dirty, Untracked and Stash say whether git reports the copy holding
	// a tracked change, a file no commit records that git does not ignore,
	// and a stash; Files names every path these speak of, and the ignored
	// ones they do not.
	Dirty     bool `json:"dirty"`
	Untracked bool `json:"untracked"`
	Stash     bool `json:"stash"`
	// Digest confirms exactly this: a discard given it removes the copy
	// only while the copy still holds what it digests.
	Digest string `json:"digest"`
}

// KeptWorkLostPath is one path a discard destroys whose content nothing
// else holds, and why (KeptWorkLoss.Files).
type KeptWorkLostPath struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
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
	// KeptWorkDiscardShown is a discard given without Confirm: Loss says
	// exactly what would be lost and carries the digest to confirm with,
	// and nothing was recorded or removed.
	KeptWorkDiscardShown KeptWorkEffect = "discard-shown"
	// KeptWorkDiscarded is a discard answer recorded, with what it was
	// shown, and carried out: the copy is gone, and Loss is what went with
	// it.
	KeptWorkDiscarded KeptWorkEffect = "discarded"
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
	// Loss is what a discard would lose or lost, for the two discard
	// effects, and absent for every other.
	Loss *KeptWorkLoss `json:"loss,omitempty"`
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

// phraseKeptWorkDiscardShown is the row a discard shown and not confirmed
// reads as. What would be lost and the digest are the structured
// rendering's, which the row points at (P13).
var phraseKeptWorkDiscardShown = Phrase{
	Outcome:     "Nothing was removed: this is what discarding that local copy would lose.",
	Consequence: "The copy stays, with everything it holds, until you confirm.",
	Decision:    "Read exactly what would be lost, and the digest of it, with --json; to remove the copy, give the same answer again with --confirm and that digest, which is taken only while the copy still holds exactly that.",
}

// phraseKeptWorkDiscarded is the row a discard carried out reads as.
var phraseKeptWorkDiscarded = Phrase{
	Outcome:     "That local copy was removed, with everything in it that was nowhere else.",
	Consequence: "What was in it is gone, and new work in the same project no longer waits on it.",
	Decision:    "Nothing to decide.",
}

// PhraseDiscardNeedsTerminal is the row for a discard confirmation given
// where no person is at a terminal: under --json, or from a process whose
// standard input and standard error are not one terminal. The command
// surface refuses it before anything is asked of the service, which is
// the owner's decision on #411 for keeping discard the person's: a headless
// agent cannot confirm one. The copy and the entry are as they were.
var PhraseDiscardNeedsTerminal = Phrase{
	Outcome:     "Nothing was removed, because a discard is confirmed only by you at a terminal, and this was asked from somewhere else.",
	Consequence: "The copy stays, with everything it holds, and nothing was recorded.",
	Decision:    "Run the same command yourself at a terminal, without --json, to remove it.",
}

// KeptWorkAnsweredPhrase is the line a person reads for an answered kept
// work entry: one row per effect.
func KeptWorkAnsweredPhrase(a KeptWorkAnswered) Phrase {
	switch a.Effect {
	case KeptWorkCarriedOn:
		return phraseKeptWorkCarriedOn
	case KeptWorkDiscardShown:
		return phraseKeptWorkDiscardShown
	case KeptWorkDiscarded:
		return phraseKeptWorkDiscarded
	}
	return phraseKeptWorkLanded
}

// keptAnswerRows is this file's rows for TablePhrases.
func keptAnswerRows() []Phrase {
	return []Phrase{phraseKeptWorkLanded, phraseKeptWorkCarriedOn, phraseKeptWorkDiscardShown, phraseKeptWorkDiscarded, PhraseDiscardNeedsTerminal}
}
