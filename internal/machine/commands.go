package machine

// The commands assistant init offers, and the rows it speaks them in.
//
// The test, lint and document stages run the commands the trusted
// configuration names and hold for a person when it names none, so a stage
// never reports a pass it did not establish. Init keeps that stance and
// shortens the setup: it reads the checkout's files for the commands the
// repository already declares, offers them, and writes them into the
// checkout's configuration file only with the person's consent (P1). What it
// cannot do is make them take effect. P7 reads commands from the default
// branch as origin holds it, so a written command reaches a run only once
// the file is committed there, and every row that follows a write says so.

// CommandProposals is what assistant init found and did about the commands
// the checks run. File names the configuration file every proposal is about,
// which is internal/config's RepositoryDocument at the root of the checkout
// init ran in; Proposed are the commands offered, each marked with whether it
// was written; Configured are the keys that file already names, which are
// never offered, so init does not replace what a person wrote; and
// NotProposed are the keys left without an offer and why.
//
// File and every command are machine text a person copies exactly, so they
// are carried as they are and printed beside the phrase rather than in it.
type CommandProposals struct {
	// File is the configuration file in the checkout the proposals are
	// about, as a path relative to the checkout's root.
	File string `json:"file"`
	// Proposed are the commands offered, in the order of the keys they set.
	Proposed []ProposedCommand `json:"proposed"`
	// Configured are the keys the file already names, which are not
	// offered whatever was found for them.
	Configured []string `json:"configured,omitempty"`
	// NotProposed are the keys init considers and offered nothing for, each
	// with why.
	NotProposed []NotProposed `json:"not_proposed"`
	// Result is what became of the offer, one of CommandsResults.
	Result CommandsResult `json:"result"`
	// Refusal is why nothing was written, set exactly when Result is
	// CommandsRefused, one of CommandsRefusals.
	Refusal CommandsRefusal `json:"refusal,omitempty"`
	// Detail says what was wrong with the file when Result is
	// CommandsUnreadable or CommandsRefused, and is empty otherwise.
	Detail string `json:"detail,omitempty"`
}

// ProposedCommand is one command init offered for one key.
type ProposedCommand struct {
	// Key is the configuration key the command would set, such as
	// "commands.test".
	Key string `json:"key"`
	// Command is the shell command line offered for it.
	Command string `json:"command"`
	// Source is the file in the checkout the command was read from, and
	// for a build file's target, which target.
	Source string `json:"source"`
	// Written is whether the command is now in the file. It is true only
	// for a command the person consented to that the merge wrote.
	Written bool `json:"written"`
}

// NotProposed is one key init offered nothing for, and why.
type NotProposed struct {
	// Key is the configuration key.
	Key string `json:"key"`
	// Reason is why nothing was offered for it.
	Reason string `json:"reason"`
}

// CommandsResult is what became of the commands init offered. It is a closed
// set, CommandsResults, and each member has its own row in the phrase table.
type CommandsResult string

// The six results, in the order CommandsResults lists them.
const (
	// CommandsNone is nothing to offer: no file in the checkout declares a
	// command for a key the configuration file does not already name.
	CommandsNone CommandsResult = "none"
	// CommandsPrinted is commands offered and nothing written, because no
	// consent was sought or given: the invocation was not at a terminal, or
	// asked for a structured answer, and did not pass the consent flag.
	CommandsPrinted CommandsResult = "printed"
	// CommandsDeclined is commands offered at a terminal and every one
	// declined, so nothing was written.
	CommandsDeclined CommandsResult = "declined"
	// CommandsWritten is at least one command written into the file with
	// the person's consent; ProposedCommand.Written says which.
	CommandsWritten CommandsResult = "written"
	// CommandsUnreadable is commands found while the configuration file in
	// the checkout could not be read, with no consent given, so none was
	// offered for writing and nothing was written.
	CommandsUnreadable CommandsResult = "unreadable"
	// CommandsRefused is consent given and nothing written, because the file
	// could not take the commands; Refusal says why.
	CommandsRefused CommandsResult = "refused"
)

// CommandsResults is every result, so a test walks the set rather than a list
// of its own.
func CommandsResults() []CommandsResult {
	return []CommandsResult{CommandsNone, CommandsPrinted, CommandsDeclined, CommandsWritten, CommandsUnreadable, CommandsRefused}
}

// CommandsRefusal is why commands the person consented to were not written.
// It is a closed set, CommandsRefusals, and each member has its own row.
type CommandsRefusal string

// The two refusals, in the order CommandsRefusals lists them.
const (
	// CommandsFileUnreadable is a configuration file that could not be read,
	// or whose content internal/config refuses, so a merge into it would
	// have to guess at what it means.
	CommandsFileUnreadable CommandsRefusal = "unreadable"
	// CommandsFileChanged is a configuration file that changed between the
	// read the merge was computed from and the write.
	CommandsFileChanged CommandsRefusal = "changed"
)

// CommandsRefusals is every refusal, so a test walks the set rather than a
// list of its own.
func CommandsRefusals() []CommandsRefusal {
	return []CommandsRefusal{CommandsFileUnreadable, CommandsFileChanged}
}

// commandsStillStop is the consequence every row that wrote nothing shares.
const commandsStillStop = "Nothing was written, so a check with no command configured on the default branch still stops each run for your decision."

var phraseByCommandsResult = map[CommandsResult]Phrase{
	CommandsNone: {
		Outcome:     "No commands were offered, because none was found in this checkout beyond what its configuration file already names.",
		Consequence: "A check with no command configured on the default branch still stops each run for your decision.",
		Decision:    "Nothing to decide here; name a command yourself in that file on the default branch when a check should run one.",
	},
	CommandsPrinted: {
		Outcome:     "The commands below were found in this checkout and are offered for its configuration file named below.",
		Consequence: commandsStillStop,
		Decision:    "Run assistant init again at a terminal to accept them one at a time, or with --accept-commands to write them all.",
	},
	CommandsDeclined: {
		Outcome:     "The commands below were found in this checkout and you accepted none of them.",
		Consequence: commandsStillStop,
		Decision:    "Nothing to decide; run assistant init again to be offered them again.",
	},
	CommandsWritten: {
		Outcome:     "The commands marked as written below were added to the configuration file named below, in this checkout only.",
		Consequence: "A run reads commands from that file only as the default branch on origin holds it, so no run uses them until the file is committed there.",
		Decision:    "Commit the file and land it on the default branch named above, by whatever route changes reach that branch; a branch carrying it does not configure its own checks.",
	},
	CommandsUnreadable: {
		Outcome:     "Commands were found in this checkout, but none can be written, because its configuration file named below could not be read.",
		Consequence: commandsStillStop,
		Decision:    "Repair the file and run assistant init again.",
	},
}

var phraseByCommandsRefusal = map[CommandsRefusal]Phrase{
	CommandsFileUnreadable: {
		Outcome:     "The commands below were not written, because the configuration file named below could not be read as one they can be merged into.",
		Consequence: "Nothing was written, so the file is as it was.",
		Decision:    "Repair the file, or add the commands yourself, and run assistant init again.",
	},
	CommandsFileChanged: {
		Outcome:     "The commands below were not written, because the configuration file named below changed while they were being merged into it.",
		Consequence: "Nothing was written, so whatever changed it was kept.",
		Decision:    "Run assistant init again.",
	},
}

// CommandsPhrase is what a person reads about the commands init offered: the
// row for the result, and for a refusal the row for why. A result outside
// CommandsResults reads as the unreadable refusal, which claims nothing was
// written; a refused answer whose refusal is outside CommandsRefusals does
// the same.
func CommandsPhrase(p CommandProposals) Phrase {
	if p.Result == CommandsRefused {
		if row, ok := phraseByCommandsRefusal[p.Refusal]; ok {
			return row
		}
		return phraseByCommandsRefusal[CommandsFileUnreadable]
	}
	if row, ok := phraseByCommandsResult[p.Result]; ok {
		return row
	}
	return phraseByCommandsRefusal[CommandsFileUnreadable]
}

// PhraseCommandsConsent is the row init prints at a terminal before asking
// about each command it found, one at a time.
var PhraseCommandsConsent = Phrase{
	Outcome:     "The commands below were found in this checkout and are not in its configuration file.",
	Consequence: "Each one you accept runs with your credentials on every run of this repository once the file reaches the default branch.",
	Decision:    "Answer y to accept a command, or anything else to leave it out.",
}

// PhraseNoProseCommand is the row saying what init offers for keeping prose
// documentation current, which is nothing. A command configured for that
// check updates documentation by running, and no file a repository commonly
// carries declares one that does that rather than building or serving what
// is already written, so offering one would let the check pass on something
// it did not establish.
var PhraseNoProseCommand = Phrase{
	Outcome:     "Nothing is offered for keeping prose documentation current, because no file this checkout carries declares a command that does that.",
	Consequence: "That check still stops each run for your decision unless a command for it is configured on the default branch.",
	Decision:    "Answer it as each run reaches it, or configure a command of your own that updates the documentation.",
}

// commandsRows is every row this file holds, as TablePhrases lists them.
func commandsRows() []Phrase {
	var out []Phrase
	for _, result := range CommandsResults() {
		if result != CommandsRefused {
			out = append(out, CommandsPhrase(CommandProposals{Result: result}))
		}
	}
	for _, refusal := range CommandsRefusals() {
		out = append(out, CommandsPhrase(CommandProposals{Result: CommandsRefused, Refusal: refusal}))
	}
	return append(out, PhraseCommandsConsent, PhraseNoProseCommand)
}
