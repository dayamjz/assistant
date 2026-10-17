package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
)

// GrokName is the name configuration spells this adapter with.
const GrokName = "grok"

// GrokKeyVariable is the environment variable Grok Build reads as an API key.
// This adapter strips it from every invocation's environment so the CLI
// authenticates with `grok login` credentials rather than the API-key route
// that bills against console.x.ai.
const GrokKeyVariable = "XAI_API_KEY"

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
// empty base rather than a request for the default. GrokKeyVariable is
// stripped at invoke time regardless of what this base carries, so an API key
// in the service environment cannot redirect a run onto the API-key route.
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

// GrokFactory returns the Factory for the Grok adapter. It follows the same
// pattern as Cursor with a result envelope and resumable sessions.
func GrokFactory(opts ...GrokOption) Factory {
	return &grokFactory{settings: newGrokSettings(opts)}
}

// Name returns "grok".
func (f *grokFactory) Name() string { return GrokName }

// AutoEligible reports false: PRD section 12 defers adapters beyond Claude
// Code, so this one resolves only when an entry names it.
func (f *grokFactory) AutoEligible() bool { return false }

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

// grokRunner runs prompts through one Grok Build installation.
type grokRunner struct {
	settings grokSettings
	resolved string
	extra    []string
}

// Name returns "grok".
func (r *grokRunner) Name() string { return GrokName }

// Capabilities is what this adapter declares.
//
// Resumable sessions are declared because the mechanism is here: a fixer's
// rounds after the first carry --resume with the sessionId the envelope
// reported, which arguments and invoke build, and the Fixer method below is
// what makes that reachable.
//
// Instruction suppression is not declared, because nothing here implements it.
// This adapter carries a configured entry's own flags and the flags the run
// manages, and none of those tells Grok to ignore a repository's instruction
// files. Declaring it would buy a run that suppresses nothing while reporting
// that it did, which is the substitution PRD section 8 refuses. Leaving it
// undeclared is what lets internal/pipeline refuse a run that asks for
// suppression against this adapter before it launches one.
func (r *grokRunner) Capabilities() Capabilities {
	return Declare(CapabilityResumableSessions)
}

// Run executes one invocation with no session. It resumes nothing, and the
// session the agent opens for itself is not kept, so a record it produces
// always reads SessionNone whatever its purpose.
func (r *grokRunner) Run(ctx context.Context, purpose Purpose, inv Invocation) (Result, error) {
	if !purpose.Recognized() {
		return Result{}, fmt.Errorf("%w: %q", ErrUnrecognizedPurpose, string(purpose))
	}
	res, _, err := r.invoke(ctx, purpose, inv, session{})
	return res, err
}

// Fixer opens the run's durable fixer session. resume is empty for a new one,
// or the reference a previous Fixer reported.
//
// Having this method is what makes this adapter a SessionRunner, and
// Capabilities declares the same fact. Resolve refuses an adapter where the
// two disagree, so the pair cannot drift apart unnoticed.
func (r *grokRunner) Fixer(ctx context.Context, resume string) (Fixer, error) {
	_ = ctx
	return &grokFixer{runner: r, reference: resume}, nil
}

// grokFixer is one run's fixer session. Apply takes no purpose and no
// session, so every invocation it makes is a fix that continues this session
// and nothing else can reach it.
//
// A round whose agent reported no session reference leaves the fixer with
// nothing to continue, and the next round opens a fresh session instead. The
// round's own result stands, because refusing a fix that already edited files
// would be worse than losing the conversation, and the loss is visible rather
// than silent: that round's record says the session was not opened.
//
// A round that reported one keeps it whether or not the round then succeeded.
// None of this reaches P4. Runner.Run still passes no session in and keeps
// none out, so a review invocation records SessionNone whatever it does, and
// an invocation asking for a review shape is refused here rather than answered
// from this conversation.
type grokFixer struct {
	runner *grokRunner

	mu        sync.Mutex
	reference string
}

// Apply runs one fix round in this session, opening it on the first round and
// resuming it afterwards. An invocation asking for a review shape is refused
// with ErrReviewInFixerSession before anything starts, so this session never
// answers a review.
func (f *grokFixer) Apply(ctx context.Context, inv Invocation) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, reference, err := f.runner.invoke(ctx, PurposeFix, inv, session{resume: f.reference, keep: true})
	if reference != "" {
		f.reference = reference
	}
	return res, err
}

// Reference returns the agent's opaque handle for this session, empty until a
// round has produced one.
func (f *grokFixer) Reference() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reference
}

// invoke runs one agent process and turns what it printed into a Result or a
// refusal.
//
// It returns the session reference the agent reported when the caller keeps
// it, so the Fixer can carry it to the next round. That happens whatever the
// invocation then produced: one place decides what a round did with its
// session, and both the record and the Fixer read that one decision.
func (r *grokRunner) invoke(ctx context.Context, purpose Purpose, inv Invocation, s session) (Result, string, error) {
	validate := inv.Validate
	if s.carried() {
		validate = inv.ValidateForFixer
	}
	if err := validate(); err != nil {
		return Result{}, "", err
	}

	record := Record{
		Purpose: purpose,
		Agent:   GrokName,
		Model:   inv.Model,
		Session: s.use(),
		Started: time.Now(),
		Served:  inv.Served,
	}
	record.noteLaunch(inv)
	reference := ""
	fail := func(res *procResult, category Failure, cause error, message string) (Result, string, error) {
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
		return Result{}, reference, &InvocationError{
			Purpose:  purpose,
			Agent:    GrokName,
			Failure:  category,
			ExitCode: code,
			Message:  message,
			Err:      cause,
		}
	}

	promptFile, err := os.CreateTemp("", "assistant-grok-prompt-*")
	if err != nil {
		return fail(nil, FailureProcess, err, "creating a prompt file")
	}
	promptPath := promptFile.Name()
	defer func() { _ = os.Remove(promptPath) }()
	if _, err := promptFile.WriteString(inv.Prompt); err != nil {
		_ = promptFile.Close()
		return fail(nil, FailureProcess, err, "writing the prompt file")
	}
	if err := promptFile.Close(); err != nil {
		return fail(nil, FailureProcess, err, "closing the prompt file")
	}

	proc := runProcess(ctx, procSpec{
		bin:     r.resolved,
		args:    r.arguments(GrantFor(purpose), inv, s.resume, promptPath),
		dir:     inv.Dir,
		env:     withoutEnvNames(environment(r.settings.base, inv.Env), GrokKeyVariable),
		grace:   r.settings.grace,
		maxOut:  r.settings.maxOut,
		started: inv.Started,
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
	if s.keep && decodeErr == nil {
		if sid := envelope.session(); sid != "" {
			reference = sid
			if !s.resumed() {
				record.Session = SessionOpened
			}
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
	// Grok's auth and other hard failures print {"type":"error","message":…}
	// rather than the success envelope. Treat that as the agent's own failure
	// before looking at the exit status, so the message reaches the caller.
	if decodeErr == nil && envelope.isError() {
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
	text := envelope.resultText()
	if text == "" {
		return fail(&proc, FailureOutput,
			errors.New("agent's result envelope carried no result"), "")
	}

	result := Result{Text: text}
	switch inv.Shape {
	case ShapeReport:
		report, err := findings.ParseReport(text)
		if err != nil {
			return fail(&proc, FailureOutput,
				fmt.Errorf("agent's result is not a stage report: %w", err), "")
		}
		result.Report = report
	case ShapeReview:
		report, binding, err := findings.ParseReviewReport(text, inv.Review)
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
	return result, reference, nil
}

// arguments builds the command line. The configured entry's own flags come
// first, then the grant (grokGrantFlags), then the flags this run manages:
// --prompt-file, --output-format, --model, and --resume.
//
// Grok Build takes the prompt as --prompt-file (or -p/--single), not on
// standard input, so the prompt is written to a temporary file and that path
// is named here. config.ReservedAgentFlags refuses a configured entry that
// names --output-format, --resume, --prompt-file, or --single, so an entry
// cannot contradict how the prompt and output are managed. --model is managed
// here without being reserved there.
//
// GrokKeyVariable is stripped from the environment and --api-key is never
// written, so authentication is the CLI login.
func (r *grokRunner) arguments(grant Grant, inv Invocation, resume, promptPath string) []string {
	args := slices.Clone(r.extra)
	args = append(args, grokGrantFlags(grant)...)
	args = append(args, "--prompt-file", promptPath, "--output-format", "json")
	if inv.Model != "" {
		args = append(args, "--model", inv.Model)
	}
	if resume != "" {
		args = append(args, "--resume", resume)
	}
	return args
}

// grokGrantFlags is Grok Build's translation of a Grant.
//
//   - GrantRead runs in plan mode, which the CLI documents as read-only.
//   - GrantEdit runs in acceptEdits with the shell denied, so a headless fix
//     can write files without a person answering prompts and cannot run
//     commands.
//   - GrantWorker is GrantRead's: this adapter declares no worker capability.
//
// An unrecognized grant is GrantRead's.
func grokGrantFlags(g Grant) []string {
	switch g {
	case GrantEdit:
		return []string{"--permission-mode", "acceptEdits", "--deny", "Bash"}
	}
	return []string{"--permission-mode", "plan"}
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

// grokEnvelope is the result envelope this adapter reads. Grok Build's
// headless --output-format json object uses text and sessionId (camelCase),
// and auth failures use {"type":"error","message":…}. Every field is optional
// at the decoding layer and what is required is decided above.
//
// This describes what the adapter reads, not what the agent guarantees to
// write. An envelope this adapter cannot read is FailureOutput.
type grokEnvelope struct {
	Type       string `json:"type"`
	Message    string `json:"message"`
	Text       string `json:"text"`
	Result     string `json:"result"`
	StopReason string `json:"stopReason"`
	SessionID  string `json:"sessionId"`
	// SessionIDSnake is the Claude-shaped field the test helper still prints.
	// The real CLI uses sessionId; accepting both keeps the conformance suite
	// on the helper binary without inventing a second envelope shape.
	SessionIDSnake string `json:"session_id"`
	Model          string `json:"model"`
	NumTurns       *int64 `json:"num_turns"`
	Usage          struct {
		InputTokens              *int64 `json:"input_tokens"`
		OutputTokens             *int64 `json:"output_tokens"`
		CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func (e grokEnvelope) isError() bool {
	return e.Type == "error"
}

func (e grokEnvelope) session() string {
	if e.SessionID != "" {
		return e.SessionID
	}
	return e.SessionIDSnake
}

func (e grokEnvelope) resultText() string {
	if e.Text != "" {
		return e.Text
	}
	return e.Result
}

func (e grokEnvelope) usage() Usage {
	return Usage{
		InputTokens:         reportedCount(e.Usage.InputTokens),
		OutputTokens:        reportedCount(e.Usage.OutputTokens),
		CacheReadTokens:     reportedCount(e.Usage.CacheReadInputTokens),
		CacheCreationTokens: reportedCount(e.Usage.CacheCreationInputTokens),
		Turns:               reportedCount(e.NumTurns),
	}
}

func (e grokEnvelope) failureText() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Text != "" {
		return e.Text
	}
	return e.Type
}
