package machine

// Where this checkout's change stands on the way to a run, as assistant
// doctor reads it once the checkout is set up for validation, and the rows it
// speaks that in.
//
// Doctor's contract is that a person does what its first line says and runs
// it again. For a first run that line is one of a short sequence, and these
// rows are that sequence in its order: stand on a branch of the change's own,
// commit what the run should validate, send the branch to the gate, then
// follow the run. Each step is read from the checkout and the gate as they
// stand, so the row printed is the next step and never a list of all of them.
// Nothing here or in the read that fills a Change writes to the checkout:
// every step is a command the person runs, which is P1's consent boundary.

// ChangeStep is the next step a checkout's change needs on the way to a run.
// It is a closed set, ChangeSteps, in the order a first run meets them, and
// each member has its own rows.
type ChangeStep string

// The steps, in the order ChangeSteps lists them.
const (
	// ChangeDetached is a checkout on no branch, which has to be put on one
	// before anything can be sent.
	ChangeDetached ChangeStep = "detached"
	// ChangeOnDefaultBranch is a checkout on the default branch the
	// repository record names, where a change is landed rather than made.
	ChangeOnDefaultBranch ChangeStep = "default-branch"
	// ChangeUncommitted is a branch with work no commit records, which a run
	// does not see. Change's four indicators say which kinds.
	ChangeUncommitted ChangeStep = "uncommitted"
	// ChangeNothingNew is a clean branch holding no commit its default branch
	// lacks, so there is nothing on it to validate.
	ChangeNothingNew ChangeStep = "nothing-new"
	// ChangeUnsent is a clean branch with commits the gate does not hold for
	// it, which a send through the assistant remote hands to a run.
	ChangeUnsent ChangeStep = "unsent"
	// ChangeDiverged is a clean branch that no longer holds the commit the
	// gate holds for it, so sending it replaces that commit.
	ChangeDiverged ChangeStep = "diverged"
	// ChangeSent is a clean branch whose commit is the one the gate already
	// holds for it.
	ChangeSent ChangeStep = "sent"
	// ChangeUnreadable is a checkout or gate that could not be read, so no
	// step is named; Change.Detail says what failed.
	ChangeUnreadable ChangeStep = "unreadable"
)

// ChangeSteps is every step, so a test walks the set rather than a list of
// its own.
func ChangeSteps() []ChangeStep {
	return []ChangeStep{ChangeDetached, ChangeOnDefaultBranch, ChangeUncommitted, ChangeNothingNew,
		ChangeUnsent, ChangeDiverged, ChangeSent, ChangeUnreadable}
}

// Change is where this checkout's change stands, as assistant doctor read it.
// The indicators are read for every step but unreadable, so the structured
// answer carries the same facts a person's rows are chosen from; the branch
// names and the command are the person's own text, carried as they are and
// printed beside the rows rather than in them.
type Change struct {
	// Step is the next step, one of ChangeSteps.
	Step ChangeStep `json:"step"`
	// Branch is the branch the checkout is on, and empty when it is on none.
	Branch string `json:"branch,omitempty"`
	// DefaultBranch is the default branch the repository record names.
	DefaultBranch string `json:"default_branch,omitempty"`
	// Untracked reports a file git does not track and does not ignore.
	Untracked bool `json:"untracked"`
	// Unstaged reports an edit to a tracked file that is not staged.
	Unstaged bool `json:"unstaged"`
	// Staged reports a staged edit that is not committed.
	Staged bool `json:"staged"`
	// Unmerged reports a conflict that is not resolved.
	Unmerged bool `json:"unmerged"`
	// Command is the exact command line the step's row tells the person to
	// run, for the two steps that send the branch, and empty otherwise.
	Command string `json:"command,omitempty"`
	// Detail says what could not be read, set exactly for ChangeUnreadable.
	Detail string `json:"detail,omitempty"`
}

// againDoctor ends every row whose step changes the checkout, which is
// doctor's own contract: do it, then ask again.
const againDoctor = "then run assistant doctor again."

// uncommittedCost is the consequence the rows for uncommitted work share.
const uncommittedCost = "A run validates only what is committed, so it would not see them."

var phraseByChangeStep = map[ChangeStep]Phrase{
	ChangeDetached: {
		Outcome:     "This checkout is on no branch.",
		Consequence: "A run validates a branch, so there is nothing here it can be handed yet.",
		Decision:    "Make a branch for your change with git switch -c and a name of your choosing, which keeps anything uncommitted, " + againDoctor,
	},
	ChangeOnDefaultBranch: {
		Outcome:     "This checkout is on its default branch, named below.",
		Consequence: "A change is validated on a branch of its own before it reaches the default branch, and this one has none yet.",
		Decision:    "Make a branch for your change with git switch -c and a name of your choosing, which carries anything uncommitted with it, " + againDoctor,
	},
	ChangeNothingNew: {
		Outcome:     "This branch holds no commit its default branch does not already have.",
		Consequence: "There is nothing on it for a run to validate yet.",
		Decision:    "Make your change here and commit it, " + againDoctor,
	},
	ChangeUnsent: {
		Outcome:     "This branch has commits that have not been sent for validation.",
		Consequence: "Nothing validates them until they reach the gate, and sending them there is what begins a run.",
		Decision:    "Run the command below to send them, then run assistant watch to follow the run.",
	},
	ChangeDiverged: {
		Outcome:     "This branch no longer holds what was last sent for validation from it.",
		Consequence: "Sending it replaces what the gate holds for this branch with what is here.",
		Decision:    "If that is what you mean, run the command below, then run assistant watch to follow the run; otherwise bring back what was sent before sending again.",
	},
	ChangeSent: {
		Outcome:     "This branch's latest commit has already been sent for validation.",
		Consequence: "There is nothing new on it to send.",
		Decision:    "Run assistant watch to follow what became of it, or assistant rerun to validate it again.",
	},
	ChangeUnreadable: {
		Outcome:     "Where your change stands in this checkout could not be read.",
		Consequence: "No next step for it can be named here.",
		Decision:    readDoctor,
	},
}

// The rows for uncommitted work, one per indicator, printed in this order:
// a conflict first, because nothing commits until it is resolved.
var (
	phraseChangeUnmerged = Phrase{
		Outcome:     "This branch has conflicts that are not resolved.",
		Consequence: "Nothing can be committed until they are, and a run validates only what is committed.",
		Decision:    "Resolve each one, stage it with git add and commit with git commit, " + againDoctor,
	}
	phraseChangeUntracked = Phrase{
		Outcome:     "This branch has files git does not track yet.",
		Consequence: uncommittedCost,
		Decision:    "Stage the ones that belong to your change with git add and commit them with git commit, list any that never should in .gitignore, " + againDoctor,
	}
	phraseChangeUnstaged = Phrase{
		Outcome:     "This branch has edits to tracked files that are not staged.",
		Consequence: uncommittedCost,
		Decision:    "Stage them with git add and commit them with git commit, " + againDoctor,
	}
	phraseChangeStaged = Phrase{
		Outcome:     "This branch has staged edits that are not committed.",
		Consequence: uncommittedCost,
		Decision:    "Commit them with git commit, " + againDoctor,
	}
)

// ChangePhrases is what a person reads about where the change stands: the
// step's row, or for uncommitted work a row per kind present, a conflict
// first and then untracked, unstaged and staged. A step outside ChangeSteps,
// and an uncommitted change with no indicator set, read as the unreadable
// row, which names no step rather than a wrong one.
func ChangePhrases(c Change) []Phrase {
	if c.Step == ChangeUncommitted {
		var out []Phrase
		for _, kind := range []struct {
			set bool
			row Phrase
		}{{c.Unmerged, phraseChangeUnmerged}, {c.Untracked, phraseChangeUntracked}, {c.Unstaged, phraseChangeUnstaged}, {c.Staged, phraseChangeStaged}} {
			if kind.set {
				out = append(out, kind.row)
			}
		}
		if len(out) > 0 {
			return out
		}
		return []Phrase{phraseByChangeStep[ChangeUnreadable]}
	}
	if row, ok := phraseByChangeStep[c.Step]; ok {
		return []Phrase{row}
	}
	return []Phrase{phraseByChangeStep[ChangeUnreadable]}
}

// phraseRunCanStartChange is the doctor's verdict when a run can start and the
// change has a step of its own, which the line after it names, so the verdict
// does not say there is nothing to do.
var phraseRunCanStartChange = Phrase{
	Outcome:     "A run can start here.",
	Consequence: "Everything a run needs is in place.",
	Decision:    "Do what the line below says about your change.",
}

// changeRows is every row this file holds, as TablePhrases lists them.
func changeRows() []Phrase {
	out := []Phrase{phraseChangeUnmerged, phraseChangeUntracked, phraseChangeUnstaged, phraseChangeStaged}
	for _, step := range ChangeSteps() {
		if step != ChangeUncommitted {
			out = append(out, phraseByChangeStep[step])
		}
	}
	return append(out, phraseRunCanStartChange)
}
