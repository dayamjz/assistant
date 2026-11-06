package machine

import "github.com/dayamjz/assistant/internal/supervisor"

// The stop guard's two phrases, which the host hook reads out to the
// coordinator when PRD section 5's stop guard decides a turn. They are the
// phrase table's two turn rows, in phrases.go, exported as the strings the
// host's message begins with, and TablePhrases lists them so the vocabulary
// test holds them to the rule beside every other row. Neither names a step of
// its own: the host's message is this phrase followed by the pull guard's,
// which names the decision the situation calls for, and each row's Decision
// says exactly that.
var (
	// PhraseTurnBlocked is what the coordinator reads when its turn may not
	// end: work is under way, nothing is watching it, and the turn has to
	// deal with that before it ends.
	PhraseTurnBlocked = phraseTurnBlocked.String()
	// PhraseTurnReEntered is what the coordinator reads when its turn may
	// end although nothing is watching the work, because the host re-entered
	// it after a block: the guard answers once per re-entry and judges the
	// next turn afresh.
	PhraseTurnReEntered = phraseTurnReEntered.String()
)

// HostStop is the stop guard's answer to the coordinator's host, PRD section
// 5's stop guard as one document: whether the turn may end, what the guard
// found and why, and the message the host relays. It is the verdict's shape
// and nothing about the host's protocol: which exit status says blocked and
// which stream the message goes on are the host's convention, and
// internal/cli's host adapter maps this shape onto them.
//
// Allowed, Message and the supervision are written together by HostStopOf
// and nowhere else, so an answer that blocks cannot stand beside an empty
// message and an answer that allows cannot carry one that says the turn is
// blocked.
type HostStop struct {
	// Supervision is what the guard found: internal/supervisor's verdict
	// and, when it blocks, the pull guard's phrase for it. A Reason that is
	// empty is a guard that was not computed, which Detail explains; it
	// blocks, because no answer must mean blocked.
	Supervision Supervision `json:"supervision"`
	// ReEntered says the host supplied its re-entry signal, which is the one
	// input that allows a turn to end with work under way unwatched.
	ReEntered bool `json:"re_entered"`
	// Allowed is whether the turn may end: the verdict allows, or the host
	// re-entered the turn.
	Allowed bool `json:"allowed"`
	// Message is what the coordinator reads, empty when the verdict allows
	// on its own: the turn's phrase and then the pull guard's, which names
	// the next decision.
	Message string `json:"message,omitempty"`
	// Detail says why the guard could not be computed, when it could not,
	// and is empty otherwise.
	Detail string `json:"detail,omitempty"`
	// InputUnread says why what the host wrote to the hook's input was not
	// read as a re-entry signal although something was there: input that
	// does not parse, says the signal twice or as another type, is too
	// large, or did not end in time. It is empty when there was no input or
	// the input was read. It never allows: input this explains carries no
	// signal, so ReEntered is set only by a signal from elsewhere.
	InputUnread string `json:"input_unread,omitempty"`
}

// HostStopOf is the one translation from the guard's verdict and the host's
// re-entry signal to the answer the host reads. A verdict that allows is an
// allowed answer with no message, whatever the signal. A verdict that blocks
// without the signal is a blocked answer whose message says the turn cannot
// end and then what to do, through SupervisionOf. The same verdict with the
// signal is an allowed answer whose message says why it was allowed, so the
// one turn the host re-entered can end and the record of why is on the
// stream the host keeps.
//
// The signal allows exactly the invocation it was given to. Nothing here
// stores anything, so this function cannot tell a first re-entered turn from
// a second: that bound is the host's, which supplies the signal on the turn
// it re-entered after a block and on no other, and the host's own protocol
// is what consumes it. A host that supplied the signal on every turn would
// have disabled the guard, and nothing here can tell.
func HostStopOf(v supervisor.StopVerdict, reEntered bool) HostStop {
	answer := HostStop{Supervision: SupervisionOf(v), ReEntered: reEntered}
	if v.Allowed() {
		answer.Allowed = true
		return answer
	}
	if reEntered {
		answer.Allowed = true
		answer.Message = PhraseTurnReEntered + " " + answer.Supervision.Warning
		return answer
	}
	answer.Message = PhraseTurnBlocked + " " + answer.Supervision.Warning
	return answer
}

// HostInstall is the answer to installing the stop guard's hook into the
// coordinator's host: PRD section 6's stop guard row says the installer
// prints the exact line the host's configuration needs and writes it only
// when given an explicit flag, so without that flag nothing in the host's
// configuration changes (P1). This shape is what that installer found and
// did, and nothing about how it found it: which file the host reads, which
// host was selected, and how the line merges into it are internal/cli's
// adapter.
//
// Settings and Line are machine text a person copies exactly, so they are
// carried as they are and printed beside the phrase rather than in it.
type HostInstall struct {
	// Settings is the host's settings file the line belongs in, whether or
	// not anything was written to it.
	Settings string `json:"settings"`
	// Line is the exact configuration line: a whole settings document on one
	// line, which is the file's content when it holds nothing else, and
	// whose one hook merges beside whatever hooks the file already holds.
	Line string `json:"line"`
	// Command is the command line the hook runs, which is the part of Line
	// this program owns.
	Command string `json:"command"`
	// Host is which coordinator host the request targeted, one of claude or
	// codex, empty when the answer predates host selection.
	Host string `json:"host,omitempty"`
	// Result is what became of the request, one of HostInstallResults.
	Result HostInstallResult `json:"result"`
	// Refusal is why the line was not written, set exactly when Result is
	// HostInstallRefused, one of HostInstallRefusals.
	Refusal HostInstallRefusal `json:"refusal,omitempty"`
	// Conflict is the command line of the hook the file already holds that
	// the line could not be added beside, when Refusal is
	// HostInstallConflict, carried as the file holds it.
	Conflict string `json:"conflict,omitempty"`
	// Detail says what was wrong with the file when the line was refused, and
	// is empty otherwise.
	Detail string `json:"detail,omitempty"`
	// WakeCommand is PRD section 8's host wake for the tmux pane the
	// installer ran in: the command line that, set as notify.command in the
	// home's configuration, has the service type a notice into that pane for
	// every wake it queues. It is machine text a person copies, and it is
	// only ever printed, whatever the explicit flag says: the configuration
	// file is the operator's, and a notify.command it already holds is a
	// channel of theirs the line would replace. Empty when none was offered,
	// and then WakeAbsent says why.
	WakeCommand string `json:"wake_command,omitempty"`
	// WakeProgram is the program the pane ran when the installer read it,
	// which WakeCommand carries and the wake checks before it types, so it
	// types only into the program the person installed it from.
	WakeProgram string `json:"wake_program,omitempty"`
	// WakeAbsent says why no host wake was offered: the installer did not
	// run inside a tmux pane, the pane could not be read, or the installer
	// was itself the pane's foreground program, as WakeInstallerForeground
	// says. Empty when WakeCommand is set.
	WakeAbsent string `json:"wake_absent,omitempty"`
	// WakeInstallerForeground says no host wake was offered because the
	// installer led the foreground process group of the pane's terminal, as
	// a command typed at the pane's shell does, so the program tmux named for
	// the pane was the installer itself, which no host started there later
	// would match.
	WakeInstallerForeground bool `json:"wake_installer_foreground,omitempty"`
}

// HostWake is the answer of PRD section 8's host wake, which the service
// runs as its notify command for each wake it queues: the notice it typed
// into the coordinator's tmux pane, and which pane. It answers only for a
// notice typed; a pane that could not be read, or that no longer belongs to
// the server or the program the installer saw, is a failure with nothing
// typed, and the notify command's record keeps why.
type HostWake struct {
	// Pane is the tmux pane the notice was typed into, as tmux names it.
	Pane string `json:"pane"`
	// Program is the program the pane ran, checked against the one the
	// installer saw before anything was typed.
	Program string `json:"program"`
	// Typed is the text typed, which is the phrase table's notice about a
	// queued wake, the one listen prints, followed by Enter.
	Typed string `json:"typed"`
}

// HostWakeNotice is the text the host wake types into the coordinator's
// pane: the notice listen prints for a queued wake, so the coordinator reads
// one sentence for one fact whichever way it was woken (P14). It carries no
// backquote, dollar sign or other character a shell would expand, so text
// typed into a pane whose program the check let through is still a sentence.
func HostWakeNotice() string { return PhraseNoticeQueued.String() }

// HostWakePhrase is what a person reads about a notice the host wake typed.
func HostWakePhrase(HostWake) Phrase { return phraseHostWakeTyped }

// HostInstallWakePhrase is what a person reads about the host wake the
// installer offered beside the stop guard's line, or about why it offered
// none.
func HostInstallWakePhrase(h HostInstall) Phrase {
	switch {
	case h.WakeCommand != "":
		return phraseHostWakeOffered
	case h.WakeInstallerForeground:
		return phraseHostWakeInstallerForeground
	}
	return phraseHostWakeAbsent
}

// HostInstallResult is what became of a request to install the stop guard's
// hook. It is a closed set, HostInstallResults, and each member has its own
// row in the phrase table.
type HostInstallResult string

// The four results, in the order HostInstallResults lists them.
const (
	// HostInstallPrinted is the default: the line was printed and nothing
	// was written, because the explicit flag was not given.
	HostInstallPrinted HostInstallResult = "printed"
	// HostInstallWritten is the line added to the settings file under the
	// explicit flag, with everything else the file held kept.
	HostInstallWritten HostInstallResult = "written"
	// HostInstallPresent is the explicit flag given over a file that already
	// holds exactly this hook, so nothing was written.
	HostInstallPresent HostInstallResult = "present"
	// HostInstallRefused is the explicit flag given over a file the line
	// could not be added to, so nothing was written; Refusal says why.
	HostInstallRefused HostInstallResult = "refused"
)

// HostInstallResults is every result, so a test walks the set rather than a
// list of its own.
func HostInstallResults() []HostInstallResult {
	return []HostInstallResult{HostInstallPrinted, HostInstallWritten, HostInstallPresent, HostInstallRefused}
}

// HostInstallRefusal is why the line was not written under the explicit
// flag. It is a closed set, HostInstallRefusals, and each member has its own
// row in the phrase table.
type HostInstallRefusal string

// The three refusals, in the order HostInstallRefusals lists them.
const (
	// HostInstallConflict is a file that already holds a turn-end hook
	// running this program's stop guard with a different command line, which
	// would be a second guard over another binary or another home, or the
	// same command marked to run in the background. Conflict equals Command
	// in the background case and differs from it when the command line does.
	// HostInstallPhrase reads that equality so the two cases do not share a
	// sentence.
	HostInstallConflict HostInstallRefusal = "conflict"
	// HostInstallUnreadable is a file that could not be read, or whose
	// content is not a settings document the line can be merged into
	// without guessing at what it means.
	HostInstallUnreadable HostInstallRefusal = "unreadable"
	// HostInstallChanged is a file that changed between the read the merge
	// was computed from and the write, so the write would have replaced
	// content the merge never saw.
	HostInstallChanged HostInstallRefusal = "changed"
)

// HostInstallRefusals is every refusal, so a test walks the set rather than
// a list of its own.
func HostInstallRefusals() []HostInstallRefusal {
	return []HostInstallRefusal{HostInstallConflict, HostInstallUnreadable, HostInstallChanged}
}
