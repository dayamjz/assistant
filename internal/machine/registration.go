package machine

// The rows registering a checkout speaks beyond the gate's own: what a
// repeated assistant init changed in the repository record, the refusal for
// an origin that sends a push somewhere other than where it fetches from, and
// the refusal any command reading a checkout makes when git cannot be
// started. Which exit code a refusal ends with is internal/cli's failureExit,
// not this file's.

// RecordField is one field of the repository record init writes that a
// person chose or could have chosen with a flag. It is a closed set,
// RecordFields.
type RecordField string

// The three fields, in the order RecordFields lists them.
const (
	// RecordUpstream is where validated changes are destined, --upstream or
	// origin.
	RecordUpstream RecordField = "upstream"
	// RecordFork is the fork validated changes are sent to and proposed
	// from, --fork-url, and empty when none is recorded.
	RecordFork RecordField = "fork"
	// RecordDefaultBranch is the branch trusted configuration is read from,
	// --default-branch or what origin's HEAD names.
	RecordDefaultBranch RecordField = "default_branch"
)

// RecordFields is every field init reports a change to, so a test walks the
// set rather than a list of its own.
func RecordFields() []RecordField {
	return []RecordField{RecordUpstream, RecordFork, RecordDefaultBranch}
}

// RecordChange is one field a repeated init wrote a different value into.
// Before and After are the record's values as internal/store holds them,
// credentials already removed, and an empty one is a field with no value,
// which only a fork can be.
type RecordChange struct {
	// Field is which field changed.
	Field RecordField `json:"field"`
	// Before is the value the record held before this init.
	Before string `json:"before"`
	// After is the value it holds now.
	After string `json:"after"`
}

// The rows for a field a repeated init changed. Each says what a run does now
// and names the flag that puts the old value back, because init records what
// it was given and an init run again without a flag records the default for
// it, which is how a fork recorded once stops being recorded.
var (
	phraseUpstreamChanged = Phrase{
		Outcome:     "The upstream your changes are destined for is not the one recorded for this checkout before.",
		Consequence: "A run from now on sends its change to the upstream recorded now.",
		Decision:    "If that is not where your changes go, run assistant init again with --upstream naming the one they go to.",
	}
	phraseForkDropped = Phrase{
		Outcome:     "This checkout no longer records the fork its changes were sent to, because init was not given --fork-url this time.",
		Consequence: "A run from now on sends its change to the upstream rather than to your fork.",
		Decision:    "To keep sending to your fork, run assistant init again with --fork-url naming it.",
	}
	phraseForkChanged = Phrase{
		Outcome:     "The fork recorded for this checkout is not the one recorded before.",
		Consequence: "A run from now on sends its change to the fork recorded now and proposes from there.",
		Decision:    "If that is not your fork, run assistant init again with --fork-url naming it.",
	}
	phraseDefaultBranchChanged = Phrase{
		Outcome:     "The branch trusted configuration is read from is not the one recorded for this checkout before.",
		Consequence: "A run from now on reads its configuration and commands from the branch named below.",
		Decision:    "If that is not your default branch, run assistant init again with --default-branch naming it.",
	}
)

// RecordChangePhrase is what a person reads about one changed field. A fork
// changed to no value reads as dropped, and any other fork change as changed.
// A field outside RecordFields, which init does not report, reads as the
// upstream row, which still says a run's destination may have moved and
// names the flag that settles it.
func RecordChangePhrase(c RecordChange) Phrase {
	switch c.Field {
	case RecordFork:
		if c.After == "" {
			return phraseForkDropped
		}
		return phraseForkChanged
	case RecordDefaultBranch:
		return phraseDefaultBranchChanged
	}
	return phraseUpstreamChanged
}

// phraseInitRefreshed is InitPhrase's row for a checkout initialized again
// over the gate it already had: internal/gate wrote its hooks, its record and
// the remote again, and created nothing.
var phraseInitRefreshed = Phrase{
	Outcome:     "This checkout already had its gate, and init set it up again where it stood.",
	Consequence: "The runs recorded through it are kept, and a change sent to it is validated before it goes anywhere.",
}

// PhraseInitOriginAmbiguous is init's refusal when it was given no --upstream
// and origin sends a push to another repository than the one it fetches
// from, so which one is where your changes go is not settled by origin.
var PhraseInitOriginAmbiguous = Phrase{
	Outcome:     "No gate was set up, because this checkout's origin sends what you send to another repository than the one it reads from.",
	Consequence: "This checkout was left as it was, and nothing was guessed in place of where your changes go.",
	Decision:    "Run assistant init again with --upstream naming the repository your changes are destined for.",
}

// PhraseGitMissing is the refusal of any command that has to read the
// checkout it was run in when git could not be started to read it, said as
// that rather than as a directory that is not a checkout.
var PhraseGitMissing = Phrase{
	Outcome:     "Nothing was done, because git could not be run to read this checkout.",
	Consequence: "Nothing was changed, and nothing here can be validated until git can be run.",
	Decision:    "Install git so that it is on this PATH, then repeat the command; assistant doctor says whether it can be run.",
}

// registrationRows is every row this file holds, as TablePhrases lists them.
func registrationRows() []Phrase {
	out := make([]Phrase, 0, len(RecordFields())+4)
	for _, field := range RecordFields() {
		out = append(out, RecordChangePhrase(RecordChange{Field: field, After: "a value"}))
	}
	out = append(out, RecordChangePhrase(RecordChange{Field: RecordFork, Before: "a value"}))
	out = append(out, InitPhrase(Init{}), PhraseInitOriginAmbiguous, PhraseGitMissing)
	return out
}
