package machine

// The rows assistant init speaks when it is given a fork, and the row its
// answer carries when it recorded one.
//
// A fork changes where a run's work goes: the branch is sent to the fork and
// the pull request is proposed from it to the upstream. init records one only
// when both are repositories on the code host this build proposes changes
// on, and never one naming the upstream itself, because otherwise one of the
// two writes would have nowhere to go and the other would be a guess. Which
// exit code a refusal ends with is internal/cli's failureExit, not this
// file's.

// ForkRefusal is why init did not record the fork it was given. It is a
// closed set, ForkRefusals, and each member has its own row.
type ForkRefusal string

// The three refusals, in the order ForkRefusals lists them.
const (
	// ForkUnaddressable is a fork that is not a repository on the code host
	// this build proposes changes on.
	ForkUnaddressable ForkRefusal = "fork_unaddressable"
	// ForkUpstreamUnaddressable is a fork given beside an upstream that is
	// not a repository on that host, so there is nothing to propose to.
	ForkUpstreamUnaddressable ForkRefusal = "upstream_unaddressable"
	// ForkIsUpstream is a fork naming the upstream itself.
	ForkIsUpstream ForkRefusal = "fork_is_upstream"
)

// ForkRefusals is every refusal, so a test walks the set rather than a list
// of its own.
func ForkRefusals() []ForkRefusal {
	return []ForkRefusal{ForkUnaddressable, ForkUpstreamUnaddressable, ForkIsUpstream}
}

// forkRefusedConsequence is what every refusal row says was not done.
const forkRefusedConsequence = "This checkout was left as it was, and nothing was guessed in place of where your changes go."

var phraseByForkRefusal = map[ForkRefusal]Phrase{
	ForkUnaddressable: {
		Outcome:     "No gate was set up, because the fork you named is not a repository on github.com, where this build proposes changes.",
		Consequence: forkRefusedConsequence,
		Decision:    "Run assistant init again with --fork-url naming your fork on github.com, or without it to send changes to the upstream.",
	},
	ForkUpstreamUnaddressable: {
		Outcome:     "No gate was set up, because a change sent to a fork is proposed to the upstream, and the upstream is not a repository on github.com.",
		Consequence: forkRefusedConsequence,
		Decision:    "Run assistant init again with --upstream naming the repository on github.com your fork was made from, or without --fork-url.",
	},
	ForkIsUpstream: {
		Outcome:     "No gate was set up, because the fork you named is the upstream itself.",
		Consequence: forkRefusedConsequence,
		Decision:    "Run assistant init again with --fork-url naming your own fork, or without it to send changes to the upstream.",
	},
}

// ForkRefusalPhrase is what a person reads when init refused the fork it was
// given. A refusal outside ForkRefusals, which init does not make, reads as
// the fork not being a repository this build can propose from.
func ForkRefusalPhrase(refusal ForkRefusal) Phrase {
	if row, ok := phraseByForkRefusal[refusal]; ok {
		return row
	}
	return phraseByForkRefusal[ForkUnaddressable]
}

// PhraseInitFork is the row init's answer carries when the record names a
// fork, after the row for the gate itself.
var PhraseInitFork = Phrase{
	Outcome:     "Changes validated here are sent to your fork, and proposed from it to the upstream.",
	Consequence: "The upstream is sent a proposal and none of your branches.",
	Decision:    "Nothing to do.",
}

// forkRows is every row this file holds, as TablePhrases lists them.
func forkRows() []Phrase {
	out := make([]Phrase, 0, len(phraseByForkRefusal)+1)
	for _, refusal := range ForkRefusals() {
		out = append(out, phraseByForkRefusal[refusal])
	}
	return append(out, PhraseInitFork)
}
