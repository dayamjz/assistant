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

// OpenAIName is the name configuration spells this adapter with.
const OpenAIName = "openai"

// OpenAIOption configures the OpenAI adapter. Options are supplied when
// the factory is built and are fixed for the lifetime of every Runner it
// produces, so every invocation runs under the same configuration.
type OpenAIOption func(*openaiSettings)

type openaiSettings struct {
	bin      string
	base     []string
	baseSet  bool
	grace    time.Duration
	maxOut   int64
	recorder Recorder
	sweep    Sweep
}

// WithOpenAIBinary names the executable to run. The default is "openai", resolved
// through PATH. A caller that pins an installation, or a test that substitutes
// a stand-in, passes it here.
func WithOpenAIBinary(path string) OpenAIOption {
	return func(s *openaiSettings) {
		if path != "" {
			s.bin = path
		}
	}
}

// WithOpenAIBaseEnvironment sets the environment every invocation starts from,
// before an Invocation's own Env is applied over it. The default is the
// process environment; an empty or nil slice given here is a deliberately
// empty base rather than a request for the default. Nothing is filtered out of it: an agent's credentials
// legitimately arrive this way, and what keeps them out of a record is that
// Record has no field they could be written into.
func WithOpenAIBaseEnvironment(env []string) OpenAIOption {
	return func(s *openaiSettings) {
		s.base = slices.Clone(env)
		s.baseSet = true
	}
}

// WithOpenAITerminationGrace sets how long the process tree gets to exit on a polite
// signal before a forceful one is sent. A value of zero or less leaves the
// default in place.
func WithOpenAITerminationGrace(d time.Duration) OpenAIOption {
	return func(s *openaiSettings) {
		if d > 0 {
			s.grace = d
		}
	}
}

// WithOpenAIMaxOutput sets the largest standard output, in bytes, one invocation may
// produce. An invocation that produces more fails with FailureOversize and its
// output is discarded rather than truncated. A value of zero or less leaves
// the default in place.
func WithOpenAIMaxOutput(n int64) OpenAIOption {
	return func(s *openaiSettings) {
		if n > 0 {
			s.maxOut = n
		}
	}
}

// WithOpenAIRecorder sets the Recorder every invocation's cost is reported to. The
// default records nothing, and Result.Record carries the same value either
// way.
func WithOpenAIRecorder(r Recorder) OpenAIOption {
	return func(s *openaiSettings) { s.recorder = r }
}

// WithOpenAISweep supplies the identity-based sweep described on Sweep. The default
// is none, and the residual gap that leaves is stated there.
func WithOpenAISweep(sw Sweep) OpenAIOption {
	return func(s *openaiSettings) { s.sweep = sw }
}

func newOpenAISettings(opts []OpenAIOption) openaiSettings {
	s := openaiSettings{
		bin:    OpenAIName,
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

// openaiFactory is the Factory for OpenAI.
type openaiFactory struct{ settings openaiSettings }

// OpenAIFactory returns the Factory for the OpenAI adapter. This adapter does
// not support resumable sessions, so it implements Runner only and never
// SessionRunner.
func OpenAIFactory(opts ...OpenAIOption) Factory {
	return &openaiFactory{settings: newOpenAISettings(opts)}
}

// Name returns "openai".
func (f *openaiFactory) Name() string { return OpenAIName }

// New reports whether OpenAI can be run here, and returns a Runner when
// it can.
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
func (f *openaiFactory) New(ctx context.Context, args []string) (Runner, error) {
	_ = ctx
	resolved, err := exec.LookPath(f.settings.bin)
	if err != nil {
		return nil, fmt.Errorf("%s is not runnable: %w", f.settings.bin, err)
	}
	return &openaiRunner{
		settings: f.settings,
		resolved: resolved,
		extra:    slices.Clone(args),
	}, nil
}

// openaiRunner runs prompts through one OpenAI installation.
type openaiRunner struct {
	settings openaiSettings
	resolved string
	extra    []string
}

// Name returns "openai".
func (r *openaiRunner) Name() string { return OpenAIName }

// Capabilities is what this adapter declares.
//
// Resumable sessions are not declared because OpenAI does not support session
// continuation, so this adapter implements Runner only and never SessionRunner.
//
// Instruction suppression is not declared, because nothing here implements it.
// This adapter carries a configured entry's own flags and the four the run
// manages, and none of those tells OpenAI to ignore a repository's
// instruction files. Declaring it would buy a run that suppresses nothing
// while reporting that it did, which is the substitution PRD section 8 refuses.
// Leaving it undeclared is what lets internal/pipeline refuse a run that asks
// for suppression against this adapter before it launches one.
func (r *openaiRunner) Capabilities() Capabilities {
	return Capabilities{}
}

// Run executes one invocation with no session. OpenAI does not support
// sessions, so every invocation is independent and the record always reads
// SessionNone.
func (r *openaiRunner) Run(ctx context.Context, purpose Purpose, inv Invocation) (Result, error) {
	if !purpose.Recognized() {
		return Result{}, fmt.Errorf("%w: %q", ErrUnrecognizedPurpose, string(purpose))
	}
	return r.invoke(ctx, purpose, inv)
}

// invoke runs one agent process and turns what it printed into a Result or a
// refusal.
func (r *openaiRunner) invoke(ctx context.Context, purpose Purpose, inv Invocation) (Result, error) {
	if err := inv.Validate(); err != nil {
		// Nothing started, so nothing cost anything and there is no record to
		// write.
		return Result{}, err
	}

	record := Record{
		Purpose: purpose,
		Agent:   OpenAIName,
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
			Agent:    OpenAIName,
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

	// The envelope is read before anything is classified, so an agent that
	// reported what it spent has that recorded whichever way the invocation
	// then ended, a cancellation and an elapsed deadline included. A failed
	// invocation is exactly the one whose cost is worth knowing, and recording
	// a zero for it would understate what was spent. An over-limit standard
	// output was discarded whole rather than truncated, so there is nothing
	// here to decode and nothing is recorded from it, which is the outcome
	// output no part of which may be read as whole should have.
	var envelope openaiEnvelope
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

	// Two things have to hold before a status means anything: the process must
	// have run, which an *exec.ExitError says and any other error denies, and
	// it must have reported a status of its own rather than having been ended
	// by something else. Checking both here is what keeps the -1 that stands
	// for "no status" from being read as an ordinary exit, and the cause
	// travels with the refusal, so an agent the system killed is not
	// indistinguishable from one that crashed.
	var exited *exec.ExitError
	if !proc.exited || (proc.err != nil && !errors.As(proc.err, &exited)) {
		return fail(&proc, FailureProcess, proc.err, "")
	}
	// The agent's own verdict outranks its exit status. An agent that says it
	// failed is an agent failure whatever status came with it, and what it
	// said about the failure is the best evidence there is of what went wrong.
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
		// The demand travels with the invocation, so the only way to read a
		// review's output here is bound to it. A report of another revision,
		// and one whose findings reach past what it declared reading, are
		// refused on the terms findings.ParseReviewReport states rather than
		// reported and left for a caller to check.
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
// first, then the managed flags for output format and model.
//
// The prompt is not on the command line at all. It is written to the agent's
// standard input, so its size is bounded by MaxPromptBytes rather than by an
// argument list, and a prompt that begins with a dash cannot be read as an
// option.
func (r *openaiRunner) arguments(inv Invocation) []string {
	args := slices.Clone(r.extra)
	args = append(args, "--output-format", "json")
	if inv.Model != "" {
		args = append(args, "--model", inv.Model)
	}
	return args
}

// report hands one record to the configured Recorder, if there is one.
func (r *openaiRunner) report(rec Record) {
	if r.settings.recorder != nil {
		r.settings.recorder.RecordInvocation(rec)
	}
}

// sweep calls the identity-based sweep seam, on a context the invocation's own
// cancellation does not cancel, since a cancelled invocation is when a sweep
// matters most.
func (r *openaiRunner) sweep(ctx context.Context, dir string) {
	if r.settings.sweep == nil {
		return
	}
	r.settings.sweep.SweepUnder(context.WithoutCancel(ctx), dir)
}

// openaiEnvelope is the result envelope this adapter reads, holding only the
// fields it uses. Every field is optional at the decoding layer and what is
// required is decided above, so a field the agent stops reporting produces a
// named refusal rather than a decoding error a caller cannot act on.
//
// A field this struct does not name is ignored, so an agent that adds one does
// not cost an otherwise usable result.
//
// This describes what the adapter reads, not what the agent guarantees to
// write. An envelope this adapter cannot read is FailureOutput.
type openaiEnvelope struct {
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Model   string `json:"model"`
	// Every count is a pointer so that a counter the agent omitted stays
	// distinguishable from one it reported as zero. Decoding into a number
	// would collapse the two here, where the difference is still knowable.
	Usage struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// usage converts the reported counts into the recorded form, carrying which of
// them the agent actually reported.
func (e openaiEnvelope) usage() Usage {
	return Usage{
		InputTokens:  reportedCount(e.Usage.PromptTokens),
		OutputTokens: reportedCount(e.Usage.CompletionTokens),
	}
}

// failureText is what the agent said about its own failure. It is content, so
// it reaches an *InvocationError and never a Record.
func (e openaiEnvelope) failureText() string {
	return e.Result
}
