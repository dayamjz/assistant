package stages_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
)

// TestMain lets this binary act as the scripted agent the intent stage's
// inference paths are driven against.
func TestMain(m *testing.M) {
	standin.Main()
	os.Exit(m.Run())
}

// blocked reports why a stage report would block a run, or "" when it would
// not. It is the whole of the never-blocks assertion, written as a predicate
// rather than as assertions inside a test so that the tests below can check it
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
// below is a test that cannot fail. The three cases are the three ways the
// intent stage could come to block: it states ask, it states fix, or it states
// nothing and P3 resolves that to ask on its behalf.
//
// The third is why blocked normalizes. A finding with no action reports Holds
// false as it stands and ask once the stage node has normalized it, so a
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
// Every path the stage has is run, not the ones a working build takes: the
// intent nobody supplied, the transcripts that are not there, the source that
// could not read them, the material with nothing in it, the summarizer that
// failed, the one that never answered, the one that answered with nothing, and
// the recorder that could not write. Each is checked with the predicate above,
// which the test before this one shows rejects a blocking report.
func TestTheIntentStageNeverBlocksARun(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)

	for _, c := range intentPaths(t) {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, err := runIntent(t, c.options, c.state)
			if err != nil {
				t.Fatalf("the intent stage returned an error, which stops the run: %v", err)
			}
			if why := blocked(out.Report); why != "" {
				t.Fatalf("the intent stage blocked the run: %s\nreport: %+v", why, out.Report)
			}
		})
	}
}

// The guarantee is about a run and not only about a report, so it is also
// checked by running one. The intent stage is put in a real pipeline and the
// run is executed: it has to walk past intent rather than halt at its hold.
//
// This is the half a report-shaped assertion cannot make. A stage that
// returned an error, wrote a key it had not declared, or produced a report the
// pipeline refuses would block a run without ever reporting a finding that
// held, and every one of those ends the run here instead of passing the stage.
func TestARunWalksPastTheIntentStageOnEveryPathItHas(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)

	for _, c := range intentPaths(t) {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			result := runPipeline(t, stages.Intent(c.options), c.start)
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
// here holds, which is what an intent stage that reported an ask finding would
// do, and the run must halt at its hold rather than complete.
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

// Supplied intent is authoritative acceptance criteria and inference is not
// run over it: PRD section 5 frames the two differently downstream precisely
// because one is criteria and the other is a hint, and a stage that inferred
// over a stated intent could replace the first with the second.
//
// The evidence is the agent: a stand-in scripted to answer any invocation is
// supplied, and the run has to leave it unasked.
func TestASuppliedIntentIsAuthoritativeAndIsNeverInferredOver(t *testing.T) {
	t.Parallel()

	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Times: standin.Always,
		Reply: standin.Text("an intent this stage should never have asked for"),
	}}})
	source := &transcripts{material: stages.IntentMaterial{
		Text: "the worker was asked to do something else entirely", Available: 55,
	}}
	recorder := &recorder{}

	out, err := runIntent(t, stages.IntentOptions{
		Transcripts: source,
		Runner:      agent.Runner(),
		Dir:         t.TempDir(),
		Recorder:    recorder,
	}, map[pipeline.Key]graph.Value{
		pipeline.KeyIntent:         graph.TextValue("add a greeting, and keep the existing one"),
		pipeline.KeyIntentSupplied: graph.BoolValue(true),
	})
	if err != nil {
		t.Fatalf("running the intent stage: %v", err)
	}
	if calls := agent.Calls(); len(calls) != 0 {
		t.Fatalf("the intent stage asked an agent %d times over a supplied intent", len(calls))
	}
	if source.calls != 0 {
		t.Fatalf("the intent stage read the transcripts %d times over a supplied intent", source.calls)
	}
	if len(out.Writes) != 0 {
		t.Fatalf("the intent stage wrote %+v over a supplied intent, which is the person's own words", out.Writes)
	}
	if recorder.last.Source != stages.IntentSupplied {
		t.Fatalf("a supplied intent was recorded as %s", recorder.last.Source)
	}
	if recorder.last.Summary != "add a greeting, and keep the existing one" {
		t.Fatalf("a supplied intent was recorded as %q", recorder.last.Summary)
	}
}

// An intent nobody supplied is inferred, written to the run's state, and
// recorded as a hint rather than as criteria. The score says how much of the
// material the summary was derived from, which is what a reader weighing a
// hint has to know.
func TestAnIntentNobodySuppliedIsInferredAndRecordedAsAHint(t *testing.T) {
	t.Parallel()

	const derived = "the goal was to add a greeting without disturbing the existing one"
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Match: standin.Match{PromptContains: "Derive the intent of this change"},
		Times: standin.Always,
		Reply: standin.Text(derived),
	}}})
	source := &transcripts{material: stages.IntentMaterial{
		// Half of what there was, so the score is a number the test can tell
		// from the two it would hold if coverage were not computed at all.
		Text: strings.Repeat("w", 40), Available: 80,
	}}
	recorder := &recorder{}

	out, err := runIntent(t, stages.IntentOptions{
		Transcripts: source,
		Runner:      agent.Runner(),
		Dir:         t.TempDir(),
		Recorder:    recorder,
	}, nil)
	if err != nil {
		t.Fatalf("running the intent stage: %v", err)
	}
	written, ok := out.Writes[pipeline.KeyIntent]
	if !ok {
		t.Fatalf("the intent stage inferred an intent and wrote no %s: %+v", pipeline.KeyIntent, out.Writes)
	}
	if text, _ := written.Text(); text != derived {
		t.Fatalf("the intent stage wrote %q, want the summary the agent returned", text)
	}
	if recorder.last.Source != stages.IntentInferred {
		t.Fatalf("an inferred intent was recorded as %s", recorder.last.Source)
	}
	if recorder.last.Score != 0.5 {
		t.Fatalf("a summary derived from half the material scored %v, want 0.5", recorder.last.Score)
	}
	// The prompt asks for the person's goal rather than for an account of the
	// diff, which PRD section 8 says makes a reviewer flag deliberate choices.
	if prompt := agent.Call().Prompt; !strings.Contains(prompt, "in their terms") {
		t.Fatalf("the summarizer was not asked for the goal in the person's terms:\n%s", prompt)
	}
}

// What is recorded is the summary, its source, and its score. Raw transcript
// text is never stored, and here that is a fact about the type rather than
// about this implementation's care: stages.IntentRecord has no field a
// transcript fits in, so no recorder can be handed one.
//
// The check is that the material this run had does not appear in what was
// recorded, and the material is distinctive so that finding it would mean it
// travelled rather than that two strings happened to match.
func TestNoRawTranscriptTextIsRecorded(t *testing.T) {
	t.Parallel()

	const material = "SENTINEL-TRANSCRIPT: the worker pasted a credential into its scratch notes"
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Times: standin.Always,
		Reply: standin.Text("the goal was to rename a package"),
	}}})
	recorder := &recorder{}

	if _, err := runIntent(t, stages.IntentOptions{
		Transcripts: &transcripts{material: stages.IntentMaterial{Text: material, Available: len(material)}},
		Runner:      agent.Runner(),
		Dir:         t.TempDir(),
		Recorder:    recorder,
	}, nil); err != nil {
		t.Fatalf("running the intent stage: %v", err)
	}
	if !recorder.recorded {
		t.Fatal("nothing was recorded, so this test would pass however the record was built")
	}
	if strings.Contains(recorder.last.Summary, "SENTINEL-TRANSCRIPT") {
		t.Fatalf("the material reached the durable record: %q", recorder.last.Summary)
	}
}

// The three sources are framed differently, which is what keeps a hint from
// being read as a requirement. The framings are compared to each other rather
// than to fixed sentences: what has to hold is that they differ and that each
// says what the reader may do with the text, not that any of them is worded
// the way it is today.
func TestEachIntentSourceIsFramedDifferently(t *testing.T) {
	t.Parallel()

	const intent = "add a greeting"
	framings := map[stages.IntentSource]string{}
	for _, source := range []stages.IntentSource{
		stages.IntentSupplied, stages.IntentInferred, stages.IntentUnstated,
	} {
		framings[source] = stages.IntentFraming(source, intent)
	}
	if framings[stages.IntentSupplied] == framings[stages.IntentInferred] {
		t.Fatal("supplied and inferred intent are framed the same, so a hint reads as criteria")
	}
	if framings[stages.IntentUnstated] == framings[stages.IntentInferred] {
		t.Fatal("an intent that could not be inferred is framed as one that was")
	}
	if !strings.Contains(framings[stages.IntentSupplied], "authoritative") {
		t.Fatalf("supplied intent is not framed as authoritative:\n%s", framings[stages.IntentSupplied])
	}
	if !strings.Contains(framings[stages.IntentInferred], "low-confidence") {
		t.Fatalf("inferred intent is not framed as a low-confidence hint:\n%s", framings[stages.IntentInferred])
	}
	// An absent intent must not carry the text of one, which is the framing a
	// stage would otherwise reach for by passing the empty string through.
	if strings.Contains(framings[stages.IntentUnstated], intent) {
		t.Fatalf("an unstated intent was framed carrying an intent:\n%s", framings[stages.IntentUnstated])
	}
	for source, framing := range framings {
		if strings.TrimSpace(framing) == "" {
			t.Fatalf("%s intent has no framing at all", source)
		}
	}
}

// The intent stage cannot promote an inference to authoritative, and that is
// structural rather than a rule it follows: pipeline.KeyIntentSupplied is a
// run input, so a stage declaring a write of it fails to build.
//
// The refusal is provoked here rather than described, because the claim in
// Intent's doc comment is about what internal/pipeline does and a claim of
// that shape is the kind this repository has shipped wrong before.
func TestNoStageCanDeclareTheIntentSuppliedBitAsItsOwn(t *testing.T) {
	t.Parallel()

	all := pipeline.ConstantStages("nothing to report")
	promoting := all.Intent
	promoting.Writes = []pipeline.Key{pipeline.KeyIntentSupplied}
	all.Intent = promoting

	_, err := pipeline.New(pipeline.Options{
		Stages: all, Rounds: config.FixRounds{}, Budget: config.DefaultRunBudget,
	})
	if err == nil {
		t.Fatal("a stage declaring a write of intent.supplied built a pipeline, so nothing " +
			"stops a stage from calling its own inference authoritative")
	}
	if !errors.Is(err, pipeline.ErrReservedKey) {
		t.Fatalf("the refusal is %v, want one matching ErrReservedKey", err)
	}
}

// The stage's own state read can fail, and that path has to be a note like
// every other. It is not in intentPaths because nothing a run does provokes
// it: the keys are declared, so only a reader that fails underneath reaches
// it, which is what this substitutes.
//
// It is worth covering rather than dismissing as unreachable. It is the one
// path where the thing that went wrong is the stage's own, and a stage that
// blocked on its own defect would break the guarantee in the case least
// likely to be exercised.
func TestTheIntentStageDoesNotBlockWhenItCannotReadItsOwnState(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P3)

	impl := stages.Intent(stages.IntentOptions{})
	out, err := impl.NewBody()(t.Context(), pipeline.Input{
		Stage: pipeline.StageIntent,
		State: failingReader{err: errors.New("the run's state is unreadable")},
	})
	if err != nil {
		t.Fatalf("the intent stage returned an error, which stops the run: %v", err)
	}
	if why := blocked(out.Report); why != "" {
		t.Fatalf("the intent stage blocked the run: %s\nreport: %+v", why, out.Report)
	}
	if len(out.Writes) != 0 {
		t.Fatalf("the intent stage wrote %+v having read nothing", out.Writes)
	}
}

// failingReader is a pipeline.Reader that cannot answer.
type failingReader struct{ err error }

// Get implements pipeline.Reader.
func (r failingReader) Get(pipeline.Key) (graph.Value, error) { return graph.Value{}, r.err }

// intentPath is one way through the intent stage: what it was built with, the
// state it reads, and the run that reaches it.
type intentPath struct {
	name    string
	options stages.IntentOptions
	state   map[pipeline.Key]graph.Value
	start   pipeline.Start
}

// intentPaths is every path a run can reach in the intent stage, which is what
// the two never-blocks tests run rather than the paths a working build takes.
// Adding a path to the stage means adding a row here.
//
// The stage has one more that a run cannot reach, its own state read failing,
// and TestTheIntentStageDoesNotBlockWhenItCannotReadItsOwnState covers it: a
// row here would need a reader that fails, which the pipeline test could not
// then execute.
func intentPaths(t *testing.T) []intentPath {
	t.Helper()

	dir := t.TempDir()
	material := stages.IntentMaterial{Text: "the worker renamed a package", Available: 28}
	supplied := map[pipeline.Key]graph.Value{
		pipeline.KeyIntent:         graph.TextValue("add a greeting"),
		pipeline.KeyIntentSupplied: graph.BoolValue(true),
	}
	suppliedStart := pipeline.Start{
		Branch: "topic", Base: "main", Submitted: "9f2c1ab",
		Intent: "add a greeting", IntentSupplied: true,
	}
	inferring := func(reply standin.Reply, opts ...func(*stages.IntentOptions)) stages.IntentOptions {
		o := stages.IntentOptions{
			Transcripts: &transcripts{material: material},
			Runner: standin.New(t, standin.Script{Steps: []standin.Step{
				{Times: standin.Always, Reply: reply},
			}}).Runner(),
			Dir: dir,
		}
		for _, opt := range opts {
			opt(&o)
		}
		return o
	}
	bare := pipeline.Start{Branch: "topic", Base: "main", Submitted: "9f2c1ab"}

	return []intentPath{
		{name: "a supplied intent",
			options: stages.IntentOptions{}, state: supplied, start: suppliedStart},
		{name: "a supplied intent the recorder could not write",
			options: stages.IntentOptions{Recorder: &recorder{err: errors.New("the disk is full")}},
			state:   supplied, start: suppliedStart},
		{name: "no intent and no transcripts to infer one from",
			options: stages.IntentOptions{}, start: bare},
		{name: "no intent and a transcript source but no agent",
			options: stages.IntentOptions{Transcripts: &transcripts{material: material}}, start: bare},
		{name: "transcripts that could not be extracted",
			options: inferring(standin.Text("unreachable"), func(o *stages.IntentOptions) {
				o.Transcripts = &transcripts{err: errors.New("the transcript directory is unreadable")}
			}), start: bare},
		{name: "material with nothing in it",
			options: inferring(standin.Text("unreachable"), func(o *stages.IntentOptions) {
				o.Transcripts = &transcripts{material: stages.IntentMaterial{}}
			}), start: bare},
		{name: "inference with no working directory for the agent",
			options: inferring(standin.Text("unreachable"), func(o *stages.IntentOptions) {
				o.Dir = ""
			}), start: bare},
		{name: "a summarizer that reported its own failure",
			options: inferring(standin.Failed("the model refused")), start: bare},
		{name: "a summarizer whose output could not be read",
			options: inferring(standin.Malformed("I am going to think about this out loud")), start: bare},
		{name: "a summarization that timed out",
			options: inferring(standin.Hang(), func(o *stages.IntentOptions) {
				o.Timeout = 50 * time.Millisecond
			}), start: bare},
		{name: "a summarizer that answered with nothing",
			options: inferring(standin.Text("   ")), start: bare},
		{name: "an inferred intent the recorder could not write",
			options: inferring(standin.Text("the goal was to rename a package"), func(o *stages.IntentOptions) {
				o.Recorder = &recorder{err: errors.New("the disk is full")}
			}), start: bare},
		{name: "an inference that succeeded",
			options: inferring(standin.Text("the goal was to rename a package")), start: bare},
	}
}

// runIntent runs the intent stage's body over the state it declares, through
// the same restriction the stage node applies: a read the implementation did
// not declare is refused here as it would be there.
func runIntent(t *testing.T, o stages.IntentOptions, state map[pipeline.Key]graph.Value) (pipeline.Output, error) {
	t.Helper()
	impl := stages.Intent(o)
	allowed := make(map[pipeline.Key]bool, len(impl.Reads))
	for _, key := range impl.Reads {
		allowed[key] = true
	}
	return impl.NewBody()(t.Context(), pipeline.Input{
		Stage: pipeline.StageIntent,
		State: declaredReader{allowed: allowed, state: state},
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
	// which for the two this stage reads is the empty text and false.
	if key == pipeline.KeyIntentSupplied {
		return graph.BoolValue(false), nil
	}
	return graph.TextValue(""), nil
}

// transcripts is a scripted stages.IntentTranscripts.
type transcripts struct {
	material stages.IntentMaterial
	err      error
	calls    int
}

// Material implements stages.IntentTranscripts.
func (s *transcripts) Material(context.Context) (stages.IntentMaterial, error) {
	s.calls++
	return s.material, s.err
}

// recorder is a stages.IntentRecorder that remembers what it was handed.
type recorder struct {
	err      error
	recorded bool
	last     stages.IntentRecord
}

// RecordIntent implements stages.IntentRecorder.
func (r *recorder) RecordIntent(_ context.Context, in stages.IntentRecord) error {
	r.recorded = true
	r.last = in
	return r.err
}
