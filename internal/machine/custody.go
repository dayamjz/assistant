package machine

import "github.com/dayamjz/assistant/internal/custody"

// The rows assistant sync and assistant status speak PRD section 11 in.
//
// A plan is phrased as one row for what custody found or did, chosen by
// PlanPhrase, followed for a plan not yet carried out by a row per step,
// chosen by StepPhrase. Neither names a commit, a reference or a run: those
// are what a person needs to act on a recovery reference or to check a
// move, and they are the structured rendering's, which every row that needs
// one points at. The class and step names are internal/custody's and travel
// in the structured rendering as they are.

// phrasePlanned and phraseDone are the rows for a class, before its steps
// are carried out and after. A class that does nothing has the same row in
// both.
var (
	phrasePlanned = map[custody.Class]Phrase{
		custody.ClassPipelineHolds: {
			Outcome:     "A run of this branch is still under way.",
			Consequence: "Your branch is not changed while it goes on.",
			Decision:    "Let it finish, or answer it if it asks you; assistant status says which.",
		},
		custody.ClassYoursAlready: {
			Outcome:     "Your branch already has everything the pipeline has for it.",
			Consequence: "Nothing needs to change.",
			Decision:    "Nothing to decide.",
		},
		custody.ClassRecoverable: {
			Outcome:     "A run that ended moved this branch's work on, and that work is not on your branch.",
			Consequence: "Bringing it back keeps it beside your branch under a name of this program's own and changes nothing you have.",
			Decision:    "Read the steps below, then run assistant sync --confirm to bring it back.",
		},
		custody.ClassBehind: {
			Outcome:     "Your branch is behind the change the pipeline sent on for it.",
			Consequence: "Moving it forward loses nothing you have, and needs this checkout's files clean.",
			Decision:    "Read the steps below, then run assistant sync --confirm to move it forward.",
		},
		custody.ClassDivergedEquivalent: {
			Outcome:     "Your branch and the change the pipeline sent on for it have different histories with the same content.",
			Consequence: "Taking the pipeline's replaces your branch's history, and your current one is kept first under a name of this program's own.",
			Decision:    "Read the steps below, then run assistant sync --confirm to take it.",
		},
		custody.ClassDiverged: {
			Outcome:     "Your branch and what the pipeline has for it differ in content, or your branch moved after the pipeline took it.",
			Consequence: "Nothing was changed, because moving your branch could lose work.",
			Decision:    "Bring the two together yourself, then send the branch again; assistant --json sync says why.",
		},
	}
	phraseDone = map[custody.Class]Phrase{
		custody.ClassRecoverable: {
			Outcome:     "The work a run that ended had moved on is back in this checkout, kept beside your branch.",
			Consequence: "Your branch is as it was.",
			Decision:    "Decide whether to take it into your branch yourself; assistant --json sync names where it is kept.",
		},
		custody.ClassBehind: {
			Outcome:     "Your branch was moved forward to the change the pipeline sent on for it.",
			Consequence: "Nothing you had was lost, and your files now match it.",
			Decision:    "Nothing to decide.",
		},
		custody.ClassDivergedEquivalent: {
			Outcome:     "Your branch now has the history the pipeline sent on for it, with the same content as before.",
			Consequence: "Your previous history is kept under a name of this program's own, which assistant --json sync names.",
			Decision:    "Nothing to decide.",
		},
	}
)

// phraseByRefusal is the row for each reason custody refuses, grouped by
// what the person does next rather than by the mechanic.
var phraseByRefusal = map[custody.Reason]Phrase{
	custody.ReasonNoRun: {
		Outcome:     "No run has checked this branch, so the pipeline has nothing for it.",
		Consequence: "Nothing was changed.",
		Decision:    "Nothing to decide.",
	},
	custody.ReasonDetached: {
		Outcome:     "This checkout is on no branch, so there is no branch to bring up to date.",
		Consequence: "Nothing was changed.",
		Decision:    "Switch to the branch you want reconciled and run assistant sync there.",
	},
	custody.ReasonCopyUnclean: {
		Outcome:     "A run's local copy holds changes nobody committed, so what it did cannot be brought back for you.",
		Consequence: "Nothing was changed, and the copy is kept as it is.",
		Decision:    "Look at that copy yourself; assistant --json sync says where it is.",
	},
	custody.ReasonNotCurrent: {
		Outcome:     "The planned change was not made, because the code host no longer has the change this branch would move to.",
		Consequence: "Your branch and your files are as they were.",
		Decision:    "Run assistant sync again for a fresh plan.",
	},
	custody.ReasonRecoveryTaken: {
		Outcome:     "The work could not be kept beside your branch, because the name it is kept under already holds something else.",
		Consequence: "Nothing was replaced, and your branch is where it was.",
		Decision:    "Read what that name holds with assistant --json sync, and move it aside yourself if you no longer need it.",
	},
	custody.ReasonRefused: {
		Outcome:     "The planned change to your branch was not made, because making it now could lose or overwrite work.",
		Consequence: "Your branch and your files are as they were.",
		Decision:    "Commit what is uncommitted or close the other checkout of this branch, then run assistant sync again; assistant --json sync says which.",
	},
	custody.ReasonNotSynchronizable: {
		Outcome:     "There is nothing to carry out for this branch.",
		Consequence: "Nothing was changed.",
		Decision:    "Nothing to decide.",
	},
}

// phraseUndetermined is the row for every refusal that is a fact custody
// could not establish: a run that left no record of its position, a
// history cut short, a read that failed. It is also what a reason this
// table has no row for reads as, so a refusal never reads as something
// done.
var phraseUndetermined = Phrase{
	Outcome:     "This branch could not be reconciled, because something it depends on could not be established.",
	Consequence: "Nothing was changed, rather than acting on a guess.",
	Decision:    "Run assistant --json sync for what could not be established.",
}

// phraseUnclassified is the row for a plan that carries neither a refusal
// nor a classification, which the service does not answer; it reads as
// nothing done.
var phraseUnclassified = Phrase{
	Outcome:     "This branch was not classified.",
	Consequence: "Nothing was changed.",
	Decision:    "Run assistant --json sync for why.",
}

// PlanPhrase is the row for a plan: the refusal's when it carries one, the
// done row for its class when it was carried out, and the planned row
// otherwise.
func PlanPhrase(p Plan) Phrase {
	if p.Refusal != "" {
		if row, ok := phraseByRefusal[p.Refusal]; ok {
			return row
		}
		return phraseUndetermined
	}
	if p.Classification == nil {
		return phraseUnclassified
	}
	if p.Applied {
		if row, ok := phraseDone[p.Classification.Class]; ok {
			return row
		}
	}
	if row, ok := phrasePlanned[p.Classification.Class]; ok {
		return row
	}
	return phraseUndetermined
}

// phraseByStep is the row for each step a plan lists.
var phraseByStep = map[custody.StepKind]Phrase{
	custody.StepVerify: {
		Outcome:     "First, the code host is read again and must still have the change this branch would move to.",
		Consequence: "Nothing is fetched from it or written to it.",
		Decision:    "If it no longer does, nothing below happens.",
	},
	custody.StepFetch: {
		Outcome:     "The pipeline's change is copied into this checkout under a name of this program's own.",
		Consequence: "Nothing of yours changes by it.",
		Decision:    "Nothing to decide for this step.",
	},
	custody.StepRecover: {
		Outcome:     "The run's work is kept beside your branch under a name of this program's own.",
		Consequence: "A name that already holds something else is never replaced.",
		Decision:    "Nothing to decide for this step.",
	},
	custody.StepKeep: {
		Outcome:     "Your branch's current history is kept under a name of this program's own before anything moves.",
		Consequence: "It stays there until you remove it, and a name that already holds something else is never replaced.",
		Decision:    "Nothing to decide for this step.",
	},
	custody.StepMove: {
		Outcome:     "Your branch is moved to the pipeline's change, only if it still stands where the run found it.",
		Consequence: "If it moved, or this checkout has uncommitted or untracked files, or the branch is checked out somewhere else, nothing moves.",
		Decision:    "Nothing to decide for this step.",
	},
	custody.StepRelease: {
		Outcome:     "The name the change was copied under is removed.",
		Consequence: "Only that name goes; the change stays where the steps above put it.",
		Decision:    "Nothing to decide for this step.",
	},
}

// phraseUnfamiliarStep is the row for a step of a kind the table has no row
// for, which internal/custody's closed set keeps from being produced.
var phraseUnfamiliarStep = Phrase{
	Outcome:     "A step this build cannot describe is in the plan.",
	Consequence: "What it does is in the structured answer.",
	Decision:    "Run assistant --json sync before confirming.",
}

// StepPhrase is the row for one step of a plan.
func StepPhrase(s custody.Step) Phrase {
	if row, ok := phraseByStep[s.Kind]; ok {
		return row
	}
	return phraseUnfamiliarStep
}

// phraseStatusAct is the decision a status gives for a class sync would act
// on: the status prints no steps, so it points at the command that does.
const phraseStatusAct = "Run assistant sync for the exact steps before changing anything."

// CustodyPhrase is the row assistant status prints for a branch's
// classification, and false when it prints none: for a branch the pipeline
// holds, whose run status already reports, and for a branch no run has
// checked, which status already says. A class sync would act on is the
// planned row with the decision pointing at assistant sync, because status
// shows no steps for the row's own decision to refer to.
func CustodyPhrase(c Custody) (Phrase, bool) {
	if c.Refusal == custody.ReasonNoRun {
		return Phrase{}, false
	}
	if c.Classification != nil && c.Classification.Class == custody.ClassPipelineHolds {
		return Phrase{}, false
	}
	row := PlanPhrase(Plan{Classification: c.Classification, Refusal: c.Refusal})
	if c.Classification != nil && c.Classification.Class.Acts() {
		row.Decision = phraseStatusAct
	}
	return row, true
}

// custodyRows is every row this file holds, as TablePhrases lists them.
func custodyRows() []Phrase {
	var out []Phrase
	for _, class := range custody.Classes() {
		out = append(out, phrasePlanned[class])
		if row, ok := phraseDone[class]; ok {
			out = append(out, row)
		}
		if row, ok := CustodyPhrase(Custody{Classification: &custody.Classification{Class: class}}); ok {
			out = append(out, row)
		}
	}
	for _, reason := range custody.Reasons() {
		out = append(out, PlanPhrase(Plan{Refusal: reason}))
	}
	out = append(out, phraseUndetermined, phraseUnclassified)
	for _, kind := range custody.StepKinds() {
		out = append(out, StepPhrase(custody.Step{Kind: kind}))
	}
	return append(out, phraseUnfamiliarStep)
}
