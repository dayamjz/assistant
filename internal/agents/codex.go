package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
)

// CodexName is the name configuration spells this adapter with.
const CodexName = "codex"

// CodexKeyVariable is the environment variable the Codex CLI may read as an
// API key. This adapter strips it from every invocation's environment so the
// CLI authenticates with `codex login` credentials rather than an ambient
// key in the service process. A key set only for the service therefore does
// not redirect Codex runs onto a different billing path than the operator
// logged in with.
const CodexKeyVariable = "OPENAI_API_KEY"

// CodexOption configures the Codex adapter. Options are supplied when the
// factory is built and are fixed for the lifetime of every Runner it
// produces, so every invocation runs under the same configuration.
type CodexOption func(*codexSettings)

type codexSettings struct {
	bin      string
	base     []string
	baseSet  bool
	grace    time.Duration
	maxOut   int64
	recorder Recorder
	sweep    Sweep
}

// WithCodexBinary names the executable to run. The default is "codex",
// resolved through PATH. A caller that pins an installation, or a test that
// substitutes a stand-in, passes it here.
func WithCodexBinary(path string) CodexOption {
	return func(s *codexSettings) {
		if path != "" {
			s.bin = path
		}
	}
}

// WithCodexBaseEnvironment sets the environment every invocation starts from,
// before an Invocation's own Env is applied over it. The default is the
// process environment; an empty or nil slice given here is a deliberately
// empty base rather than a request for the default. CodexKeyVariable is
// stripped at invoke time regardless of what this base carries.
func WithCodexBaseEnvironment(env []string) CodexOption {
	return func(s *codexSettings) {
		s.base = slices.Clone(env)
		s.baseSet = true
	}
}

// WithCodexTerminationGrace sets how long the process tree gets to exit on a
// polite signal before a forceful one is sent. A value of zero or less leaves
// the default in place.
func WithCodexTerminationGrace(d time.Duration) CodexOption {
	return func(s *codexSettings) {
		if d > 0 {
			s.grace = d
		}
	}
}

// WithCodexMaxOutput sets the largest standard output, in bytes, one
// invocation may produce. An invocation that produces more fails with
// FailureOversize and its output is discarded rather than truncated. A value
// of zero or less leaves the default in place.
func WithCodexMaxOutput(n int64) CodexOption {
	return func(s *codexSettings) {
		if n > 0 {
			s.maxOut = n
		}
	}
}

// WithCodexRecorder sets the Recorder every invocation's cost is reported to.
// The default records nothing, and Result.Record carries the same value
// either way.
func WithCodexRecorder(r Recorder) CodexOption {
	return func(s *codexSettings) { s.recorder = r }
}

// WithCodexSweep supplies the identity-based sweep described on Sweep. The
// default is none, and the residual gap that leaves is stated there.
func WithCodexSweep(sw Sweep) CodexOption {
	return func(s *codexSettings) { s.sweep = sw }
}

func newCodexSettings(opts []CodexOption) codexSettings {
	s := codexSettings{
		bin:    CodexName,
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

// codexFactory is the Factory for Codex.
type codexFactory struct{ settings codexSettings }

// CodexFactory returns the Factory for the Codex CLI adapter. Codex supports
// resumable sessions through `codex exec resume`, so the Runner this factory
// returns also implements SessionRunner.
func CodexFactory(opts ...CodexOption) Factory {
	return &codexFactory{settings: newCodexSettings(opts)}
}

// Name returns "codex".
func (f *codexFactory) Name() string { return CodexName }

// AutoEligible reports false: PRD section 12 defers adapters beyond Claude
// Code, so this one resolves only when an entry names it.
func (f *codexFactory) AutoEligible() bool { return false }

// New reports whether Codex can be run here, and returns a Runner when it can.
//
// What availability means here is exactly one thing: the named executable
// resolves to a file this process may execute. An installation that resolves
// and then fails on its first invocation, because it is unauthenticated or of
// an incompatible version, is not caught here; it surfaces as a failed
// invocation with the agent's own message.
func (f *codexFactory) New(ctx context.Context, args []string) (Runner, error) {
	_ = ctx
	resolved, err := exec.LookPath(f.settings.bin)
	if err != nil {
		return nil, fmt.Errorf("%s is not runnable: %w", f.settings.bin, err)
	}
	return &codexRunner{
		settings: f.settings,
		resolved: resolved,
		extra:    slices.Clone(args),
	}, nil
}

// codexRunner runs prompts through one Codex installation.
type codexRunner struct {
	settings codexSettings
	resolved string
	extra    []string
}

// Name returns "codex".
func (r *codexRunner) Name() string { return CodexName }

// Capabilities is what this adapter declares.
//
// Resumable sessions are declared because the mechanism is here: a fixer's
// rounds after the first run `codex exec resume <thread_id>`, which
// arguments and invoke build, and the Fixer method below is what makes that
// reachable.
//
// Worker sessions are declared because Start runs `codex app-server` and
// speaks the pipe protocol recorded from CLI 0.163.0. That thread is not
// the fixer session. Terminal workers are not declared.
//
// Instruction suppression is not declared, because nothing here implements
// it. Leaving it undeclared is what lets internal/pipeline refuse a run that
// asks for suppression against this adapter before it launches one.
func (r *codexRunner) Capabilities() Capabilities {
	return Declare(CapabilityResumableSessions, CapabilityWorkerSessions)
}

// Run executes one invocation with no session. It resumes nothing, and the
// session the agent opens for itself is not kept, so a record it produces
// always reads SessionNone whatever its purpose.
func (r *codexRunner) Run(ctx context.Context, purpose Purpose, inv Invocation) (Result, error) {
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
func (r *codexRunner) Fixer(ctx context.Context, resume string) (Fixer, error) {
	_ = ctx
	return &codexFixer{runner: r, reference: resume}, nil
}

// codexFixer is one run's fixer session. Apply takes no purpose and no
// session, so every invocation it makes is a fix that continues this session
// and nothing else can reach it.
type codexFixer struct {
	runner *codexRunner

	mu        sync.Mutex
	reference string
}

// Apply runs one fix round in this session, opening it on the first round and
// resuming it afterwards. An invocation asking for a review shape is refused
// with ErrReviewInFixerSession before anything starts, so this session never
// answers a review.
func (f *codexFixer) Apply(ctx context.Context, inv Invocation) (Result, error) {
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
func (f *codexFixer) Reference() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reference
}

// invoke runs one agent process and turns what it printed into a Result or a
// refusal.
func (r *codexRunner) invoke(ctx context.Context, purpose Purpose, inv Invocation, s session) (Result, string, error) {
	validate := inv.Validate
	if s.carried() {
		validate = inv.ValidateForFixer
	}
	if err := validate(); err != nil {
		return Result{}, "", err
	}

	if err := AcceptsEffort(CodexName, inv.Effort); err != nil {
		return Result{}, "", err
	}

	record := Record{
		Purpose: purpose,
		Agent:   CodexName,
		Model:   inv.Model,
		Session: s.use(),
		Started: time.Now(),
		Served:  inv.Served,
	}
	record.noteLaunch(inv)
	reference := ""
	fail := func(res *procResult, category Failure, cause error, message string) (Result, string, error) {
		record.Duration = time.Since(record.Started)
		if message == "" && res != nil {
			message = res.stderr
		}
		category, typed := availabilityCategory(category, message)
		cause = withCause(cause, typed)
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
			Agent:    CodexName,
			Failure:  category,
			ExitCode: code,
			Message:  message,
			Err:      cause,
		}
	}

	// Codex reads a dangling inherited stdin as more prompt input. runProcess
	// always closes stdin after writing spec.stdin (empty here → immediate EOF).
	proc := runProcess(ctx, procSpec{
		bin:     r.resolved,
		args:    r.arguments(GrantFor(purpose), inv, s.resume),
		dir:     inv.Dir,
		env:     withoutEnvNames(environment(r.settings.base, inv.Env), CodexKeyVariable),
		grace:   r.settings.grace,
		maxOut:  r.settings.maxOut,
		started: inv.Started,
	})
	r.sweep(ctx, inv.Dir)

	decoded, decodeErr := decodeCodexOutput(proc.stdout)
	if decodeErr == nil {
		record.Usage = decoded.usage
		if decoded.model != "" {
			record.Model = decoded.model
		}
	}
	if s.keep && decodeErr == nil && decoded.threadID != "" {
		reference = decoded.threadID
		if !s.resumed() {
			record.Session = SessionOpened
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
	if decodeErr == nil && decoded.agentError != "" {
		return fail(&proc, FailureAgent,
			errors.New("agent reported its own failure"), decoded.agentError)
	}
	if proc.code != 0 {
		return fail(&proc, FailureExit, nil, "")
	}
	if decodeErr != nil {
		return fail(&proc, FailureOutput,
			fmt.Errorf("agent did not print a Codex result: %w", decodeErr), "")
	}
	if decoded.text == "" {
		return fail(&proc, FailureOutput,
			errors.New("agent's Codex output carried no agent message"), "")
	}

	result := Result{Text: decoded.text}
	switch inv.Shape {
	case ShapeReport:
		report, err := findings.ParseReport(decoded.text)
		if err != nil {
			return fail(&proc, FailureOutput,
				fmt.Errorf("agent's result is not a stage report: %w", err), "")
		}
		result.Report = report
	case ShapeReview:
		report, binding, err := findings.ParseReviewReport(decoded.text, inv.Review)
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

// arguments builds the command line for `codex exec`.
//
// Layout for a new session: configured entry flags, then `exec`, then managed
// flags (--json, --color never, grant flags, --model, effort config), then the prompt after
// `--`. Layout for a resume: the same managed flags, then the `resume`
// subcommand and thread id, then the prompt after `--`. Grant and color flags
// stay before `resume` because Codex's clap schema marks only some exec flags
// global; --sandbox and --color are parent options and must precede the
// subcommand. The prompt is a trailing argv operand after `--` so a prompt
// that begins with a dash cannot be read as an option.
// config.ReservedAgentFlags refuses a configured entry that names --json,
// --sandbox, --resume, or the dangerous bypass flags, so an entry cannot
// contradict how output and grants are managed.
//
// CodexKeyVariable is stripped from the environment; authentication is the
// CLI login.
func (r *codexRunner) arguments(grant Grant, inv Invocation, resume string) []string {
	args := slices.Clone(r.extra)
	args = append(args, "exec", "--json", "--color", "never")
	args = append(args, codexGrantFlags(grant)...)
	if inv.Model != "" {
		args = append(args, "--model", inv.Model)
	}
	if inv.Effort != "" {
		args = append(args, "--config", "model_reasoning_effort=\""+inv.Effort+"\"")
	}
	if resume != "" {
		args = append(args, "resume", resume)
	}
	args = append(args, "--", inv.Prompt)
	return args
}

// codexGrantFlags is Codex's translation of a Grant.
//
//   - GrantRead runs with --sandbox read-only.
//   - GrantEdit runs with --approve-for-me alone. That flag sets workspace-write
//     and routes approval requests through Codex automatic review; Codex's CLI
//     refuses combining it with --sandbox (ArgumentConflict). Residual gap
//     versus Claude's GrantEdit: shell still goes through that automatic
//     review rather than being denied outright, and this package does not pass
//     --dangerously-bypass-approvals-and-sandbox (reserved; wider than an
//     edit grant).
//   - GrantWorker is not a flag on this command line. A pipe worker's
//     app-server thread is started workspace-write with on-request approval,
//     and codexworker.go decides each approval. `codex exec` is review and
//     fix, which are never GrantWorker.
//
// An unrecognized grant is GrantRead's.
func codexGrantFlags(g Grant) []string {
	switch g {
	case GrantEdit:
		return []string{"--approve-for-me"}
	}
	return []string{"--sandbox", "read-only"}
}

func (r *codexRunner) report(rec Record) {
	if r.settings.recorder != nil {
		r.settings.recorder.RecordInvocation(rec)
	}
}

func (r *codexRunner) sweep(ctx context.Context, dir string) {
	if r.settings.sweep == nil {
		return
	}
	r.settings.sweep.SweepUnder(context.WithoutCancel(ctx), dir)
}

// codexDecoded is what decodeCodexOutput extracts from one invocation's
// stdout: the last agent message, the thread id for resume, usage, and any
// agent-reported error text.
type codexDecoded struct {
	text       string
	threadID   string
	model      string
	usage      Usage
	agentError string
}

// decodeCodexOutput reads Codex `exec --json` JSONL events. It also accepts
// the single Claude-shaped result object the conformance helper still prints,
// so the suite can drive this adapter without a second envelope shape; that
// path is not what the real CLI emits.
func decodeCodexOutput(stdout []byte) (codexDecoded, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return codexDecoded{}, errors.New("empty stdout")
	}
	// Real Codex: one JSON object per line.
	if bytes.Contains(trimmed, []byte("\n")) || bytes.Contains(trimmed, []byte(`"thread.started"`)) ||
		bytes.Contains(trimmed, []byte(`"item.completed"`)) {
		return decodeCodexJSONL(trimmed)
	}
	// Helper / Claude-shaped single envelope for conformance.
	var helper struct {
		Type      string `json:"type"`
		IsError   bool   `json:"is_error"`
		Result    string `json:"result"`
		SessionID string `json:"session_id"`
		Model     string `json:"model"`
		NumTurns  *int64 `json:"num_turns"`
		Usage     struct {
			InputTokens              *int64 `json:"input_tokens"`
			OutputTokens             *int64 `json:"output_tokens"`
			CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(trimmed, &helper); err != nil {
		return decodeCodexJSONL(trimmed)
	}
	if helper.Type == "error" {
		return codexDecoded{agentError: firstNonEmpty(helper.Message, helper.Type)}, nil
	}
	out := codexDecoded{
		text:     helper.Result,
		threadID: helper.SessionID,
		model:    helper.Model,
		usage: Usage{
			InputTokens:         reportedCount(helper.Usage.InputTokens),
			OutputTokens:        reportedCount(helper.Usage.OutputTokens),
			CacheReadTokens:     reportedCount(helper.Usage.CacheReadInputTokens),
			CacheCreationTokens: reportedCount(helper.Usage.CacheCreationInputTokens),
			Turns:               reportedCount(helper.NumTurns),
		},
	}
	if helper.IsError {
		out.agentError = firstNonEmpty(helper.Result, "error")
	}
	if out.text == "" && out.agentError == "" {
		return decodeCodexJSONL(trimmed)
	}
	return out, nil
}

// decodeCodexJSONL reads the events of one `codex exec --json` turn. The
// turn's own outcome decides its failure: a turn.failed's message is the
// agent's error, and a turn.completed means the error events before it were
// the retries Codex CLI 0.163.0 announces as "Reconnecting... N/5" and
// recovered from. Only a stream that reports neither outcome falls back on
// the last error event, which is the shape a CLI that stopped mid-turn
// leaves. The failing half is recorded in testdata/codex-exec-*-0.163.0.jsonl;
// a turn that recovered after a retry was not observed, and is read this way
// because a turn.completed is the CLI saying the turn finished.
func decodeCodexJSONL(stdout []byte) (codexDecoded, error) {
	var out codexDecoded
	sawEvent := false
	lastError, failed, completed := "", "", false
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev codexEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return codexDecoded{}, fmt.Errorf("JSONL line: %w", err)
		}
		sawEvent = true
		switch ev.Type {
		case "thread.started":
			if ev.ThreadID != "" {
				out.threadID = ev.ThreadID
			}
		case "item.completed":
			if ev.Item != nil && ev.Item.Type == "agent_message" && ev.Item.Text != "" {
				out.text = ev.Item.Text
			}
		case "turn.failed":
			message := ""
			if ev.Error != nil {
				message = ev.Error.Message
			}
			failed = firstNonEmpty(message, ev.Type)
		case "turn.completed":
			completed = true
			if ev.Usage != nil {
				out.usage = Usage{
					InputTokens:         reportedCount(ev.Usage.InputTokens),
					OutputTokens:        reportedCount(ev.Usage.OutputTokens),
					CacheReadTokens:     reportedCount(ev.Usage.CachedInputTokens),
					CacheCreationTokens: reportedCount(ev.Usage.CacheWriteInputTokens),
				}
			}
		case "error":
			lastError = firstNonEmpty(ev.Message, ev.Type)
		}
	}
	if !sawEvent {
		return codexDecoded{}, errors.New("no JSONL events")
	}
	switch {
	case failed != "":
		out.agentError = failed
	case !completed:
		out.agentError = lastError
	}
	return out, nil
}

// codexEvent is one JSONL object from `codex exec --json`.
type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
	Item *struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Usage *struct {
		InputTokens           *int64 `json:"input_tokens"`
		CachedInputTokens     *int64 `json:"cached_input_tokens"`
		CacheWriteInputTokens *int64 `json:"cache_write_input_tokens"`
		OutputTokens          *int64 `json:"output_tokens"`
		ReasoningOutputTokens *int64 `json:"reasoning_output_tokens"`
	} `json:"usage"`
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
