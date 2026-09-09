package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
)

// GrokName is the name configuration spells this adapter with.
const GrokName = "grok"

// GrokOption configures the Grok adapter. Options are supplied when the
// factory is built and are fixed for the lifetime of every Runner it produces,
// so every invocation runs under the same configuration.
type GrokOption func(*grokSettings)

type grokSettings struct {
	bin      string
	base     []string
	baseSet  bool
	grace    time.Duration
	maxOut   int64
	recorder Recorder
	sweep    Sweep
}

// WithGrokBinary names the executable to run. The default is "grok", resolved
// through PATH. A caller that pins an installation, or a test that substitutes
// a stand-in, passes it here.
func WithGrokBinary(path string) GrokOption {
	return func(s *grokSettings) {
		if path != "" {
			s.bin = path
		}
	}
}

// WithGrokBaseEnvironment sets the environment every invocation starts from,
// before an Invocation's own Env is applied over it. The default is the
// process environment; an empty or nil slice given here is a deliberately
// empty base rather than a request for the default. Nothing is filtered out of
// it: an agent's credentials legitimately arrive this way, and what keeps them
// out of a record is that Record has no field they could be written into.
func WithGrokBaseEnvironment(env []string) GrokOption {
	return func(s *grokSettings) {
		s.base = slices.Clone(env)
		s.baseSet = true
	}
}

// WithGrokTerminationGrace sets how long the process tree gets to exit on a
// polite signal before a forceful one is sent. A value of zero or less leaves
// the default in place.
func WithGrokTerminationGrace(d time.Duration) GrokOption {
	return func(s *grokSettings) {
		if d > 0 {
			s.grace = d
		}
	}
}

// WithGrokMaxOutput sets the largest standard output, in bytes, one invocation
// may produce. An invocation that produces more fails with FailureOversize and
// its output is discarded rather than truncated. A value of zero or less
// leaves the default in place.
func WithGrokMaxOutput(n int64) GrokOption {
	return func(s *grokSettings) {
		if n > 0 {
			s.maxOut = n
		}
	}
}

// WithGrokRecorder sets the Recorder every invocation's cost is reported to.
// The default records nothing, and Result.Record carries the same value either
// way.
func WithGrokRecorder(r Recorder) GrokOption {
	return func(s *grokSettings) { s.recorder = r }
}

// WithGrokSweep supplies the identity-based sweep described on Sweep. The
// default is none, and the residual gap that leaves is stated there.
func WithGrokSweep(sw Sweep) GrokOption {
	return func(s *grokSettings) { s.sweep = sw }
}

func newGrokSettings(opts []GrokOption) grokSettings {
	s := grokSettings{
		bin:    GrokName,
		grace:  DefaultTerminationGrace,
		maxOut: DefaultMaxOutput,
	}
	for _, o := range opts {
		if o != nil {
			o(&s)
		}
	}
	if !s.baseSet {
		s.base = os.Environ()
	}
	return s
}

// grokFactory is the Factory for Grok.
type grokFactory struct{ settings grokSettings }

// GrokFactory returns the Factory for the Grok adapter. Grok does not have
// resumable sessions, so it implements only Runner and not SessionRunner.
func GrokFactory(opts ...GrokOption) Factory {
	return &grokFactory{settings: newGrokSettings(opts)}
}

// Name returns "grok".
func (f *grokFactory) Name() string { return GrokName }

// New reports whether Grok can be run here, and returns a Runner when it can.
//
// What availability means here is exactly one thing: the named executable
// resolves to a file this process may execute. That is what an ordered
// fallback list needs to skip an agent that is not installed, and it is all
// this check buys. An installation that resolves and then fails on its first
// invocation, because it is unauthenticated or of an incompatible version, is
// not caught here; it surfaces as a failed invocation with the agent's own
// message. Proving more would mean running the agent during resolution, which
// costs a process and a possible hang at the exact moment a run is deciding
// whether it can start at all.
func (f *grokFactory) New(ctx context.Context, args []string) (Runner, error) {
	_ = ctx
	resolved, err := exec.LookPath(f.settings.bin)
	if err != nil {
		return nil, fmt.Errorf("%s is not runnable: %w", f.settings.bin, err)
	}
	return &grokRunner{
		settings: f.settings,
		resolved: resolved,
		extra:    slices.Clone(args),
	}, nil
}

// grokRunner runs prompts through one Grok installation.
type grokRunner struct {
	settings grokSettings
	resolved string
	extra    []string
}

// Name returns "grok".
func (r *grokRunner) Name() string { return GrokName }

// Capabilities is what this adapter declares.
//
// Instruction suppression is not declared, because nothing here implements it.
// This adapter carries a configured entry's own flags and the four the run
// manages, and none of those tells Grok to ignore a repository's instruction
// files. Declaring it would buy a run that suppresses nothing while reporting
// that it did, which is the substitution PRD section 8 refuses. Leaving it
// undeclared is what lets internal/pipeline refuse a run that asks for
// suppression against this adapter before it launches one.
//
// Resumable sessions are not declared, because Grok does not support session
// resumption. This adapter has no Fixer method and does not implement
// SessionRunner.
func (r *grokRunner) Capabilities() Capabilities {
	return Capabilities{}
}

// Run executes one invocation with no session.
func (r *grokRunner) Run(ctx context.Context, purpose Purpose, inv Invocation) (Result, error) {
	if !purpose.Recognized() {
		return Result{}, fmt.Errorf("%w: %q", ErrUnrecognizedPurpose, string(purpose))
	}
	return r.invoke(ctx, purpose, inv)
}

// invoke runs one agent process and turns what it printed into a Result or a
// refusal.
func (r *grokRunner) invoke(ctx context.Context, purpose Purpose, inv Invocation) (Result, error) {
	if err := inv.Validate(); err != nil {
		return Result{}, err
	}

	record := Record{
		Purpose: purpose,
		Agent:   GrokName,
		Model:   inv.Model,
		Session: SessionNone,
		Started: time.Now(),
	}

	fail := func(res *procResult, category Failure, cause error, message string) (Result, error) {
		record.Duration = time.Since(record.Started)
		record.Failure = category
		r.report(record)
		code := -1
		if res != nil {
			code = res.code
			if message == "" {
				message = res.stderr
			}
		}
		return Result{}, &InvocationError{
			Purpose:  purpose,
			Agent:    GrokName,
			Failure:  category,
			ExitCode: code,
			Message:  message,
			Err:      cause,
		}
	}

	proc := runProcess(ctx, procSpec{
		bin:    r.resolved,
		args:   r.arguments(inv),
		stdin:  inv.Prompt,
		dir:    inv.Dir,
		env:    environment(r.settings.base, inv.Env),
		grace:  r.settings.grace,
		maxOut: r.settings.maxOut,
	})

	r.sweep(ctx, inv.Dir)

	var envelope grokEnvelope
	decodeErr := json.Unmarshal(proc.stdout, &envelope)
	if decodeErr == nil {
		record.Usage = envelope.usage()
		if envelope.Model != "" {
			record.Model = envelope.Model
		}
	}

	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		return fail(&proc, FailureCancelled, ctx.Err(), "")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fail(&proc, FailureTimeout, ctx.Err(), "")
	}
	if proc.over {
		return fail(&proc, FailureOversize,
			fmt.Errorf("agent printed more than the limit of %d bytes", r.settings.maxOut), "")
	}

	var exited *exec.ExitError
	if !proc.exited || (proc.err != nil && !errors.As(proc.err, &exited)) {
		return fail(&proc, FailureProcess, proc.err, "")
	}

	if decodeErr == nil && envelope.IsError {
		return fail(&proc, FailureAgent,
			errors.New("agent reported its own failure"), envelope.failureText())
	}
	if proc.code != 0 {
		return fail(&proc, FailureExit, nil, "")
	}
	if decodeErr != nil {
		return fail(&proc, FailureOutput,
			fmt.Errorf("agent did not print a result envelope: %w", decodeErr), "")
	}
	if envelope.Result == "" {
		return fail(&proc, FailureOutput,
			errors.New("agent's result envelope carried no result"), "")
	}

	result := Result{Text: envelope.Result}
	switch inv.Shape {
	case ShapeReport:
		report, err := findings.ParseReport(envelope.Result)
		if err != nil {
			return fail(&proc, FailureOutput,
				fmt.Errorf("agent's result is not a stage report: %w", err), "")
		}
		result.Report = report
	case ShapeReview:
		report, binding, err := findings.ParseReviewReport(envelope.Result, inv.Review)
		if err != nil {
			return fail(&proc, FailureOutput,
				fmt.Errorf("agent's result is not a bindable review report: %w", err), "")
		}
		result.Report, result.Binding = report, binding
	}

	record.Duration = time.Since(record.Started)
	record.Failure = FailureNone
	r.report(record)
	result.Record = record
	return result, nil
}

// arguments builds the command line. The configured entry's own flags come
// first, then the managed flags: --print, --output-format, and --model.
//
// The prompt is not on the command line at all. It is written to the agent's
// standard input, so its size is bounded by MaxPromptBytes rather than by an
// argument list, and a prompt that begins with a dash cannot be read as an
// option.
func (r *grokRunner) arguments(inv Invocation) []string {
	args := slices.Clone(r.extra)
	args = append(args, "--print", "--output-format", "json")
	if inv.Model != "" {
		args = append(args, "--model", inv.Model)
	}
	return args
}

// report hands one record to the configured Recorder, if there is one.
func (r *grokRunner) report(rec Record) {
	if r.settings.recorder != nil {
		r.settings.recorder.RecordInvocation(rec)
	}
}

// sweep calls the identity-based sweep seam, on a context the invocation's own
// cancellation does not cancel, since a cancelled invocation is when a sweep
// matters most.
func (r *grokRunner) sweep(ctx context.Context, dir string) {
	if r.settings.sweep == nil {
		return
	}
	r.settings.sweep.SweepUnder(context.WithoutCancel(ctx), dir)
}

// grokEnvelope is the result envelope this adapter reads, holding only the
// fields it uses. Every field is optional at the decoding layer and what is
// required is decided above, so a field the agent stops reporting produces a
// named refusal rather than a decoding error a caller cannot act on.
//
// A field this struct does not name is ignored, so an agent that adds one does
// not cost an otherwise usable result.
//
// This describes what the adapter reads, not what the agent guarantees to
// write. An envelope this adapter cannot read is FailureOutput.
type grokEnvelope struct {
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Model   string `json:"model"`
	// Every count is a pointer so that a counter the agent omitted stays
	// distinguishable from one it reported as zero. Decoding into a number
	// would collapse the two here, where the difference is still knowable.
	NumTurns *int64 `json:"num_turns"`
	Usage    struct {
		InputTokens              *int64 `json:"input_tokens"`
		OutputTokens             *int64 `json:"output_tokens"`
		CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// usage converts the reported counts into the recorded form, carrying which of
// them the agent actually reported.
func (e grokEnvelope) usage() Usage {
	return Usage{
		InputTokens:         reportedCount(e.Usage.InputTokens),
		OutputTokens:        reportedCount(e.Usage.OutputTokens),
		CacheReadTokens:     reportedCount(e.Usage.CacheReadInputTokens),
		CacheCreationTokens: reportedCount(e.Usage.CacheCreationInputTokens),
		Turns:               reportedCount(e.NumTurns),
	}
}

// failureText is what the agent said about its own failure. It is content, so
// it reaches an *InvocationError and never a Record.
func (e grokEnvelope) failureText() string {
	if e.Result != "" {
		return e.Result
	}
	return e.Subtype
}
