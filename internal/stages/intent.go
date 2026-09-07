package stages

import (
	"context"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// Intent is the intent stage: it establishes what the change set out to do, so
// that the stages after it know what they are measuring against.
//
// # What it establishes, and why that is all
//
// PRD section 5 gives this stage two sources of intent. Supplied intent is
// authoritative acceptance criteria; inferred intent is a low-confidence hint,
// and the two are framed differently in every downstream prompt.
//
// Only the first exists in this build. The PRD's phase list defers
// transcript-based intent inference to phase 2, on the grounds that supplying
// intent explicitly is the mechanism that has to work first, so there is no
// transcript to read, nothing to summarize, and no hint to record. What this
// stage does is read the intent the run was started with, say what standing it
// has, and report that.
//
// A run carries three intent states rather than two, and this stage reports
// each of them differently. An intent supplied as acceptance criteria is
// authoritative. An intent offered without that claim is text the run carries
// and the stages after this one read, but it is a hint rather than a contract,
// so it is framed the way the PRD frames an inferred intent and for the same
// reason: a hint read as a requirement makes review flag choices the person
// made deliberately. A run with no intent text at all carries none, and only
// that case is reported as absent.
//
// So it starts no agent and touches no filesystem: it is a function of the
// run's state, which is what keeps the side effects of this pipeline at its
// edges. See the stage-2 note in this package's documentation for what the
// deferred half owes when it lands.
//
// # It writes nothing
//
// pipeline.NewState puts the supplied intent in pipeline.KeyIntent before the
// run starts, so there is nothing here to write: a run with intent already
// carries it, and a run without one carries the empty text rather than a
// sentence this stage invented. The declaration below says so, and a write
// this stage attempted anyway would fail the step.
//
// Nothing here can call an intent authoritative that a person did not supply,
// and that is structural rather than careful: pipeline.KeyIntentSupplied is a
// run input, so no stage may declare a write of it and a pipeline built from
// one that did is refused. internal/pipeline owns that refusal and tests it.
//
// # The guarantee this implementation owes
//
// PRD section 5 says the intent stage never blocks a run. Every finding it
// reports is a note, so nothing it finds holds the stage for a person and
// nothing it finds enters a fix round, and no path returns an error, so
// nothing it meets stops the run. That holds for the case where its own state
// read fails as well, which is the only way this stage can go wrong at all.
//
// internal/pipeline deliberately does not enforce this, and that is the right
// division. A stage node that could not hold would have to discard an ask
// finding to keep the promise, and a discarded ask is exactly what P3 exists
// to prevent. So the guarantee is this implementation's.
//
// The bound on it is behavioural, not structural: the report is built here, so
// a future edit could state an action other than note and nothing in the type
// system would object. The tests are what stand in for that.
// TestTheIntentStageNeverBlocksARun drives every path this stage has, and it
// uses an assertion that TestTheNeverBlocksAssertionRejectsAReportThatBlocks
// holds to a report that does block, so it cannot pass by checking nothing.
func Intent() pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{pipeline.KeyIntent, pipeline.KeyIntentSupplied},
		NewBody: func() pipeline.Body {
			return func(_ context.Context, in pipeline.Input) (pipeline.Output, error) {
				return establishIntent(in)
			}
		},
	}
}

// establishIntent is the stage body. It reports and never refuses: the one
// error it can meet becomes a note on a report the run carries forward.
func establishIntent(in pipeline.Input) (pipeline.Output, error) {
	intent, stated, err := readIntent(in.State)
	switch {
	case err != nil:
		// A read this stage declared cannot be refused by the reader, so this
		// is a defect in the declaration above rather than a run's input. It
		// is still reported as a note rather than returned as an error,
		// because the guarantee is unconditional and a stage that blocked on
		// its own defect would have broken it in the one case nobody tested.
		return intentReport(
			"The intent stage could not read the run's own intent, so this run carries no intent.",
			intentNote("intent-unreadable", findings.SeverityError, fmt.Sprintf(
				"Reading the run's recorded intent failed: %v. The run continues with no intent "+
					"recorded, so nothing after this stage has criteria to measure the change "+
					"against. This is a defect in the intent stage rather than in the change "+
					"being validated.", err))), nil

	case stated && intent != "":
		return intentReport(
			"The intent for this run was supplied, so it is authoritative acceptance criteria.",
			intentNote("intent-supplied", findings.SeverityInfo,
				"The intent was supplied rather than inferred, so it is authoritative "+
					"acceptance criteria for this change rather than a hint about it:\n\n"+
					intent)), nil

	case intent != "":
		return intentReport(
			"The intent for this run was offered as a hint rather than stated as acceptance criteria.",
			intentNote("intent-offered", findings.SeverityInfo,
				"The intent below was offered as a hint about this change rather than stated as "+
					"acceptance criteria for it, so the run carries it and the stages after this one "+
					"read it, but the change is not held to it. It is framed the way an inferred "+
					"intent is framed, and for the same reason: a hint read as a requirement makes "+
					"review flag choices the person made deliberately, so a change that departs "+
					"from it is not a defect for departing from it:\n\n"+intent)), nil

	default:
		return intentReport(
			"No intent text was recorded for this run, so it carries none.",
			intentNote("intent-not-supplied", findings.SeverityWarning,
				"No intent was recorded for this run, so nothing after this stage has criteria "+
					"to measure the change against and each judges it on what it does. "+
					"Supplying an intent, with the decisions and tradeoffs behind it, is what "+
					"gives them something to measure.")), nil
	}
}

// readIntent reads the run's intent text and the supplied bit standing over
// it, and returns them separately rather than as one verdict. The two are
// distinct facts and the stage draws three cases from them, so collapsing them
// here would leave an intent that was offered indistinguishable from no intent
// at all.
//
// The text is trimmed, which is what makes the supplied bit standing over an
// empty string report as no intent rather than as authoritative criteria that
// say nothing. pipeline.NewState already refuses to start such a run; the
// trimming is here as well because this stage's behaviour turns on it.
func readIntent(state pipeline.Reader) (intent string, stated bool, err error) {
	suppliedValue, err := state.Get(pipeline.KeyIntentSupplied)
	if err != nil {
		return "", false, err
	}
	intentValue, err := state.Get(pipeline.KeyIntent)
	if err != nil {
		return "", false, err
	}
	claimed, _ := suppliedValue.Bool()
	text, _ := intentValue.Text()
	return strings.TrimSpace(text), claimed, nil
}

// intentNote builds one of this stage's notes. Every finding this stage
// reports goes through here, and the action is fixed rather than a parameter,
// which is the narrowest the never-blocks guarantee can be made inside one
// package: a path that wanted to hold would have to stop using this
// constructor. The name is the stage's because this package is where all nine
// bodies go and a bare one would be claimed by the first of them.
func intentNote(id string, severity findings.Severity, description string) findings.Finding {
	return findings.Finding{
		ID:          id,
		Severity:    severity,
		Action:      findings.ActionNote,
		Description: description,
	}
}

// intentReport builds this stage's report.
func intentReport(summary string, found ...findings.Finding) pipeline.Output {
	return pipeline.Output{Report: findings.Report{Summary: summary, Findings: found}}
}
