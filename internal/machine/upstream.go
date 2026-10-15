package machine

// The row assistant init speaks when it cannot learn where validated changes
// go.
//
// The upstream init records is the remote internal/service points a run's
// copy at, so init never guesses one: a working copy with no remote named origin, and
// no --upstream naming one, is refused before anything is set up, and the
// remedy is a flag the caller supplies. Which exit code that refusal ends with
// is internal/cli's failureExit, not this file's.

// PhraseInitNoUpstream is what a person reads when init was given no
// --upstream and the working copy has no remote named origin to take the
// upstream from. It names --upstream, which is the remedy, and
// --default-branch beside it, because a working copy with no origin has no
// default branch to learn either, and a person given only the first flag
// would be refused again for the second.
var PhraseInitNoUpstream = Phrase{
	Outcome:     "No gate was set up, because this checkout has no remote named origin to learn where your changes go from.",
	Consequence: "This checkout was left as it was, and nothing was guessed in place of where your changes go.",
	Decision: "Run assistant init again with --upstream naming the repository your changes are destined for and " +
		"--default-branch naming its default branch, or add origin and run assistant init again.",
}

// upstreamRows is every row this file holds, as TablePhrases lists them.
func upstreamRows() []Phrase { return []Phrase{PhraseInitNoUpstream} }
