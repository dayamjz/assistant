// Package stages is where the nine delivery-gate stage bodies live. The intent
// stage is written; the rest are not.
//
// PRD section 5 specifies the nine, internal/pipeline wires them into a graph
// and owns their order, and each body is separate work against
// pipeline.Implementation. Until a body exists, the field it will occupy has
// to hold something, because pipeline.Stages is nine required fields and a
// pipeline with a missing one does not build.
//
// What an unwritten stage holds instead is Pending: a stage that validates
// nothing and says so. Which stages have a body is the written table below,
// and Implemented reports it, so no reader has to count.
//
// # Why a placeholder rather than a refusal to build the pipeline
//
// A binary that refuses to start any run until all nine bodies exist cannot be
// driven, tested end to end, or grown one stage at a time. A binary whose
// unwritten stages hand the decision to a person can be driven today and gets
// stricter as each body lands, which is what the end-to-end harness needs.
//
// The alternative that is not on the table is a placeholder that passes. A
// stage reporting a pass it did not establish is the failure the whole gate
// exists to prevent, and it would make "it passed the gate" mean nothing in
// exactly the builds where it means least.
//
// # What a pending stage does
//
// It reads nothing, writes nothing, and reports one finding whose action is
// ask. PRD principle P3 makes an ask finding hold the stage for a person, so a
// run reaching a stage with no body stops there and waits, whatever the fix
// round limits say: internal/pipeline never lets an ask finding into a fix
// round. The person's answer is one of the hold's own options, so approving
// carries the run past a stage that looked at nothing, which is the honest
// shape of the situation and is recorded as such in the stage's report.
//
// # Replacing one
//
// A body that lands is added to this package and named in the row below,
// replacing that stage's call to Pending. Nothing else changes: the pipeline
// is built from the same Stages value and the wiring is the same for all nine.
//
// A body that lands also changes where a run first stops, so a test that named
// the stage it expected a run to hold at has to derive it instead. The ones in
// internal/cli and internal/service read Implemented and take the first stage
// without a body, which is what a run actually walks to.
//
// # Carried forward: who answers a hold
//
// internal/pipeline's outcome.go and doc.go describe every hold answer as
// coming from "a person", in roughly twelve lines between them. That became
// narrower than the truth when store.Resolver shipped: a hold may also be
// answered over the machine interface, under the authority PRD section 9 gives
// a caller of it, and store.Hold.ResolvedBy records which it was.
//
// The prose is not wrong yet, and this stage does not make it wrong. It is
// true for as long as no stage body connects a graph halt to a stored hold,
// and internal/store's own documentation says nothing in production resolves
// one. The intent stage cannot be the body that changes that, because it never
// holds: it reports notes and nothing else, so it has no halt to resolve.
//
// Whichever stage body first resolves a store hold owns correcting those lines
// so the halt description says what actually answers it. This note is here
// rather than in a task list because this is the file the next stage body is
// added to, and a prose claim that was true when written and false after a
// later change is how this repository has lost review rounds before.
package stages

import (
	"context"
	"fmt"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// Pending is the implementation of a stage whose body has not been written. It
// reports one ask finding, so the stage holds for a person rather than
// reporting a pass it did not establish.
//
// name is the stage it stands in for, and it appears in the summary and in the
// finding, so a person reading the hold is told which stage stopped and why
// rather than being handed a report they have to interpret.
func Pending(name string) pipeline.Implementation {
	return pipeline.Constant(
		fmt.Sprintf("The %s stage has no implementation in this build, so nothing was checked.", name),
		findings.Finding{
			ID:       "pending-" + name,
			Severity: findings.SeverityWarning,
			Action:   findings.ActionAsk,
			Description: fmt.Sprintf(
				"The %s stage is not implemented in this build. It read nothing, checked nothing, "+
					"and established nothing about this change. Approving carries the run past a stage "+
					"that did not run; skipping records it as skipped; cancelling ends the run.", name),
		},
	)
}

// written is the one owner of which stages this build has a body for: a stage
// with an entry uses it, and a stage without one gets Pending. Adding a body
// is adding an entry rather than editing All.
//
// The value is a constructor rather than an Implementation so that a body
// holding anything per-build is constructed when the pipeline is, on the same
// terms as pipeline.Implementation.NewBody.
//
// The intent stage is built from the zero IntentOptions, which records a
// supplied intent and infers nothing. Its seams are per-build values and this
// build supplies none; IntentTranscripts says why that is the deliberate
// answer today rather than a wiring oversight.
var written = map[pipeline.Stage]func() pipeline.Implementation{
	pipeline.StageIntent: func() pipeline.Implementation { return Intent(IntentOptions{}) },
}

// All returns the nine stages as this build has them: each stage's own body
// where one is written, and Pending everywhere else.
//
// It is what wires the binary, so what a run actually walks is this value and
// not a set assembled somewhere else. The nine fields are named here because
// pipeline.Stages is nine named fields on purpose, per P2: there is no list to
// index and no order to get wrong.
func All() pipeline.Stages {
	return pipeline.Stages{
		Intent:   implementation(pipeline.StageIntent),
		Rebase:   implementation(pipeline.StageRebase),
		Review:   implementation(pipeline.StageReview),
		Test:     implementation(pipeline.StageTest),
		Document: implementation(pipeline.StageDocument),
		Lint:     implementation(pipeline.StageLint),
		Push:     implementation(pipeline.StagePush),
		PR:       implementation(pipeline.StagePR),
		CI:       implementation(pipeline.StageCI),
	}
}

// implementation returns the body written for a stage, or Pending when this
// build has none.
func implementation(stage pipeline.Stage) pipeline.Implementation {
	if newImplementation, ok := written[stage]; ok {
		return newImplementation()
	}
	return Pending(stage.String())
}

// Implemented returns the stages this build has a body for, in the order a run
// takes them. It is empty today.
//
// assistant doctor reports it, which is what lets that command answer
// decisively rather than by implication: a build with no stage bodies cannot
// validate a change, and saying so is more use than reporting every dependency
// as present.
func Implemented() []pipeline.Stage {
	var out []pipeline.Stage
	for _, stage := range pipeline.Order() {
		if _, ok := written[stage]; ok {
			out = append(out, stage)
		}
	}
	return out
}

// PendingFixer is the fixer for a build that has none. internal/pipeline
// requires one whenever any stage's fix round limit is above zero, so a build
// that keeps the configured limits has to supply something even before any
// stage can produce a finding for it.
//
// Its body refuses. A fix round is an agent editing the change and a stage
// re-running to verify it, and neither exists yet, so the honest answer is a
// failure naming what is missing. A body that returned a summary and changed
// nothing would leave the stage reporting the same findings, the loop
// converging, and the run parked with a reason that named the bound rather
// than the missing fixer.
//
// It is unreachable in this build, and reachable is what it is written for. A
// stage with no body reports an ask finding, which never enters a fix loop, so
// nothing today can reach a fix node. The first stage body that reports a
// fix-eligible finding does, and it meets this rather than a silent pass.
//
// requires is what the run's fixer path needs of the agent adapter, which
// internal/runs answers with Service.FixerRequires. It is a parameter rather
// than a constant here because whether a run keeps a durable fixer session is
// the service's configuration, not this package's.
func PendingFixer(requires []agents.Capability) pipeline.Fixer {
	return pipeline.Fixer{
		Requires: requires,
		NewBody: func() pipeline.FixBody {
			return func(_ context.Context, in pipeline.FixInput) (pipeline.FixOutput, error) {
				return pipeline.FixOutput{}, fmt.Errorf(
					"no fixer is implemented in this build, so the %d fix-eligible finding(s) the %s stage reported cannot be applied",
					len(in.Findings), in.Stage)
			}
		},
	}
}
