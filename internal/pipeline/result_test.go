package pipeline

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// A stage that declares another stage's keys reads back what that stage left:
// that it ran, what became of it, the report it recorded, and what its fixer
// wrote. This is the whole of what the pull request stage narrates a run from.
//
// It is driven through a real run rather than over a state built by hand, so
// what it reads is what the stage node actually recorded, encoded the way that
// node encodes it.
func TestReadStageResultReadsWhatAStageLeftBehind(t *testing.T) {
	t.Parallel()
	const fixed = "rewrote the offending line"
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, recording(c, nil, nil, func(in Input, call int) (Output, error) {
		if call == 1 {
			return Output{Report: reportWith(findings.ActionFix, "this is mechanically wrong")}, nil
		}
		return Output{Report: findings.Report{
			Summary: "the lint stage is clean now",
			Risk:    findings.RiskLow,
			Tested:  []string{"ran the linter"},
		}}, nil
	}))

	var read StageResult
	var readErr error
	set(&all, StagePush, recording(c, StageResultKeys(StageLint), nil, func(in Input, _ int) (Output, error) {
		read, readErr = ReadStageResult(in.State, StageLint)
		return Output{Report: passing()}, nil
	}))

	p := build(t, Options{
		Stages: all,
		Fixer: recordingFixer(c, nil, []Key{KeyHead}, func(in FixInput, _ int) (FixOutput, error) {
			return FixOutput{Summary: fixed, Writes: map[Key]graph.Value{KeyHead: graph.TextValue("c1")}}, nil
		}),
		Rounds: rounds(1),
		Budget: 200,
	})
	if _, result := start(t, p, complete()); result.Position != "" {
		t.Fatalf("the run stopped at %q rather than running to the end", result.Position)
	}

	if readErr != nil {
		t.Fatalf("reading the lint stage's result: %v", readErr)
	}
	if read.Stage != StageLint {
		t.Fatalf("the result names the %s stage", read.Stage)
	}
	if !read.Ran {
		t.Fatal("the lint stage ran twice and its result reports it did not run")
	}
	if read.Outcome != OutcomePassed {
		t.Fatalf("the lint stage ended %s and its result reports %s", OutcomePassed, read.Outcome)
	}
	if read.Report.Summary != "the lint stage is clean now" {
		t.Fatalf("the result carries the report %q, and the stage recorded the one from its last round",
			read.Report.Summary)
	}
	if read.Report.Risk != findings.RiskLow || len(read.Report.Tested) != 1 {
		t.Fatalf("the result lost part of the recorded report: %+v", read.Report)
	}
	if read.Fix != fixed {
		t.Fatalf("the result carries the fix summary %q, and the fixer wrote %q", read.Fix, fixed)
	}
}

// A stage that never ran is not a stage that found nothing, and a result says
// which. Both are read out of one run, so nothing here rests on a state
// assembled by hand.
func TestReadStageResultTellsAStageThatDidNotRunFromOneThatFoundNothing(t *testing.T) {
	t.Parallel()
	c := newCalls()
	all := recordingStages(c)

	var skipped, ran StageResult
	var errs []error
	set(&all, StagePush, recording(c,
		append(StageResultKeys(StageLint), StageResultKeys(StageIntent)...), nil,
		func(in Input, _ int) (Output, error) {
			var err error
			skipped, err = ReadStageResult(in.State, StageLint)
			errs = append(errs, err)
			ran, err = ReadStageResult(in.State, StageIntent)
			errs = append(errs, err)
			return Output{Report: passing()}, nil
		}))

	p := build(t, Options{Stages: all, Rounds: rounds(0), Budget: 200})
	begin := complete()
	begin.Skip = []Stage{StageLint}
	if _, result := start(t, p, begin); result.Position != "" {
		t.Fatalf("the run stopped at %q rather than running to the end", result.Position)
	}

	for _, err := range errs {
		if err != nil {
			t.Fatalf("reading a stage's result: %v", err)
		}
	}
	if skipped.Ran {
		t.Fatal("the lint stage was skipped and its result reports it ran")
	}
	if skipped.Outcome != OutcomeSkipped {
		t.Fatalf("the skipped lint stage reports %s", skipped.Outcome)
	}
	if !ran.Ran {
		t.Fatal("the intent stage ran and its result reports it did not")
	}
	if ran.Report.Summary != passingSummary {
		t.Fatalf("the intent stage's result carries %q", ran.Report.Summary)
	}
}

// The keys are the declaration a caller makes, so a caller that declares them
// may read a result and one that does not may not. The second half is what
// makes the first mean anything: a reader that answered whatever it was asked
// would let a stage narrate keys it never declared.
func TestReadStageResultIsRefusedWithoutTheDeclaration(t *testing.T) {
	t.Parallel()
	c := newCalls()
	all := recordingStages(c)

	var refusal error
	set(&all, StagePush, recording(c, nil, nil, func(in Input, _ int) (Output, error) {
		_, refusal = ReadStageResult(in.State, StageLint)
		return Output{Report: passing()}, nil
	}))

	p := build(t, Options{Stages: all, Rounds: rounds(0), Budget: 200})
	exec, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	state, err := p.NewState(complete())
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if _, err := exec.Run(t.Context(), "run", state); err == nil {
		t.Fatal("a stage read a result it declared no keys for and the run carried on")
	}
	if refusal == nil {
		t.Fatal("reading a result without declaring its keys was allowed")
	}
	if !strings.Contains(refusal.Error(), string(StageLint.ReportKey())) {
		t.Fatalf("the refusal %q does not name the key that was not declared", refusal)
	}
}

// A stage that takes automatic fix rounds has a fix key and a stage that does
// not has none, so the keys a caller declares differ by stage. Declaring one
// the schema does not hold is refused when the pipeline is built, which is
// what keeps this from being a convention.
func TestStageResultKeysCoverOnlyWhatTheSchemaHolds(t *testing.T) {
	t.Parallel()
	for _, stage := range Order() {
		for _, key := range StageResultKeys(stage) {
			if _, ok := schema[key]; !ok {
				t.Errorf("StageResultKeys names %q for the %s stage, which the schema does not hold",
					key, stage)
			}
		}
	}

	c := newCalls()
	all := recordingStages(c)
	// The intent stage takes no fix rounds, so its fix key is not in the
	// schema and a stage declaring it is refused rather than reading nothing.
	set(&all, StagePush, recording(c, []Key{StageIntent.FixKey()}, nil, nil))
	if _, err := New(Options{Stages: all, Rounds: rounds(0), Budget: 200}); err == nil {
		t.Fatal("a stage declared a fix key for a stage that takes no fix rounds and the pipeline built")
	}
}
