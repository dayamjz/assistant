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
	// Answer is land, carry-on, discard or remove-anyway.
	Answer string `json:"answer"`
	// Confirm is, with a discard answer, the digest a discard shown
	// beforehand carried (KeptWorkLoss.Digest): without it a discard shows
	// what would be lost and removes nothing. With it, a task's clone or a
	// run's worktree is removed on that confirmation from a terminal's
	// foreground. It is refused with any other answer.
	Confirm string `json:"confirm,omitempty"`
	// Force is, with a remove-anyway answer, the stronger confirmation
	// (PRD section 6): without it remove-anyway shows why a discard
	// refuses and records nothing; with it, together with the task or run
	// name that owns the entry, the entry is released and the copy stays,
	// admitted only from a terminal's foreground. It is refused with any
	// other answer.
	Force bool `json:"force,omitempty"`
}

// KeptWorkLoss is what discarding a kept copy would lose, as the program
// read it: the commits the default branch lacks, every path whose content
// nothing else holds, the three indicators git reports, the commit HEAD
// stands on, and the digest of all of it, which a discard would be
// confirmed with.
type KeptWorkLoss struct {
	// Head is the commit the copy's HEAD stands on.
	Head string `json:"head"`
	// Lacks is the commits the removal destroys a name of that nothing held
	// reaches, held being the default branch and the upstream alone: every
	// commit HEAD, a reference, a root reference such
	// as ORIG_HEAD or a reflog entry of the copy, by the value it moved to
	// or the one it moved from, reaches that neither the default branch
	// nor what the upstream holds now reaches: anything the copy names
	// that the upstream advertises, and the commit the upstream's branch
	// of the same name as a branch or remote-tracking reference of the
	// copy's stands on. A commit reset away, a stash under the newest, and
	// one only a remote-tracking reference names that the upstream no
	// longer reaches are among them. For a run's worktree the names counted
	// are only what its removal destroys, its HEAD, its own references and
	// root references and their reflogs, and the stash entries it made, so
	// a commit only the gate's own references or their reflogs, or another
	// worktree's stash entries, record is not listed. Those stand after the
	// removal but hold nothing, as the default branch and the upstream do:
	// a commit one of the worktree's own names reaches is listed even when
	// the gate records it too, and a land of HEAD that does not carry it is
	// refused.
	Lacks []string `json:"lacks"`
	// Objects is every object a name the removal destroys points at that
	// is not a commit, an annotated tag, a tree or a blob, with its type
	// and the names that point at it, unless the upstream advertises it
	// now, and otherwise listed whatever else holds it.
	Objects []KeptWorkLostObject `json:"objects"`
	// Files is every path the removal destroys whose content or deletion
	// nothing else holds, decided from what the copy holds and not from
	// what git reports, a file git ignores among them: each with why it is
	// lost, one of changed (a file HEAD holds other content at), new
	// (nothing in HEAD at that path), staged (index content neither HEAD
	// nor the working tree holds), deleted (a path HEAD holds that the
	// working tree or the index no longer does, staged or not) and
	// repository (a file of the repository
	// directory the removal deletes that differs from what git writes into
	// a new repository, its path relative to that directory). A path
	// ending in a slash is a directory lost whole.
	Files []KeptWorkLostPath `json:"files"`
	// Dirty, Untracked and Stash say whether git reports the copy holding
	// a tracked change, a file no commit records that git does not ignore,
	// and a stash, for a run's worktree, whose stash is the gate's, only
	// an entry it made; Files names every path these speak of, and the
	// ignored ones they do not.
	Dirty     bool `json:"dirty"`
	Untracked bool `json:"untracked"`
	Stash     bool `json:"stash"`
	// Digest seals exactly this and nothing else, each file by what it
	// holds: the value a confirmation names. A task's clone or a run's
	// worktree is removed when that digest still seals the copy.
	Digest string `json:"digest"`
}

// KeptWorkLostObject is one object a discard destroys a name of that is
// not a commit, and every name of it the discard destroys: HEAD, a
// reference's full name, a root reference's name such as AUTO_MERGE, or a
// reflog entry as ref@{n}; none for an object only a reflog entry's
// previous value records
// (KeptWorkLoss.Objects).
//
// A name is the bytes git stores, which need not be UTF-8, and JSON carries
// a string with every byte that is not UTF-8 replaced, so two names that
// differ only there arrive alike. NameBytes is therefore each of Names as
// its exact bytes, in the same order, base64 in JSON, and Names its display
// form: exact in a Go value, and as JSON carries it in an encoded one.
// LostObject builds both from one name list, so they cannot disagree.
type KeptWorkLostObject struct {
	Object    string   `json:"object"`
	Type      string   `json:"type"`
	Names     []string `json:"names"`
	NameBytes [][]byte `json:"name_bytes"`
}

// KeptWorkLostPath is one path a discard destroys whose content or
// deletion nothing else holds, and why (KeptWorkLoss.Files). PathBytes is
// Path's exact bytes, base64 in JSON, on KeptWorkLostObject's terms: a
// path is bytes too, and JSON carries Path with every byte that is not
// UTF-8 replaced.
type KeptWorkLostPath struct {
	Kind      string `json:"kind"`
	Path      string `json:"path"`
	PathBytes []byte `json:"path_bytes"`
}

// LostObject is a KeptWorkLostObject of the object, its type and its
// names, NameBytes filled from the same names.
func LostObject(object, typ string, names []string) KeptWorkLostObject {
	raw := make([][]byte, len(names))
	for i, n := range names {
		raw[i] = []byte(n)
	}
	return KeptWorkLostObject{Object: object, Type: typ, Names: names, NameBytes: raw}
}

// LostPath is a KeptWorkLostPath of the kind and the path, PathBytes filled
// from the same path.
func LostPath(kind, path string) KeptWorkLostPath {
	return KeptWorkLostPath{Kind: kind, Path: path, PathBytes: []byte(path)}
}

// ExactNames is the object's names as their exact bytes say, read from
// NameBytes when it holds one per name, as every value LostObject builds
// does, before and after JSON, and otherwise from Names, which is exact in
// a Go value no JSON carried.
func (o KeptWorkLostObject) ExactNames() []string {
	if len(o.NameBytes) != len(o.Names) {
		return o.Names
	}
	out := make([]string, len(o.NameBytes))
	for i, b := range o.NameBytes {
		out[i] = string(b)
	}
	return out
}

// ExactPath is the path as its exact bytes say, read from PathBytes when it
// is there, as in every value LostPath builds, before and after JSON, and
// otherwise from Path, which is exact in a Go value no JSON carried.
func (p KeptWorkLostPath) ExactPath() string {
	if p.PathBytes == nil {
		return p.Path
	}
	return string(p.PathBytes)
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
	// exactly what would be lost, with its digest, and nothing was recorded
	// or removed.
	KeptWorkDiscardShown KeptWorkEffect = "discard-shown"
	// KeptWorkDiscarded is a discard given with Confirm: the copy was
	// removed and the entry answered discard.
	KeptWorkDiscarded KeptWorkEffect = "discarded"
	// KeptWorkRemoveAnywayShown is a remove-anyway given without Force:
	// why a discard refuses and what of the loss could be read, with
	// nothing recorded or released.
	KeptWorkRemoveAnywayShown KeptWorkEffect = "remove-anyway-shown"
	// KeptWorkRemovedAnyway is a remove-anyway given with Force: the
	// entry was released and the copy left standing.
	KeptWorkRemovedAnyway KeptWorkEffect = "removed-anyway"
)

// KeptWorkAnswered is the answer to a KeptWorkAnswerRequest: the entry as
// it stands after the answer, what the answer did, and for a landing the
// run it started. A land, carry on, confirmed discard or confirmed
// remove-anyway records its answer on the entry; a discard or
// remove-anyway shown records nothing, so its entry is as it was, still
// open.
type KeptWorkAnswered struct {
	// Entry is the entry as the answer left it: answered for a land, a
	// carry on, a confirmed discard or a confirmed remove-anyway, and
	// unchanged and open for a discard or remove-anyway shown.
	Entry store.KeptWork `json:"entry"`
	// Effect is what the answer did.
	Effect KeptWorkEffect `json:"effect"`
	// Run is the run a landing started, empty for every other effect.
	Run string `json:"run,omitempty"`
	// Task is the task a carry on created, empty for every other effect.
	Task string `json:"task,omitempty"`
	// Loss is what a discard would lose, for a discard shown, and for a
	// remove-anyway shown what of the loss could be read (possibly
	// incomplete), absent for every other effect.
	Loss *KeptWorkLoss `json:"loss,omitempty"`
	// Refusal is why a discard refuses the copy, for a remove-anyway
	// shown, and absent for every other effect.
	Refusal string `json:"refusal,omitempty"`
}

// phraseKeptWorkLanded is the row a landed answer reads as.
var phraseKeptWorkLanded = Phrase{
	Outcome:     "The work in that local copy was taken into validation, as if you had sent it yourself.",
	Consequence: "The copy stays until its work is somewhere safe, and new work that would start where it stood no longer waits on it.",
	Decision:    "Follow the validation with assistant status; it asks you only what it would ask of work you sent.",
}

// phraseKeptWorkCarriedOn is the row a carried-on answer reads as.
var phraseKeptWorkCarriedOn = Phrase{
	Outcome:     "New work was begun in that local copy, picking up what was already there.",
	Consequence: "The work it continues stays as it finished, and new work that would start where that copy stood no longer waits on it.",
	Decision:    "Follow the new work with assistant tasks; it asks you what any work would.",
}

// phraseKeptWorkDiscardShown is the row a discard shown reads as. What
// would be lost and the digest follow it, as internal/cli's renderer
// prints them: each commit, object and file of KeptWorkLoss on a line of
// its own after a label, as machine text a person copies exactly, and the
// digest last, or PhraseKeptWorkLosesNothing when nothing is listed.
var phraseKeptWorkDiscardShown = Phrase{
	Outcome:     "Nothing was removed yet: discarding that local copy would lose what is listed below, and the digest under it seals exactly that list.",
	Consequence: "The copy stays, with everything it holds, until you confirm with that digest.",
	Decision:    "Repeat the discard with --confirm and the digest below, at a terminal in the foreground and without --json, to remove the copy; or answer land or carry-on instead.",
}

// phraseKeptWorkDiscarded is the row a confirmed discard reads as.
var phraseKeptWorkDiscarded = Phrase{
	Outcome:     "That local copy was removed on your confirmed discard.",
	Consequence: "What it contained is gone, and new work that would start where it stood is no longer blocked by it.",
	Decision:    "Nothing further to decide about that copy.",
}

// phraseKeptWorkRemoveAnywayShown is the row a remove-anyway shown reads as.
var phraseKeptWorkRemoveAnywayShown = Phrase{
	Outcome:     "Nothing was released yet: a discard of that local copy is refused for the reason below. Where any of that loss could be read, it follows as Incomplete.",
	Consequence: "The copy stays, with everything it holds, and new work that would start where it stood still waits on it.",
	Decision:    "Repeat the remove-anyway with --force at a terminal in the foreground and without --json, naming the same task or run, to release the entry and keep the copy; or answer land or carry-on when that is what you want instead.",
}

// phraseKeptWorkRemovedAnyway is the row a confirmed remove-anyway reads as.
var phraseKeptWorkRemovedAnyway = Phrase{
	Outcome:     "That local copy's hold was released on your confirmed remove-anyway; the copy itself was left standing.",
	Consequence: "New work that would start where that copy stood is no longer blocked by it; cleanup may still remove the copy later on a proof.",
	Decision:    "Nothing further to decide about that hold.",
}

// PhraseKeptWorkLosesNothing is the row a discard shown prints in place
// of a list when KeptWorkLoss names no commit, object or file, before the
// digest, which still seals that empty list.
var PhraseKeptWorkLosesNothing = Phrase{
	Outcome:     "Discarding that local copy would lose nothing that the default branch or the upstream does not hold.",
	Consequence: "Nothing is listed, and the digest under this seals that.",
	Decision:    "Nothing to decide about what would be lost.",
}

// PhraseDiscardRemovalDeferred was the row for a discard confirmation of a
// run's worktree while that removal was deferred. Worktree confirmation
// removes the copy now; the row remains in the phrase table so a sentence
// that named it still resolves, and is not offered for new refusals.
var PhraseDiscardRemovalDeferred = Phrase{
	Outcome:     "Nothing was removed: this row named a run's worktree confirmation while that removal was deferred.",
	Consequence: "The copy stayed, with everything it contained, and new work that would start where it stood stayed blocked by it.",
	Decision:    "This refusal is no longer produced: a confirmed discard removes a run's worktree the way it removes a task's clone.",
}

// PhraseDiscardNeedsTerminal is the row for a discard confirmation given
// from somewhere other than the foreground of a terminal: under --json or
// off a terminal, where the command line refuses it before the service is
// asked, and from a peer the service does not place in a terminal's
// foreground (PRD section 6, "Removing a discarded copy"). The copy and its
// entry are as they were.
var PhraseDiscardNeedsTerminal = Phrase{
	Outcome:     "Nothing was removed, because a discard is confirmed only at a terminal, in the foreground, and this was asked from somewhere else.",
	Consequence: "The copy stays, with everything it holds, and nothing was recorded.",
	Decision:    "Give a confirmation yourself at a terminal, in the foreground and without --json.",
}

// PhraseRemoveAnywayNeedsTerminal is the row for a remove-anyway
// confirmation given from somewhere other than a terminal's foreground,
// on PhraseDiscardNeedsTerminal's terms for remove-anyway (PRD section 6).
var PhraseRemoveAnywayNeedsTerminal = Phrase{
	Outcome:     "Nothing was released, because a remove-anyway is confirmed only at a terminal, in the foreground, and this was asked from somewhere else.",
	Consequence: "The copy stays, with everything it holds, and the entry was not released.",
	Decision:    "Give a confirmation yourself at a terminal, in the foreground and without --json, with --force.",
}

// The two carry-on slot refusals below are read by a person after the
// service let go of every lock the refusal was decided under, so each
// clause states only what no write after that moment can make false: what
// this answer did, and what the service was doing when the answer came to
// carry the work on, in the past tense. Neither says whether the entry is
// open (another answer holding the slot may close it), that the answer
// recorded nothing (the answer that recorded the claim can be the one
// refused, when another answer carries that claim out first), nor what the
// service can do now or later (the holder lets the slot go, and a retry
// answered on a hold can free a slot a dispatch kept). The person reads
// what stands instead (#731).

// PhraseCarryOnSlotBusy is the row for a carry-on answer refused because
// another call of this service held the new task's slot when the answer
// asked for it (claiming[id]): another carry-on of the same claim, a
// dispatch pass reading the hold, a relaunch or a steer. The row says the
// answer ended there and why, says it cannot tell whether the entry is
// open, and asks the person to read what stands and answer again only if
// the entry is open there. It names no task identifier and no internal
// mechanic (P13).
var PhraseCarryOnSlotBusy = Phrase{
	Outcome:     "This answer ended before carrying that work on, because this service was busy with it when this answer came to carry it on.",
	Consequence: "The entry may have changed since this answer read it, so this answer does not say whether it is open.",
	Decision:    "Read what stands with assistant --json status, and answer again if the entry is open there.",
}

// PhraseCarryOnAlreadyStarted is the row for a carry-on answer refused
// because a dispatch or a relaunch of the new task in this service had
// ended and kept its slot before the answer asked for it (a settled
// isolated[id]). That does not say the new task began: a refused relaunch
// keeps the slot with no attempt begun. The row says the answer ended
// there and why, says it cannot tell whether the entry is open, and asks
// the person to read what stands; it does not promise that answering
// again helps (P13).
var PhraseCarryOnAlreadyStarted = Phrase{
	Outcome:     "This answer ended before carrying that work on, because this service had already handled it once.",
	Consequence: "The entry may have changed since this answer read it, so this answer does not say whether it is open.",
	Decision:    "Read what stands with assistant --json status.",
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
	case KeptWorkRemoveAnywayShown:
		return phraseKeptWorkRemoveAnywayShown
	case KeptWorkRemovedAnyway:
		return phraseKeptWorkRemovedAnyway
	}
	return phraseKeptWorkLanded
}

// keptAnswerRows is this file's rows for TablePhrases.
func keptAnswerRows() []Phrase {
	return []Phrase{phraseKeptWorkLanded, phraseKeptWorkCarriedOn, phraseKeptWorkDiscardShown, phraseKeptWorkDiscarded,
		phraseKeptWorkRemoveAnywayShown, phraseKeptWorkRemovedAnyway,
		PhraseKeptWorkLosesNothing, PhraseDiscardRemovalDeferred, PhraseDiscardNeedsTerminal, PhraseRemoveAnywayNeedsTerminal,
		PhraseCarryOnSlotBusy, PhraseCarryOnAlreadyStarted}
}
