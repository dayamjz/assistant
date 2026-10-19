package machine

// The rows assistant init speaks when inspection cannot finish: git is not
// there to read the checkout, origin advertises two addresses, or the
// checkout's configuration file and the default branch's copy disagree about
// the commands init offers. Each refusal sets nothing up. Which exit code one
// ends with is internal/cli's failureExit, not this file's.

// OnboardRefusal is why init stopped during inspection, before a gate or a
// repository record exists. It is a closed set, OnboardRefusals, and each
// member has its own row.
type OnboardRefusal string

// The three refusals, in the order OnboardRefusals lists them.
const (
	// OnboardGitMissing is a checkout init could not read because the git
	// binary could not be started.
	OnboardGitMissing OnboardRefusal = "git_missing"
	// OnboardIdentityAmbiguous is an origin whose fetch address and push
	// address differ, when the caller did not name --upstream.
	OnboardIdentityAmbiguous OnboardRefusal = "identity_ambiguous"
	// OnboardConfigConflict is a checkout whose configuration file and the
	// copy on the default branch disagree on a command init offers.
	OnboardConfigConflict OnboardRefusal = "config_conflict"
)

// OnboardRefusals is every refusal, so a test walks the set rather than a
// list of its own.
func OnboardRefusals() []OnboardRefusal {
	return []OnboardRefusal{OnboardGitMissing, OnboardIdentityAmbiguous, OnboardConfigConflict}
}

// onboardRefusedConsequence is what every refusal row says was not done.
const onboardRefusedConsequence = "No gate was set up and this checkout was left as it was. Nothing was written and no command was run."

var phraseByOnboardRefusal = map[OnboardRefusal]Phrase{
	OnboardGitMissing: {
		Outcome:     "No gate was set up, because git is not available to read this checkout.",
		Consequence: onboardRefusedConsequence,
		Decision:    "Install git so this checkout can be read, then run assistant init again.",
	},
	OnboardIdentityAmbiguous: {
		Outcome:     "No gate was set up, because this checkout's origin has two addresses and which one your changes are destined for is not settled.",
		Consequence: onboardRefusedConsequence,
		Decision:    "Run assistant init again with --upstream naming the address your changes are destined for.",
	},
	OnboardConfigConflict: {
		Outcome:     "No gate was set up, because this checkout's configuration file and the copy on its default branch disagree about which commands are configured.",
		Consequence: onboardRefusedConsequence,
		Decision:    "Make the two copies agree, then run assistant init again.",
	},
}

// OnboardRefusalPhrase is what a person reads when init refused during
// inspection. A refusal outside OnboardRefusals, which init does not make,
// reads as git not being available, which still refuses and still says
// nothing was set up.
func OnboardRefusalPhrase(refusal OnboardRefusal) Phrase {
	if row, ok := phraseByOnboardRefusal[refusal]; ok {
		return row
	}
	return phraseByOnboardRefusal[OnboardGitMissing]
}

// onboardRows is every row this file holds, as TablePhrases lists them.
func onboardRows() []Phrase {
	out := make([]Phrase, 0, len(phraseByOnboardRefusal))
	for _, refusal := range OnboardRefusals() {
		out = append(out, phraseByOnboardRefusal[refusal])
	}
	return out
}
