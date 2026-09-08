// Package stages is where the nine delivery-gate stage bodies live.
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
// replacing that stage's call to Pending. The product needs nothing else: the
// pipeline is built from the same Stages value and the wiring is the same for
// all nine.
//
// A body that lands can also change where a run stops, so a test that named
// the stage it expected a run to hold at has to derive it instead - and from
// Holding, not from this table's complement, because having a body and
// holding are different questions: the test stage holds with a body wherever
// its configuration names no command. The deriving helpers in internal/cli
// and internal/service read Holding for exactly that reason.
//
// internal/journey is the exception, and deliberately so: it declares the
// stages without a body by name rather than deriving them, and that
// declaration is checked against Implemented in both directions. So a body
// landing here is red there until somebody writes it down. That package's
// stages.go says why deriving them would be worse.
//
// # Carried forward: the deferred half of the intent stage
//
// PRD section 5 gives the intent stage two sources of intent, and this build
// implements one. Supplied intent is read and reported; inferred intent is
// not, because the PRD's phase list defers transcript-based intent inference
// to phase 2 on the grounds that supplying intent explicitly is the mechanism
// that has to work first.
//
// No seam for it ships. An earlier draft of this package exported a transcript
// source, a recorder, and an options struct that nothing constructed, and they
// went for the reason Capabilities.Missing went: they answered to no consumer.
// Inference is deferred rather than specified, so nothing was queued to read
// them and nothing would have broken had they never arrived, which is what
// makes a declaration in that position a comment. Whoever builds inference
// adds the seams it actually uses.
//
// Surface landed ahead of consumers it names is the other case, and StageDeps
// is this package's one instance of it: deps.go names the bodies each of its
// fields answers to.
//
// What that work inherits is stated here so it is not rediscovered. The intent
// stage never blocks a run, and inference adds ways to fail that must not
// change it: transcripts that are missing, a source that cannot read them,
// material with nothing in it, a summarizer that fails or never answers, and a
// durable record that cannot be written are all reported and stepped past, not
// failed. Inferred intent is a low-confidence hint and never acceptance
// criteria, and nothing may promote it - pipeline.KeyIntentSupplied is a run
// input, so no stage may declare a write of it. Raw transcript text is never
// stored; only the derived summary, its source, and its score. The
// never-blocks tests in intent_test.go extend to cover each new path, and the
// assertion they use is one another test holds to a report that does block, so
// adding a path means adding a row rather than adding a second predicate.
//
// # Carried forward: who answers a hold
//
// internal/pipeline's outcome.go and doc.go describe every hold answer as
// coming from "a person", in roughly twelve lines between them. That became
// narrower than the truth when store.Resolver shipped: a hold may also be
// answered over the machine interface, under the authority PRD section 9 gives
// a caller of it, and store.Hold.ResolvedBy records which it was.
//
// The prose is not wrong yet, and nothing here makes it wrong: this package
// does not import internal/store, so no body it holds can resolve a stored
// hold, whatever that body reports. It stays true for as long as that is so,
// and internal/store's own documentation says nothing in production resolves
// one either.
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
	"github.com/dayamjz/assistant/internal/config"
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
// terms as pipeline.Implementation.NewBody. It takes the build-scoped
// dependencies for the same reason: they are settled once, when the service
// has resolved an agent, and one pipeline then serves every run. Anything that
// varies per run is a declared state key instead, because a value captured
// here would be the same value for every run of this service.
var written = map[pipeline.Stage]func(StageDeps) pipeline.Implementation{
	pipeline.StageIntent: Intent,
	pipeline.StageReview: Review,
	pipeline.StageTest:   Test,
	pipeline.StagePR:     PullRequest,
}

// All returns the nine stages as this build has them: each stage's own body
// where one is written, and Pending everywhere else.
//
// It is what wires the binary, so what a run actually walks is this value and
// not a set assembled somewhere else. The nine fields are named here because
// pipeline.Stages is nine named fields on purpose, per P2: there is no list to
// index and no order to get wrong.
func All(deps StageDeps) pipeline.Stages {
	return pipeline.Stages{
		Intent:   implementation(pipeline.StageIntent, deps),
		Rebase:   implementation(pipeline.StageRebase, deps),
		Review:   implementation(pipeline.StageReview, deps),
		Test:     implementation(pipeline.StageTest, deps),
		Document: implementation(pipeline.StageDocument, deps),
		Lint:     implementation(pipeline.StageLint, deps),
		Push:     implementation(pipeline.StagePush, deps),
		PR:       implementation(pipeline.StagePR, deps),
		CI:       implementation(pipeline.StageCI, deps),
	}
}

// implementation returns the body written for a stage, or Pending when this
// build has none.
func implementation(stage pipeline.Stage, deps StageDeps) pipeline.Implementation {
	if newImplementation, ok := written[stage]; ok {
		return newImplementation(deps)
	}
	return Pending(stage.String())
}

// Implemented returns the stages this build has a body for, in the order a run
// takes them. It reads the written table, so it reports what All places rather
// than a count stated beside it, and it names no stage this build has not
// written.
//
// assistant doctor reports it, which is what lets that command answer
// decisively rather than by implication: how much of the gate a build can
// actually validate is the set this returns, and saying which stages those are
// is more use than reporting every dependency as present.
//
// It answers which stages have a body and nothing more. Where a run stops is
// a different fact, because a bodied stage can still hold; Holding owns that
// one, and a test deriving a run's walk reads it rather than this.
func Implemented() []pipeline.Stage {
	var out []pipeline.Stage
	for _, stage := range pipeline.Order() {
		if _, ok := written[stage]; ok {
			out = append(out, stage)
		}
	}
	return out
}

// holdsByConfiguration is the bodied stages whose resolved configuration alone
// already decides a hold, before the body reads the run or the change: each
// row is the rule that decides it, and each rule is the same read the body
// itself answers a run with, so the prediction and the body cannot drift
// apart. A body lands a row here when P3 makes it hold on a fact the
// configuration settles in advance; a hold that depends on the change or on
// an agent's answer has no row, because nothing here can see it.
var holdsByConfiguration = map[pipeline.Stage]func(config.Config) bool{
	pipeline.StageTest: func(cfg config.Config) bool {
		_, configured := configuredTestCommand(cfg)
		return !configured
	},
}

// Holding returns the stages a run holds at under cfg regardless of the change
// under validation, in the order a run takes them.
//
// Two kinds of stage are in it: a stage with no body, whose Pending
// placeholder reports one ask finding and holds; and a bodied stage whose row
// in holdsByConfiguration says cfg already decides a hold - today the test
// stage wherever cfg names no test command. What it deliberately leaves out is
// every hold that depends on the change or on what an agent answers, such as a
// configured check that fails, so absence from this list means a stage does
// not hold unconditionally, never that it cannot hold. A caller that skips a
// stage subtracts that skip itself: a skip is a per-run input this package
// does not see.
//
// cfg is a parameter rather than read here, because which configuration
// applies is the caller's fact: the service resolves one per service and a
// test resolves its own, and a predicate that read a file or a default to find
// out would be guessing at the one fact that changes its answer. Pass the
// configuration the runs in question resolve.
func Holding(cfg config.Config) []pipeline.Stage {
	var holding []pipeline.Stage
	for _, stage := range pipeline.Order() {
		if _, hasBody := written[stage]; !hasBody {
			holding = append(holding, stage)
		} else if holds, ok := holdsByConfiguration[stage]; ok && holds(cfg) {
			holding = append(holding, stage)
		}
	}
	return holding
}

// PendingFixer is the fixer for a build that has none. internal/pipeline
// requires one whenever any stage's fix round limit is above zero, so a build
// that keeps the configured limits has to supply something even before any
// stage can produce a finding for it.
//
// Its body refuses. A fix round is an agent editing the change and a stage
// re-running to verify it; the review stage is the second half and nothing
// here is the first, so the honest answer is a failure naming what is missing.
// A body that returned a summary and changed nothing would leave the stage
// reporting the same findings, the loop converging, and the run parked with a
// reason that named the bound rather than the missing fixer.
//
// Reachable is what it is written for, and the review stage is what reaches
// it. A fix node exists only for a stage whose row in internal/pipeline's
// stage table declares a fix round limit above zero, because the pipeline
// builds one for no other; and only a stage with a body can report the
// fix-eligible finding that routes into one, because a stage without a body
// reports an ask finding, which never enters a fix loop. The intent stage has
// neither half: its row declares no rounds, so it has no fix node at all, and
// it reports only notes besides. The review stage has both, so a fix-eligible
// review finding meets this failure rather than a silent pass.
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
