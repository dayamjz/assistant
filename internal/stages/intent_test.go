package stages_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
)

// blocked reports why a stage report would block a run, or "" when it would
// not. It is the whole of the never-blocks assertion, written as a predicate
// rather than as assertions inside a test so that the test below can check it
// rejects a blocking report as well as accepting the ones the intent stage
// produces. An assertion nothing ever fails is one that stops being evidence
// the day the thing it guards changes.
//
// It normalizes first, because that is what the stage node does and it is
// where P3 lands: a finding whose action is missing, empty, or a word nobody
// recognizes becomes ask there and holds the stage. Asking Holds of an
// un-normalized report would answer false for exactly that finding, which is
// the shape a future edit to the intent stage is most likely to produce and
// the one this predicate most has to catch.
func blocked(report findings.Report) string {
	report = report.Normalize()
	if err := report.Validate(); err != nil {
		return "the report is one the pipeline refuses: " + err.Error()
	}
	if held := report.Held(); len(held) != 0 {
		return "it holds for a person at finding " + held[0].ID
	}
	if fixable := report.Fixable(); len(fixable) != 0 {
		return "it sends the run to a fixer at finding " + fixable[0].ID
	}
	if !report.AllNotes() {
		return "it reports a finding that is not a note"
	}
	return ""
}

// The predicate above has to reject a blocking report, or every use of it
// below is a test that cannot fail. The cases are the ways the intent stage
// could come to block: it states ask, it states fix, or it states nothing and
// P3 resolves that to ask on its behalf.
//
// The last two are why blocked normalizes. A finding with no action reports
// Holds false as it stands and ask once the stage node has normalized it, so a
// predicate that skipped normalizing would accept the one edit most likely to
// break this stage by accident.
func TestTheNeverBlocksAssertionRejectsAReportThatBlocks(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)

	for _, c := range []struct {
		name    string
		finding findings.Finding
	}{
		{"an ask finding", findings.Finding{
			ID: "asks", Action: findings.ActionAsk, Description: "this needs a decision"}},
		{"a fix finding", findings.Finding{
			ID: "fixes", Action: findings.ActionFix, Description: "this is mechanically wrong"}},
		{"a finding with no action at all", findings.Finding{
			ID: "unclassified", Description: "this says nothing about who resolves it"}},
		{"a finding whose action is a word nobody recognizes", findings.Finding{
			ID: "unreadable", Action: "autofix", Description: "this action is not one of the three"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			report := findings.Report{Summary: "a stage that blocks", Findings: []findings.Finding{c.finding}}
			if blocked(report) == "" {
				t.Fatalf("the never-blocks assertion accepted a report carrying %+v, so every "+
					"use of it below would pass whatever the intent stage did", c.finding)
			}
		})
	}
}

// PRD section 5: the intent stage never blocks a run. internal/pipeline
// deliberately leaves that to the implementation, because a stage node that
// could not hold would have to discard an ask finding and a discarded ask is
// what P3 exists to prevent, so this is where the guarantee is established.
//
// Every path the stage has is run, the one no run can reach included, and each
// is checked with the predicate above, which the test before this one shows
// rejects a blocking report.
func TestTheIntentStageNeverBlocksARun(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)

	for _, c := range intentPaths() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, err := runIntentWith(t, c)
			if err != nil {
				t.Fatalf("the intent stage returned an error, which stops the run: %v", err)
			}
			if why := blocked(out.Report); why != "" {
				t.Fatalf("the intent stage blocked the run: %s\nreport: %+v", why, out.Report)
			}
			if len(out.Writes) != 0 {
				t.Fatalf("the intent stage asked to write %+v, and it declares no writes", out.Writes)
			}
		})
	}
}

// The guarantee is about a run and not only about a report, so it is also
// checked by running one. The intent stage goes into a real pipeline and the
// run is executed: it has to walk past intent rather than halt at its hold.
//
// This is the half a report-shaped assertion cannot make. A stage that
// returned an error, wrote a key it had not declared, or produced a report the
// pipeline refuses would block a run without ever reporting a finding that
// held, and every one of those ends the run here instead of passing the stage.
func TestARunWalksPastTheIntentStage(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)

	for _, c := range intentPaths() {
		if c.start == nil {
			continue // pipeline.NewState refuses this start; see intentPaths.
		}
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			result := runPipeline(t, stages.Intent(), *c.start)
			if got := pipeline.StageOutcome(result.State, pipeline.StageIntent); got != pipeline.OutcomePassed {
				t.Fatalf("the intent stage came to %s, want passed: a run does not walk past it", got)
			}
			if result.Status != graph.StatusCompleted {
				t.Fatalf("the run came to %s rather than completing, so the intent stage stopped it", result.Status)
			}
		})
	}
}

// And the run above has to be able to notice a stage that does block, or it
// reports the same result whatever the intent stage does. The stage swapped in
// here holds, which is what an intent stage reporting an ask finding would do,
// and the run must halt at its hold rather than complete.
func TestTheWalkPastAssertionNoticesAnIntentStageThatBlocks(t *testing.T) {
	t.Parallel()

	blocking := pipeline.Constant("an intent stage that holds", findings.Finding{
		ID:          "intent-blocks",
		Severity:    findings.SeverityWarning,
		Action:      findings.ActionAsk,
		Description: "this intent stage stops the run for a person",
	})
	result := runPipeline(t, blocking, pipeline.Start{
		Branch: "topic", Base: "main", Submitted: "9f2c1ab",
	})
	if got := pipeline.StageOutcome(result.State, pipeline.StageIntent); got != pipeline.OutcomeHeld {
		t.Fatalf("an intent stage reporting an ask finding came to %s, want held; the run above "+
			"cannot tell a stage that blocks from one that does not", got)
	}
	if result.Status == graph.StatusCompleted {
		t.Fatal("the run completed past an intent stage that holds, so completing proves nothing")
	}
}

// A run carries three intent states, not two: intent supplied as acceptance
// criteria, intent offered without that claim, and no intent at all. The stage
// has to report all three apart, so the distinction PRD section 5 frames
// downstream prompts on is one it actually draws rather than one it records
// the same way twice.
//
// The offered case is the one this most has to pin. pipeline.KeyIntent holds
// the offered text and the stages after this one read it, so a report saying
// the run carries no intent would be false about state the run is carrying.
// It must carry the text, as the supplied case does, and it must not claim the
// standing the supplied case has.
func TestTheThreeIntentStatesAreReportedApart(t *testing.T) {
	t.Parallel()

	const criteria = "add a greeting, and keep the existing one"
	const hint = "somewhere around the greeting, probably"
	withIntent, err := runIntent(t, map[pipeline.Key]graph.Value{
		pipeline.KeyIntent:         graph.TextValue(criteria),
		pipeline.KeyIntentSupplied: graph.BoolValue(true),
	})
	if err != nil {
		t.Fatalf("running the intent stage over a supplied intent: %v", err)
	}
	offeredRun, err := runIntent(t, map[pipeline.Key]graph.Value{
		pipeline.KeyIntent: graph.TextValue(hint),
	})
	if err != nil {
		t.Fatalf("running the intent stage over an offered intent: %v", err)
	}
	without, err := runIntent(t, nil)
	if err != nil {
		t.Fatalf("running the intent stage over no intent: %v", err)
	}

	supplied := only(t, withIntent.Report)
	offered := only(t, offeredRun.Report)
	absent := only(t, without.Report)
	byID := map[string]string{}
	for _, state := range []struct {
		name  string
		found findings.Finding
	}{{"a supplied intent", supplied}, {"an offered intent", offered}, {"no intent", absent}} {
		if other, clash := byID[state.found.ID]; clash {
			t.Fatalf("%s and %s are both reported as %s", other, state.name, state.found.ID)
		}
		byID[state.found.ID] = state.name
	}

	if !strings.Contains(supplied.Description, "authoritative") {
		t.Fatalf("a supplied intent is not reported as authoritative: %s", supplied.Description)
	}
	if !strings.Contains(supplied.Description, criteria) {
		t.Fatalf("the report of a supplied intent does not carry it: %s", supplied.Description)
	}
	if strings.Contains(absent.Description, "authoritative") {
		t.Fatalf("no intent at all is reported as authoritative: %s", absent.Description)
	}

	if !strings.Contains(offered.Description, hint) {
		t.Fatalf("the report of an offered intent does not carry it, so a reader has to look it up: %s",
			offered.Description)
	}
	if strings.Contains(offered.Description, "authoritative") {
		t.Fatalf("an offered intent is reported with the standing of a supplied one: %s", offered.Description)
	}
	if !strings.Contains(offered.Description, "hint") {
		t.Fatalf("an offered intent is not framed as the low-confidence hint PRD section 5 makes it: %s",
			offered.Description)
	}
	for _, claim := range []string{"carries none", "No intent", "no intent was", "nothing was given"} {
		for _, text := range []string{offered.Description, offeredRun.Report.Summary} {
			if strings.Contains(text, claim) {
				t.Fatalf("an offered intent is reported as if the run carried none (%q), but "+
					"pipeline.KeyIntent holds it and the stages after this one read it: %s", claim, text)
			}
		}
	}
}

// A supplied-intent bit standing over no text is not a supplied intent.
// pipeline.NewState refuses to start such a run, so this is the shape a resume
// or a hand-built state could present, and reporting it as authoritative
// acceptance criteria would be reporting criteria that say nothing.
func TestTheSuppliedBitOverNoTextIsNotAnAuthoritativeIntent(t *testing.T) {
	t.Parallel()

	out, err := runIntent(t, map[pipeline.Key]graph.Value{
		pipeline.KeyIntent:         graph.TextValue("   "),
		pipeline.KeyIntentSupplied: graph.BoolValue(true),
	})
	if err != nil {
		t.Fatalf("running the intent stage: %v", err)
	}
	if found := only(t, out.Report); strings.Contains(found.Description, "authoritative") {
		t.Fatalf("a supplied-intent bit over blank text was reported as authoritative: %s", found.Description)
	}
}

// intentPath is one way through the intent stage: the state it reads, and the
// run that reaches it.
type intentPath struct {
	name  string
	state map[pipeline.Key]graph.Value
	// start is the run that reaches this path, or nil for a state
	// pipeline.NewState refuses to start a run from, which is the only way a
	// path here can be one no run reaches. The pipeline-level test skips
	// those; the report-level one does not, because they are exactly the
	// paths nothing else exercises.
	start *pipeline.Start
}

// intentPaths is every path the intent stage has. Adding a path to the stage
// means adding a row here.
func intentPaths() []intentPath {
	run := func(intent string, supplied bool) *pipeline.Start {
		return &pipeline.Start{
			Branch: "topic", Base: "main", Submitted: "9f2c1ab",
			Intent: intent, IntentSupplied: supplied,
		}
	}
	return []intentPath{
		{
			name: "a supplied intent",
			state: map[pipeline.Key]graph.Value{
				pipeline.KeyIntent:         graph.TextValue("add a greeting"),
				pipeline.KeyIntentSupplied: graph.BoolValue(true),
			},
			start: run("add a greeting", true),
		},
		{
			name:  "no intent at all",
			start: run("", false),
		},
		{
			// assistant run --intent "..." --intent-supplied=false, which
			// internal/service records as an offered intent.
			name: "an intent offered rather than supplied",
			state: map[pipeline.Key]graph.Value{
				pipeline.KeyIntent: graph.TextValue("something already in state"),
			},
			start: run("something already in state", false),
		},
		{
			name: "the supplied bit standing over no text",
			state: map[pipeline.Key]graph.Value{
				pipeline.KeyIntent:         graph.TextValue("   "),
				pipeline.KeyIntentSupplied: graph.BoolValue(true),
			},
			// pipeline.NewState refuses this start, so no run reaches it.
		},
	}
}

// runIntent runs the intent stage's body over the state it declares, through
// the same restriction the stage node applies: a read the implementation did
// not declare is refused here as it would be there.
func runIntent(t *testing.T, state map[pipeline.Key]graph.Value) (pipeline.Output, error) {
	t.Helper()
	return runIntentWith(t, intentPath{state: state})
}

// runIntentWith runs the intent stage's body over one path.
func runIntentWith(t *testing.T, path intentPath) (pipeline.Output, error) {
	t.Helper()
	impl := stages.Intent()
	allowed := make(map[pipeline.Key]bool, len(impl.Reads))
	for _, key := range impl.Reads {
		allowed[key] = true
	}
	return impl.NewBody()(t.Context(), pipeline.Input{
		Stage: pipeline.StageIntent,
		State: declaredReader{allowed: allowed, state: path.state},
	})
}

// runPipeline builds a pipeline whose intent stage is the one given and whose
// every other stage reports nothing, and runs it to a halt or to the end. The
// stages around it come from pipeline.ConstantStages, so nothing here says how
// many there are.
func runPipeline(t *testing.T, intent pipeline.Implementation, start pipeline.Start) graph.Result {
	t.Helper()
	all := pipeline.ConstantStages("nothing to report")
	all.Intent = intent
	p, err := pipeline.New(pipeline.Options{
		Stages: all,
		Rounds: config.FixRounds{},
		Budget: config.DefaultRunBudget,
	})
	if err != nil {
		t.Fatalf("building a pipeline around the intent stage: %v", err)
	}
	exec, err := p.Executor(graph.NewMemoryStore())
	if err != nil {
		t.Fatalf("building an executor: %v", err)
	}
	state, err := p.NewState(start)
	if err != nil {
		t.Fatalf("building the run's initial state: %v", err)
	}
	result, err := exec.Run(t.Context(), "intent-test", state)
	if err != nil {
		t.Fatalf("running the pipeline: %v", err)
	}
	return result
}

// declaredReader is the pipeline.Reader a stage body is given: it answers the
// keys the implementation declared and refuses the rest, so a test cannot read
// a key the real stage node would not have handed over.
//
// It has no way to fail a declared read, and that is the point. The real
// reader records every refusal it hands back and the node adapter takes the
// recorded error whatever the body returned, so a fake that failed a read
// without that consequence would state a shape the mechanism cannot produce
// and would let a body look like it had recovered from something no body can.
type declaredReader struct {
	allowed map[pipeline.Key]bool
	state   map[pipeline.Key]graph.Value
}

// Get implements pipeline.Reader.
func (r declaredReader) Get(key pipeline.Key) (graph.Value, error) {
	if !r.allowed[key] {
		return graph.Value{}, errors.New("the intent stage did not declare a read of " + string(key))
	}
	if value, ok := r.state[key]; ok {
		return value, nil
	}
	// A declared key a run has not written holds the zero value of its kind,
	// which for the two this stage reads is false and the empty text.
	if key == pipeline.KeyIntentSupplied {
		return graph.BoolValue(false), nil
	}
	return graph.TextValue(""), nil
}

// only returns the report's one finding, failing when it reported another
// number of them.
func only(t *testing.T, report findings.Report) findings.Finding {
	t.Helper()
	normalized := report.Normalize()
	if len(normalized.Findings) != 1 {
		t.Fatalf("the intent stage reported %d findings, want 1: %+v",
			len(normalized.Findings), normalized.Findings)
	}
	return normalized.Findings[0]
}
