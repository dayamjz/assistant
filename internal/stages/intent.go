package stages

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// IntentSource says where the intent a run carries came from. Downstream
// prompts are framed on it, so it is a closed set of three rather than the
// two PRD section 5 names: a run whose intent was never supplied and could not
// be inferred has an intent from nowhere, and a reader told "inferred" about
// it would weigh a hint that was never derived.
type IntentSource string

const (
	// IntentSupplied is intent a person stated. PRD section 5 makes it
	// authoritative acceptance criteria, and no stage may produce it: the bit
	// behind it is pipeline.KeyIntentSupplied, a run input no stage can write.
	IntentSupplied IntentSource = "supplied"
	// IntentInferred is intent this stage derived from what the worker did. It
	// is a low-confidence hint and never acceptance criteria.
	IntentInferred IntentSource = "inferred"
	// IntentUnstated is no intent at all: none was supplied and none was
	// derived. It is not an error, and PRD section 5's "this stage never
	// blocks a run" is why: a run with no recorded intent proceeds, carrying
	// the empty pipeline.KeyIntent it started with rather than a sentence this
	// stage invented for it.
	IntentUnstated IntentSource = "unstated"
)

// String renders the source as it is recorded.
func (s IntentSource) String() string { return string(s) }

// IntentFraming returns the sentence a downstream stage introduces the run's
// intent with. It exists so that PRD section 5's "the two are framed
// differently in every downstream prompt" has one owner, per P14, rather than
// each stage phrasing the distinction for itself and one of them eventually
// presenting a hint as a requirement.
//
// The three framings say different things about what the reader may do with
// the text: acceptance criteria to be met, a hint that may be wrong, and an
// absence that is not a licence to invent one.
//
// No stage body consumes this yet, because no body downstream of intent is
// written. That is a gap in what this build enforces rather than a weaker
// claim about the framing: what is here is the one owner, ready for the first
// downstream body, and until one exists nothing in this repository
// demonstrates a downstream prompt carrying it.
func IntentFraming(source IntentSource, intent string) string {
	intent = strings.TrimSpace(intent)
	switch source {
	case IntentSupplied:
		return "The person who asked for this change stated its intent, and it is authoritative " +
			"acceptance criteria. Treat a change that does not meet it as wrong.\n\n" + intent
	case IntentInferred:
		return "No intent was supplied for this change. The following was inferred from what the " +
			"worker did, and it is a low-confidence hint rather than acceptance criteria. It may " +
			"be wrong or incomplete, so do not report a deliberate choice as a defect for " +
			"departing from it.\n\n" + intent
	default:
		return "No intent was supplied for this change and none could be inferred. Judge the " +
			"change on what it does rather than against criteria nobody stated, and do not " +
			"invent an intent to measure it against."
	}
}

// IntentMaterial is what an inference reads to derive an intent from: the text
// a summarizer is given, and how much of it there was to give.
//
// The two are separate because a source that had to truncate produced a
// summary of part of the record, and IntentRecord.Score is what says so. A
// source that hands over everything it has sets Available to len(Text).
type IntentMaterial struct {
	// Text is the material handed to the summarizer. Empty means the source
	// found nothing to derive an intent from, which is not an error.
	Text string
	// Available is the size in bytes of the material that existed, which is at
	// least len(Text) and is larger when the source truncated. A source that
	// leaves it zero while supplying Text is read as having supplied all of
	// it, since a score is a ratio and there is no honest one to compute from
	// a total nobody reported.
	Available int
}

// IntentTranscripts is where the record of what a worker did is read from, so
// an intent nobody supplied can be inferred from it.
//
// It is a seam with no implementation in this repository, and the build wired
// by All supplies none, so no run infers an intent today. That is deliberate:
// the PRD's phase list defers transcript-based intent inference, on the
// grounds that supplying intent explicitly is the mechanism that has to work
// first. What is here is the shape the deferred work lands in, and the stage
// treats its absence as one more thing to skip rather than as a
// misconfiguration.
type IntentTranscripts interface {
	// Material returns what this run's worker did. A source with nothing to
	// offer returns the zero IntentMaterial and a nil error; an error means it
	// could not tell, which this stage skips rather than fails.
	Material(ctx context.Context) (IntentMaterial, error)
}

// IntentRecord is what is durably recorded about a run's intent.
//
// It carries the derived summary, where it came from, and how much of the
// available material it was derived from, and it carries no transcript. That
// is the mechanism behind "raw transcript text is never stored" rather than a
// rule a caller follows: there is no field here a transcript fits in, so an
// IntentRecorder cannot be handed one to store.
//
// What that does not cover is a summary that quotes its material. The
// summarizer is asked for the change's goal rather than for a transcript, and
// nothing here inspects what comes back, so a summary reproducing raw text
// would be recorded as the summary it claims to be. The bound is on the shape,
// not on the content.
type IntentRecord struct {
	// Summary is the intent in words: what the change set out to do.
	Summary string
	// Source says where Summary came from.
	Source IntentSource
	// Score is how much of the available material the summary was derived
	// from, from 0 to 1. It is 1 for an IntentSupplied record, since a person
	// stating the criteria left nothing unread, and 0 for IntentUnstated.
	//
	// It measures coverage of the material and not correctness of the summary:
	// a summary derived from every byte of a transcript that never discussed
	// the change scores 1 and is still wrong. It is here so a reader weighing
	// a hint knows how much the summarizer actually saw.
	Score float64
}

// IntentRecorder records a run's derived intent durably. It is a seam, and the
// build wired by All supplies none.
type IntentRecorder interface {
	// RecordIntent stores what was derived. An error is reported by the stage
	// and does not fail it: PRD section 5 has this stage never block a run,
	// and a run whose intent was not written down is a worse report rather
	// than an unsafe change.
	RecordIntent(ctx context.Context, r IntentRecord) error
}

// DefaultIntentTimeout bounds one summarization. This stage owns a bound of
// its own because "never blocks a run" has to cover a summarizer that never
// answers: an unbounded agent call would hold the run open at stage one
// without ever reporting a finding, which is a run nobody can answer and is
// exactly the outcome the guarantee exists to exclude.
const DefaultIntentTimeout = 2 * time.Minute

// IntentOptions is what the intent stage is built with. Every field is
// optional, and the zero value is the build All wires: intent that was
// supplied is recorded as authoritative, and nothing is inferred.
type IntentOptions struct {
	// Transcripts is where an intent nobody supplied is inferred from. Nil
	// means this build infers nothing.
	Transcripts IntentTranscripts
	// Runner is the agent that summarizes the material. Nil means this build
	// infers nothing, whatever Transcripts holds.
	Runner agents.Runner
	// Dir is the absolute working directory the summarizer runs in, normally
	// the run's isolated copy. agents.Invocation requires one, so inference
	// without it is refused before the agent starts and skipped like any other
	// inference that could not be made.
	Dir string
	// Model names the model to summarize with, empty to leave the choice to
	// the agent.
	Model string
	// Recorder durably records what was derived. Nil means this build records
	// nothing beyond the run's own state.
	Recorder IntentRecorder
	// Timeout bounds one summarization. Zero means DefaultIntentTimeout, and a
	// negative value leaves the call bounded only by the context it is given.
	Timeout time.Duration
}

// summarizes reports whether this build can infer an intent at all.
func (o IntentOptions) summarizes() bool { return o.Transcripts != nil && o.Runner != nil }

// deadline bounds a summarization, returning a cancel that is always safe to
// call.
func (o IntentOptions) deadline(ctx context.Context) (context.Context, context.CancelFunc) {
	switch {
	case o.Timeout < 0:
		return ctx, func() {}
	case o.Timeout == 0:
		return context.WithTimeout(ctx, DefaultIntentTimeout)
	default:
		return context.WithTimeout(ctx, o.Timeout)
	}
}

// Intent is the intent stage: it establishes what the change set out to do and
// records it for every stage after it.
//
// # The guarantee this implementation owes
//
// PRD section 5 says the intent stage never blocks a run, and every path here
// answers to that. Every finding it reports is a note, so nothing it finds
// holds the stage for a person and nothing it finds enters a fix round, and it
// returns no error, so nothing it meets stops the run. Missing transcripts, a
// source that could not read them, material with nothing in it, a summarizer
// that failed or never answered, and a recorder that could not write are all
// reported and stepped past.
//
// internal/pipeline deliberately does not enforce this structurally, and that
// is the right division. A stage node that could not hold would have to
// discard an ask finding to keep the promise, and a discarded ask is exactly
// what P3 exists to prevent. So the guarantee is this implementation's, which
// is why it is stated here and demonstrated by a test rather than left to the
// pipeline.
//
// The bound on it is behavioural, not structural: the report is built here, so
// a future edit could state an action other than note and nothing in the type
// system would object. The tests are what stand in for that.
// TestTheIntentStageNeverBlocksARun drives every path a run can reach, and
// TestTheIntentStageDoesNotBlockWhenItCannotReadItsOwnState drives the one it
// cannot, so what is checked is the paths this stage has rather than the ones
// a working build takes. Both use an assertion that
// TestTheNeverBlocksAssertionRejectsAReportThatBlocks holds to a report that
// does block, so neither can pass by checking nothing.
//
// # Supplied intent is authoritative and is never inferred over
//
// A run that supplied an intent has acceptance criteria, and this stage
// records them and stops: it reads no transcript, starts no agent, and writes
// nothing to pipeline.KeyIntent, because what is already there is what the
// person said. Inference runs only where no intent was supplied. Nothing here
// can promote an inference to authoritative either, and that is structural
// rather than careful: pipeline.KeyIntentSupplied is a run input, so this
// stage's declaration cannot include a write of it and a pipeline built from
// one that did would be refused.
//
// # What it writes
//
// An inferred intent is written to pipeline.KeyIntent, which is the one key
// this stage declares. An intent that was supplied is already there and an
// intent that could not be derived leaves it as it was, so a run with no
// intent carries the empty text rather than a sentence this stage invented.
func Intent(o IntentOptions) pipeline.Implementation {
	return pipeline.Implementation{
		Reads:  []pipeline.Key{pipeline.KeyIntent, pipeline.KeyIntentSupplied},
		Writes: []pipeline.Key{pipeline.KeyIntent},
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
				return o.establish(ctx, in)
			}
		},
	}
}

// establish is the stage body. It reports and never refuses: every error it
// meets becomes a note on a report the run carries forward.
func (o IntentOptions) establish(ctx context.Context, in pipeline.Input) (pipeline.Output, error) {
	supplied, stated, err := readIntent(in.State)
	if err != nil {
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
					"being validated.", err)),
		), nil
	}
	if stated {
		return o.record(ctx, IntentRecord{Summary: supplied, Source: IntentSupplied, Score: 1},
			"The intent for this run was supplied, so it is authoritative acceptance criteria "+
				"and nothing was inferred.",
			intentNote("intent-supplied", findings.SeverityInfo,
				"The intent was supplied rather than inferred, so it is authoritative "+
					"acceptance criteria for this change rather than a hint about it."),
			nil)
	}
	return o.infer(ctx)
}

// infer derives an intent for a run that supplied none. Every way it can fail
// to produce one ends in a note and an advancing run.
func (o IntentOptions) infer(ctx context.Context) (pipeline.Output, error) {
	if !o.summarizes() {
		return o.unstated(ctx,
			"No intent was supplied and this build infers none, so this run carries no intent.",
			intentNote("intent-not-inferred", findings.SeverityWarning,
				"No intent was supplied for this run and this build has no transcript source to "+
					"infer one from, so the stages after this one judge the change on what it "+
					"does. Supplying an intent gives them acceptance criteria to measure it "+
					"against."))
	}
	material, err := o.Transcripts.Material(ctx)
	if err != nil {
		return o.unstated(ctx,
			"No intent was supplied and the record of what the worker did could not be read, "+
				"so this run carries no intent.",
			intentNote("intent-material-unreadable", findings.SeverityWarning, fmt.Sprintf(
				"Reading the record of what the worker did failed: %v. No intent was inferred "+
					"and the run continues without one.", err)))
	}
	if strings.TrimSpace(material.Text) == "" {
		return o.unstated(ctx,
			"No intent was supplied and there was no record of what the worker did, so this "+
				"run carries no intent.",
			intentNote("intent-material-empty", findings.SeverityWarning,
				"No intent was supplied for this run and the record of what the worker did is "+
					"empty, so there was nothing to infer one from. The run continues without "+
					"an intent."))
	}
	summary, err := o.summarize(ctx, material.Text)
	if err != nil {
		return o.unstated(ctx,
			"No intent was supplied and inferring one failed, so this run carries no intent.",
			intentNote("intent-inference-failed", findings.SeverityWarning, fmt.Sprintf(
				"Inferring an intent from the record of what the worker did failed: %v. The run "+
					"continues without an intent.", err)))
	}
	if summary == "" {
		return o.unstated(ctx,
			"No intent was supplied and the summarizer returned nothing, so this run carries "+
				"no intent.",
			intentNote("intent-inference-empty", findings.SeverityWarning,
				"Inferring an intent returned an empty summary, so there is nothing to record. "+
					"The run continues without an intent."))
	}
	score := material.score()
	return o.record(ctx, IntentRecord{Summary: summary, Source: IntentInferred, Score: score},
		"No intent was supplied, so one was inferred from what the worker did. It is a "+
			"low-confidence hint rather than acceptance criteria.",
		intentNote("intent-inferred", findings.SeverityWarning, fmt.Sprintf(
			"No intent was supplied for this run, so one was inferred from the record of what "+
				"the worker did, covering %.0f%% of it. It is a low-confidence hint and not "+
				"acceptance criteria, so a deliberate choice that departs from it is not a "+
				"defect. Supplying an intent replaces this with criteria the run can be "+
				"measured against.", score*100)),
		map[pipeline.Key]graph.Value{pipeline.KeyIntent: graph.TextValue(summary)})
}

// unstated records that this run has no intent and reports why. It is the end
// of every path inference does not complete, and it is a note in all of them.
func (o IntentOptions) unstated(ctx context.Context, summary string, found findings.Finding) (pipeline.Output, error) {
	return o.record(ctx, IntentRecord{Source: IntentUnstated}, summary, found, nil)
}

// record writes what was derived through the recorder, if there is one, and
// appends a note when that fails. The writes are returned either way: a run's
// own state is what the stages after this one read, and losing the intent
// there because a durable record could not be written would make one failure
// into two.
func (o IntentOptions) record(
	ctx context.Context,
	r IntentRecord,
	summary string,
	found findings.Finding,
	writes map[pipeline.Key]graph.Value,
) (pipeline.Output, error) {
	out := intentReport(summary, found)
	out.Writes = writes
	if o.Recorder == nil {
		return out, nil
	}
	if err := o.Recorder.RecordIntent(ctx, r); err != nil {
		out.Report.Findings = append(out.Report.Findings, intentNote(
			"intent-not-recorded", findings.SeverityWarning, fmt.Sprintf(
				"Recording this run's intent durably failed: %v. The run continues and the "+
					"stages after this one still see the intent, but a report written after "+
					"the run may not say what it was.", err)))
	}
	return out, nil
}

// summarize asks the agent for the change's goal in the person's terms. It
// bounds the call itself, so a summarizer that never answers ends this stage
// rather than the run.
func (o IntentOptions) summarize(ctx context.Context, material string) (string, error) {
	ctx, cancel := o.deadline(ctx)
	defer cancel()
	result, err := o.Runner.Run(ctx, agents.PurposeIntent, agents.Invocation{
		Prompt: intentPrompt(material),
		Shape:  agents.ShapeText,
		Dir:    o.Dir,
		Model:  o.Model,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Text), nil
}

// intentPromptLead is the instruction the summarizer is given, and the
// landmark a test names an intent invocation by.
const intentPromptLead = "Derive the intent of this change from the record below."

// intentPrompt assembles what the summarizer is asked. It asks for the goal in
// the person's own terms rather than for a description of the diff, because
// PRD section 8 has a one-line summary of the diff make the reviewer flag
// choices the person already made deliberately.
func intentPrompt(material string) string {
	return intentPromptLead + "\n\n" +
		"State what the person set out to achieve, in their terms: the goal, the decisions and " +
		"tradeoffs behind it, the constraints, and any approach they ruled out. Do not describe " +
		"the diff and do not evaluate the work. Answer with the intent alone.\n\n" +
		"Record of what the worker did:\n\n" + material
}

// score is how much of the available material the text covers, from 0 to 1. A
// source that reported no total is read as having supplied all it had, and one
// reporting a total below what it supplied is read the same way rather than
// scored above 1.
func (m IntentMaterial) score() float64 {
	if strings.TrimSpace(m.Text) == "" {
		return 0
	}
	if m.Available <= len(m.Text) {
		return 1
	}
	return float64(len(m.Text)) / float64(m.Available)
}

// readIntent reads the run's intent and whether it was supplied. It reports a
// supplied intent as stated only when there is text to it, which
// pipeline.NewState already refuses to start a run without; the check is here
// as well because this stage's behaviour turns on it and a bit set over an
// empty string would otherwise record authoritative criteria that say nothing.
func readIntent(state pipeline.Reader) (string, bool, error) {
	suppliedValue, err := state.Get(pipeline.KeyIntentSupplied)
	if err != nil {
		return "", false, err
	}
	intentValue, err := state.Get(pipeline.KeyIntent)
	if err != nil {
		return "", false, err
	}
	supplied, _ := suppliedValue.Bool()
	text, _ := intentValue.Text()
	text = strings.TrimSpace(text)
	return text, supplied && text != "", nil
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
