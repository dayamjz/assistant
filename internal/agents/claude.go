package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
)

// ClaudeName is the name configuration spells this adapter with.
const ClaudeName = "claude"

// DefaultMaxOutput bounds what one invocation may print on standard output. A
// branch under validation influences how much an agent has to say about it, so
// an unbounded read is memory exhaustion the branch controls. It is larger
// than findings.MaxRawBytes because the report an agent prints arrives inside
// a result envelope this adapter reads first.
const DefaultMaxOutput = 8 << 20

// DefaultTerminationGrace is how long the process tree gets to exit on a
// polite signal before a forceful one is sent.
const DefaultTerminationGrace = 5 * time.Second

// ClaudeOption configures the Claude Code adapter. Options are supplied when
// the factory is built and are fixed for the lifetime of every Runner it
// produces, so every invocation runs under the same configuration.
type ClaudeOption func(*claudeSettings)

type claudeSettings struct {
	bin      string
	base     []string
	baseSet  bool
	grace    time.Duration
	maxOut   int64
	recorder Recorder
	sweep    Sweep
}

// WithBinary names the executable to run. The default is "claude", resolved
// through PATH. A caller that pins an installation, or a test that substitutes
// a stand-in, passes it here.
func WithBinary(path string) ClaudeOption {
	return func(s *claudeSettings) {
		if path != "" {
			s.bin = path
		}
	}
}

// WithBaseEnvironment sets the environment every invocation starts from,
// before an Invocation's own Env is applied over it. The default is the
// process environment; an empty or nil slice given here is a deliberately
// empty base rather than a request for the default. Nothing is filtered out of it: an agent's credentials
// legitimately arrive this way, and what keeps them out of a record is that
// Record has no field they could be written into.
func WithBaseEnvironment(env []string) ClaudeOption {
	return func(s *claudeSettings) {
		s.base = slices.Clone(env)
		s.baseSet = true
	}
}

// WithTerminationGrace sets how long the process tree gets to exit on a polite
// signal before a forceful one is sent. A value of zero or less leaves the
// default in place.
func WithTerminationGrace(d time.Duration) ClaudeOption {
	return func(s *claudeSettings) {
		if d > 0 {
			s.grace = d
		}
	}
}

// WithMaxOutput sets the largest standard output, in bytes, one invocation may
// produce. An invocation that produces more fails with FailureOversize and its
// output is discarded rather than truncated. A value of zero or less leaves
// the default in place.
func WithMaxOutput(n int64) ClaudeOption {
	return func(s *claudeSettings) {
		if n > 0 {
			s.maxOut = n
		}
	}
}

// WithRecorder sets the Recorder every invocation's cost is reported to. The
// default records nothing, and Result.Record carries the same value either
// way.
func WithRecorder(r Recorder) ClaudeOption {
	return func(s *claudeSettings) { s.recorder = r }
}

// WithSweep supplies the identity-based sweep described on Sweep. The default
// is none, and the residual gap that leaves is stated there.
func WithSweep(sw Sweep) ClaudeOption {
	return func(s *claudeSettings) { s.sweep = sw }
}

func newClaudeSettings(opts []ClaudeOption) claudeSettings {
	s := claudeSettings{
		bin:    ClaudeName,
		grace:  DefaultTerminationGrace,
		maxOut: DefaultMaxOutput,
	}
	for _, o := range opts {
		if o != nil {
			o(&s)
		}
	}
	if !s.baseSet {
		s.base = osEnvironment()
	}
	return s
}

// claudeFactory is the Factory for Claude Code.
type claudeFactory struct{ settings claudeSettings }

// ClaudeFactory returns the Factory for the Claude Code adapter. PRD section
// 12 makes it the phase-one adapter because it has a result envelope and
// resumable sessions, so the structured-output path and the fixer-session path
// are both real rather than stubbed.
func ClaudeFactory(opts ...ClaudeOption) Factory {
	return &claudeFactory{settings: newClaudeSettings(opts)}
}

// Name returns "claude".
func (f *claudeFactory) Name() string { return ClaudeName }

// New reports whether Claude Code can be run here, and returns a Runner when
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
func (f *claudeFactory) New(ctx context.Context, args []string) (Runner, error) {
	_ = ctx
	resolved, err := exec.LookPath(f.settings.bin)
	if err != nil {
		return nil, fmt.Errorf("%s is not runnable: %w", f.settings.bin, err)
	}
	return &claudeRunner{
		settings: f.settings,
		resolved: resolved,
		extra:    slices.Clone(args),
	}, nil
}

// claudeRunner runs prompts through one Claude Code installation.
type claudeRunner struct {
	settings claudeSettings
	resolved string
	extra    []string
}

// Name returns "claude".
func (r *claudeRunner) Name() string { return ClaudeName }

// Run executes one invocation with no session. It resumes nothing, and the
// session the agent opens for itself is not kept, so a record it produces
// always reads SessionNone whatever its purpose.
func (r *claudeRunner) Run(ctx context.Context, purpose Purpose, inv Invocation) (Result, error) {
	if !purpose.Recognized() {
		return Result{}, fmt.Errorf("%w: %q", ErrUnrecognizedPurpose, string(purpose))
	}
	res, _, err := r.invoke(ctx, purpose, inv, "", false)
	return res, err
}

// Fixer opens the run's durable fixer session. resume is empty for a new one,
// or the reference a previous Fixer reported.
func (r *claudeRunner) Fixer(ctx context.Context, resume string) (Fixer, error) {
	_ = ctx
	return &claudeFixer{runner: r, reference: resume}, nil
}

// claudeFixer is one run's fixer session. Apply takes no purpose and no
// session, so every invocation it makes is a fix that continues this session
// and nothing else can reach it.
//
// A round whose agent reported no session reference leaves the fixer with
// nothing to continue, and the next round opens a fresh session instead. The
// round's own result stands, because refusing a fix that already edited files
// would be worse than losing the conversation, and the loss is visible rather
// than silent: that round's record says the session was not opened.
type claudeFixer struct {
	runner *claudeRunner

	mu        sync.Mutex
	reference string
}

// Apply runs one fix round in this session, opening it on the first round and
// resuming it afterwards.
func (f *claudeFixer) Apply(ctx context.Context, inv Invocation) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, reference, err := f.runner.invoke(ctx, PurposeFix, inv, f.reference, true)
	if reference != "" {
		f.reference = reference
	}
	return res, err
}

// Reference returns the agent's opaque handle for this session, empty until a
// round has produced one.
func (f *claudeFixer) Reference() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reference
}

// invoke runs one agent process and turns what it printed into a Result or a
// refusal. resume names a session to continue, and keep says whether the
// caller retains the session this invocation reports; only a Fixer passes
// either, which is what makes SessionNone structural for everything else.
//
// It returns the session reference the agent reported when keep is set, so the
// Fixer can carry it to the next round.
func (r *claudeRunner) invoke(ctx context.Context, purpose Purpose, inv Invocation, resume string, keep bool) (Result, string, error) {
	if err := inv.Validate(); err != nil {
		// Nothing started, so nothing cost anything and there is no record to
		// write.
		return Result{}, "", err
	}

	record := Record{
		Purpose: purpose,
		Agent:   ClaudeName,
		Model:   inv.Model,
		Session: SessionNone,
		Started: time.Now(),
	}
	// A resumed session is recorded before the agent runs, because carrying
	// one in is a fact about the invocation whatever becomes of it. Opening
	// one is recorded afterwards, because until the agent reports a reference
	// there is nothing that was opened.
	if keep && resume != "" {
		record.Session = SessionResumed
	}
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
		return Result{}, "", &InvocationError{
			Purpose:  purpose,
			Agent:    ClaudeName,
			Failure:  category,
			ExitCode: code,
			Message:  message,
			Err:      cause,
		}
	}

	proc := runProcess(ctx, procSpec{
		bin:    r.resolved,
		args:   r.arguments(inv, resume),
		stdin:  inv.Prompt,
		dir:    inv.Dir,
		env:    environment(r.settings.base, inv.Env),
		grace:  r.settings.grace,
		maxOut: r.settings.maxOut,
	})
	r.sweep(ctx, inv.Dir)

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
	// The envelope is read before anything is classified, so an agent that
	// reported what it spent has that recorded whichever way it then signalled
	// failure. A failed invocation is exactly the one whose cost is worth
	// knowing, and recording a zero for it would understate what was spent.
	var envelope claudeEnvelope
	decodeErr := json.Unmarshal(proc.stdout, &envelope)
	if decodeErr == nil {
		record.Usage = envelope.usage()
		if envelope.Model != "" {
			record.Model = envelope.Model
		}
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
	if keep && resume == "" && envelope.SessionID != "" {
		record.Session = SessionOpened
	}

	result := Result{Text: envelope.Result}
	if inv.Shape == ShapeReport {
		report, err := findings.ParseReport(envelope.Result)
		if err != nil {
			return fail(&proc, FailureOutput,
				fmt.Errorf("agent's result is not a stage report: %w", err), "")
		}
		result.Report = report
	}

	record.Duration = time.Since(record.Started)
	record.Failure = FailureNone
	r.report(record)
	result.Record = record

	reference := ""
	if keep {
		reference = envelope.SessionID
	}
	return result, reference, nil
}

// arguments builds the command line. The configured entry's own flags come
// first, then the four this run manages: --print, --output-format, --model,
// and --resume.
//
// Of those four, config.ReservedAgentFlags refuses a configured entry that
// names --output-format or --resume, so an entry cannot contradict the two
// that decide how output is read and which session is continued. --print and
// --model are managed here without being reserved there, so an entry may
// carry either: its own copy is placed ahead of the managed one and what a
// repeated flag means is left to the agent's own argument parsing.
//
// The prompt is not on the command line at all. It is written to the agent's
// standard input, so its size is bounded by MaxPromptBytes rather than by an
// argument list, and a prompt that begins with a dash cannot be read as an
// option.
func (r *claudeRunner) arguments(inv Invocation, resume string) []string {
	args := slices.Clone(r.extra)
	args = append(args, "--print", "--output-format", "json")
	if inv.Model != "" {
		args = append(args, "--model", inv.Model)
	}
	if resume != "" {
		args = append(args, "--resume", resume)
	}
	return args
}

// report hands one record to the configured Recorder, if there is one.
func (r *claudeRunner) report(rec Record) {
	if r.settings.recorder != nil {
		r.settings.recorder.RecordInvocation(rec)
	}
}

// sweep calls the identity-based sweep seam, on a context the invocation's own
// cancellation does not cancel, since a cancelled invocation is when a sweep
// matters most.
func (r *claudeRunner) sweep(ctx context.Context, dir string) {
	if r.settings.sweep == nil {
		return
	}
	r.settings.sweep.SweepUnder(context.WithoutCancel(ctx), dir)
}

// claudeEnvelope is the result envelope this adapter reads, holding only the
// fields it uses. Every field is optional at the decoding layer and what is
// required is decided above, so a field the agent stops reporting produces a
// named refusal rather than a decoding error a caller cannot act on.
//
// A field this struct does not name is ignored, so an agent that adds one does
// not cost an otherwise usable result.
//
// This describes what the adapter reads, not what the agent guarantees to
// write. An envelope this adapter cannot read is FailureOutput.
type claudeEnvelope struct {
	Subtype   string `json:"subtype"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
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
func (e claudeEnvelope) usage() Usage {
	return Usage{
		InputTokens:         reportedCount(e.Usage.InputTokens),
		OutputTokens:        reportedCount(e.Usage.OutputTokens),
		CacheReadTokens:     reportedCount(e.Usage.CacheReadInputTokens),
		CacheCreationTokens: reportedCount(e.Usage.CacheCreationInputTokens),
		Turns:               reportedCount(e.NumTurns),
	}
}

// reportedCount turns a decoded count into a Count, leaving one the agent
// omitted or wrote as null unreported.
func reportedCount(n *int64) Count {
	if n == nil {
		return Count{}
	}
	return ReportedCount(*n)
}

// failureText is what the agent said about its own failure. It is content, so
// it reaches an *InvocationError and never a Record.
func (e claudeEnvelope) failureText() string {
	if e.Result != "" {
		return e.Result
	}
	return e.Subtype
}
