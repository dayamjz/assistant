package machine

import "github.com/dayamjz/assistant/internal/managed"

// The rows and the shapes of a service handed to the operating system's
// service manager, which assistant service start --install and assistant
// service stop --uninstall answer with. internal/managed owns the refusal
// set and what a definition says; this file owns what a person reads about
// them, one row per member of managed.Reasons.

// Managed is what is known about a home's managed service: which manager it
// is handed to and where its definition is. It says a definition was
// written, not that the manager has it loaded; whether the service answers
// is Service.Running.
type Managed struct {
	// Manager is the service manager, launchd or systemd.
	Manager string `json:"manager"`
	// Unit is the path of the definition file.
	Unit string `json:"unit"`
}

// ManagedRemoval is the answer to removing a home's managed service.
type ManagedRemoval struct {
	// Managed is the definition that was removed.
	Managed Managed `json:"managed"`
	// Stopped is whether a service answered and accepted the stop before the
	// definition was removed; false is a service that was not up.
	Stopped bool `json:"stopped"`
}

// managedRefusedConsequence is what every install refusal row says was not
// done.
const managedRefusedConsequence = "Nothing was changed, and the service is up only while something keeps it up."

var phraseByManagedRefusal = map[managed.Reason]Phrase{
	managed.ReasonUnsupported: {
		Outcome:     "No managed service was set up, because this build hands the service to no service manager on this operating system.",
		Consequence: managedRefusedConsequence,
		Decision:    "Run assistant service start after each login or restart.",
	},
	managed.ReasonNoUserHome: {
		Outcome:     "No managed service was set up, because the environment names no home folder for your user, which is where its definition goes.",
		Consequence: managedRefusedConsequence,
		Decision:    "Set HOME and repeat the command.",
	},
	managed.ReasonUnwritable: {
		Outcome:     "No managed service was set up, because a path it needs holds a character its definition is not written with.",
		Consequence: managedRefusedConsequence,
		Decision:    "Repeat the command once the path named after this holds no quote, backslash, percent sign, dollar sign or control character.",
	},
	managed.ReasonProtectedFolder: {
		Outcome: "No managed service was set up, because this home, this program or a checkout it serves is inside Documents, Desktop or Downloads, " +
			"which macOS keeps from a program its service manager starts unless you grant it access.",
		Consequence: "Nothing was changed; set up as it is, the service could fail to read those folders.",
		Decision: "Move them out of those folders, or grant this program Full Disk Access in System Settings under Privacy & Security " +
			"and repeat the command with --full-disk-access-granted.",
	},
	managed.ReasonInstalled: {
		Outcome:     "No managed service was set up, because one is already set up for this home.",
		Consequence: "It was left as it was.",
		Decision:    "Run assistant service start to bring it up, or assistant service stop --uninstall to remove it first.",
	},
	managed.ReasonNotInstalled: {
		Outcome:     "Nothing was removed, because no managed service is set up for this home.",
		Consequence: "A service that is up was left as it was.",
		Decision:    "Run assistant service stop to stop one that is not managed.",
	},
	managed.ReasonManagerMissing: {
		Outcome:     "This computer's service manager could not be found on your search path, so nothing was asked of it.",
		Consequence: "Nothing this command did was kept.",
		Decision:    "Repeat the command where launchctl or systemctl is on your PATH.",
	},
	managed.ReasonManagerRefused: {
		Outcome:     "This computer's service manager refused what it was asked, and its own message follows.",
		Consequence: "A definition written for this command was removed again, and one being removed was left in place.",
		Decision:    "Resolve what the message says and repeat the command.",
	},
}

// ManagedRefusalPhrase is what a person reads when internal/managed refused,
// one row per managed.Reasons. A reason outside that set reads as the
// manager having refused, which is the row that sends a person to the
// detail.
func ManagedRefusalPhrase(reason managed.Reason) Phrase {
	if row, ok := phraseByManagedRefusal[reason]; ok {
		return row
	}
	return phraseByManagedRefusal[managed.ReasonManagerRefused]
}

// PhraseManagedServiceUp is the refusal of an install while a service already
// answers for the home: the home's lock lets one service serve it, and the
// install would start a second.
var PhraseManagedServiceUp = Phrase{
	Outcome:     "No managed service was set up, because a service is already up for this home and only one may serve it.",
	Consequence: "Nothing was changed, and the service that is up keeps serving.",
	Decision:    "Run assistant service stop, then repeat the command.",
}

// PhraseServiceManaged is the row beside the service's own row when its home
// has a managed service set up.
var PhraseServiceManaged = Phrase{
	Outcome:     "This home's service is handed to this computer's service manager.",
	Consequence: "The service manager brings it up when your session begins, and does not undo a stop you ask for.",
	Decision:    "Run assistant service stop --uninstall to stop handing it over.",
}

// PhraseManagedRemoved is the answer to a removal that went through.
var PhraseManagedRemoved = Phrase{
	Outcome:     "This home's service is no longer handed to this computer's service manager, and no service is up for it.",
	Consequence: "Nothing brings it up after you log in or restart.",
	Decision:    "Run assistant service start when you want it up again.",
}

// PhraseManagedRestart is what a managed service writes as it ends for a
// restart, which its manager's output holds.
var PhraseManagedRestart = Phrase{
	Outcome:     "The service ended so that this computer's service manager brings up its replacement.",
	Consequence: "A failure status is how the manager is told to; nothing went wrong.",
	Decision:    "Nothing to do.",
}

// PhraseManagedStillUp is a removal that asked the service to stop and found
// it still answering when the wait ran out, so nothing was removed.
var PhraseManagedStillUp = Phrase{
	Outcome:     "The service accepted the stop and was still up when this stopped looking, so it is still handed to the service manager.",
	Consequence: "Nothing was removed.",
	Decision:    "Repeat the command once assistant service status says no service is up.",
}

// managedRows is every row this file holds, as TablePhrases lists them.
func managedRows() []Phrase {
	out := make([]Phrase, 0, len(phraseByManagedRefusal)+5)
	for _, reason := range managed.Reasons() {
		out = append(out, phraseByManagedRefusal[reason])
	}
	return append(out, PhraseManagedServiceUp, PhraseServiceManaged, PhraseManagedRemoved, PhraseManagedRestart, PhraseManagedStillUp)
}
