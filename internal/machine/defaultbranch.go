package machine

// The rows assistant init speaks the default branch in: where the branch it
// recorded came from, and why it could not record one.
//
// PRD principle P7 reads trusted configuration from the default branch, so
// the branch init records is the one fact on its answer a wrong value of
// which would let the branch under validation configure its own checks.
// Each source has a row that says where the answer came from, so a wrong
// answer is visible at the moment it is written, and each refusal has a row
// naming both remedies, so a person who is refused is never left to guess
// the branch for themselves.

// DefaultBranchSource is where the default branch an init recorded came
// from. It is a closed set, DefaultBranchSources, and each member has its
// own row in the phrase table. There is no member for the branch the
// checkout stands on: that is never taken as the answer.
type DefaultBranchSource string

// The two sources, in the order DefaultBranchSources lists them.
const (
	// DefaultBranchFromOrigin is what this working copy records as origin's
	// own HEAD, read from the working copy without fetching, so it is what
	// the working copy last learned rather than what origin says now.
	DefaultBranchFromOrigin DefaultBranchSource = "origin_head"
	// DefaultBranchFromFlag is the branch the caller named with
	// --default-branch, taken as given and compared with nothing.
	DefaultBranchFromFlag DefaultBranchSource = "flag"
)

// DefaultBranchSources is every source, so a test walks the set rather than
// a list of its own.
func DefaultBranchSources() []DefaultBranchSource {
	return []DefaultBranchSource{DefaultBranchFromOrigin, DefaultBranchFromFlag}
}

// DefaultBranchRefusal is why init could not establish the default branch
// without being told it. It is a closed set, DefaultBranchRefusals, and
// each member has its own row in the phrase table, every one of which names
// --default-branch and git remote set-head origin --auto.
type DefaultBranchRefusal string

// The four refusals, in the order DefaultBranchRefusals lists them.
const (
	// DefaultBranchNoOrigin is a working copy with no remote named origin,
	// so there is no record of origin's HEAD to read.
	DefaultBranchNoOrigin DefaultBranchRefusal = "no_origin"
	// DefaultBranchUnrecorded is a working copy with an origin whose HEAD it
	// does not record as a symbolic reference: absent, or an ordinary
	// reference that names a commit and no branch.
	DefaultBranchUnrecorded DefaultBranchRefusal = "unrecorded"
	// DefaultBranchOutsideOrigin is a record of origin's HEAD that points at
	// something other than one of origin's own branches.
	DefaultBranchOutsideOrigin DefaultBranchRefusal = "outside_origin"
	// DefaultBranchUnreadable is a read of origin or of its HEAD that failed
	// for any reason other than the thing read being absent.
	DefaultBranchUnreadable DefaultBranchRefusal = "unreadable"
)

// DefaultBranchRefusals is every refusal, so a test walks the set rather
// than a list of its own.
func DefaultBranchRefusals() []DefaultBranchRefusal {
	return []DefaultBranchRefusal{DefaultBranchNoOrigin, DefaultBranchUnrecorded, DefaultBranchOutsideOrigin, DefaultBranchUnreadable}
}

// defaultBranchCorrection is the decision both source rows give, so the two
// cannot name different ways of correcting a wrong answer.
const defaultBranchCorrection = "If it is not the right branch, run assistant init again with --default-branch naming the right one."

var phraseByDefaultBranchSource = map[DefaultBranchSource]Phrase{
	DefaultBranchFromOrigin: {
		Outcome:     "The branch named below was recorded as the one trusted configuration is read from, because it is the default branch this checkout last learned from origin.",
		Consequence: "Nothing was fetched to confirm it, so a default branch origin changed since then is not reflected here.",
		Decision:    defaultBranchCorrection,
	},
	DefaultBranchFromFlag: {
		Outcome:     "The branch named below was recorded as the one trusted configuration is read from, because you named it with --default-branch.",
		Consequence: "Nothing compared it with what origin records.",
		Decision:    defaultBranchCorrection,
	},
}

// phraseDefaultBranchUnsaid is the row for an answer that names no source,
// which init does not return: it claims no source rather than guessing one.
var phraseDefaultBranchUnsaid = Phrase{
	Outcome:     "The branch named below was recorded as the one trusted configuration is read from, and where that answer came from was not said.",
	Consequence: "Nothing here shows that it is the right one.",
	Decision:    defaultBranchCorrection,
}

// DefaultBranchPhrase is what a person reads about where the default branch
// an init recorded came from: the row for its source, and for a source
// outside DefaultBranchSources a row that claims none.
func DefaultBranchPhrase(source DefaultBranchSource) Phrase {
	if row, ok := phraseByDefaultBranchSource[source]; ok {
		return row
	}
	return phraseDefaultBranchUnsaid
}

// defaultBranchRefusedConsequence is the consequence every refusal row
// shares: what was not done, and why guessing is not the fallback.
const defaultBranchRefusedConsequence = "No gate was set up or repaired and this checkout was left as it was, because trusted configuration is read from that branch, " +
	"and a guess could make the branch being validated its own trusted copy."

var phraseByDefaultBranchRefusal = map[DefaultBranchRefusal]Phrase{
	DefaultBranchNoOrigin: {
		Outcome:     "This checkout's default branch could not be established, because it has no remote named origin to learn it from.",
		Consequence: defaultBranchRefusedConsequence,
		Decision: "Run assistant init again with --default-branch naming it, or add origin, fetch it and run " +
			"git remote set-head origin --auto, then run assistant init again.",
	},
	DefaultBranchUnrecorded: {
		Outcome:     "This checkout's default branch could not be established, because it records no default branch for origin.",
		Consequence: defaultBranchRefusedConsequence,
		Decision:    "Run git remote set-head origin --auto to record it, or run assistant init again with --default-branch naming it.",
	},
	DefaultBranchOutsideOrigin: {
		Outcome:     "This checkout's default branch could not be established, because what it records as origin's default branch is not one of origin's branches.",
		Consequence: defaultBranchRefusedConsequence,
		Decision:    "Run git remote set-head origin --auto to record it afresh, or run assistant init again with --default-branch naming it.",
	},
	DefaultBranchUnreadable: {
		Outcome:     "This checkout's default branch could not be established, because what it records about origin could not be read.",
		Consequence: defaultBranchRefusedConsequence,
		Decision: "Read the cause given after this, then run git remote set-head origin --auto to record it afresh, " +
			"or run assistant init again with --default-branch naming it.",
	},
}

// DefaultBranchRefusalPhrase is what a person reads when init could not
// establish the default branch: the row for the refusal, and for a refusal
// outside DefaultBranchRefusals the unreadable row, which still refuses and
// still names both remedies.
func DefaultBranchRefusalPhrase(refusal DefaultBranchRefusal) Phrase {
	if row, ok := phraseByDefaultBranchRefusal[refusal]; ok {
		return row
	}
	return phraseByDefaultBranchRefusal[DefaultBranchUnreadable]
}

// defaultBranchRows is every row this file holds, as TablePhrases lists
// them.
func defaultBranchRows() []Phrase {
	var out []Phrase
	for _, source := range DefaultBranchSources() {
		out = append(out, DefaultBranchPhrase(source))
	}
	out = append(out, phraseDefaultBranchUnsaid)
	for _, refusal := range DefaultBranchRefusals() {
		out = append(out, DefaultBranchRefusalPhrase(refusal))
	}
	return out
}
