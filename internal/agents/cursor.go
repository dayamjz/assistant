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

// CursorName is the name configuration spells this adapter with.
const CursorName = "cursor"

// CursorOption configures the Cursor adapter. Options are supplied when
// the factory is built and are fixed for the lifetime of every Runner it
// produces, so every invocation runs under the same configuration.
type CursorOption func(*cursorSettings)

type cursorSettings struct {
	bin      string
	base     []string
	baseSet  bool
	grace    time.Duration
	maxOut   int64
	recorder Recorder
	sweep    Sweep
}

// WithCursorBinary names the executable to run. The default is "cursor", resolved
// through PATH. A caller that pins an installation, or a test that substitutes
// a stand-in, passes it here.
func WithCursorBinary(path string) CursorOption {
	return func(s *cursorSettings) {
		if path != "" {
			s.bin = path
		}
	}
}

// WithCursorBaseEnvironment sets the environment every invocation starts from,
// before an Invocation's own Env is applied over it. The default is the
// process environment; an empty or nil slice given here is a deliberately
// empty base rather than a request for the default. Nothing is filtered out of it: an agent's credentials
// legitimately arrive this way, and what keeps them out of a record is that
// Record has no field they could be written into.
func WithCursorBaseEnvironment(env []string) CursorOption {
	return func(s *cursorSettings) {
		s.base = slices.Clone(env)
		s.baseSet = true
	}
}

// WithCursorTerminationGrace sets how long the process tree gets to exit on a polite
// signal before a forceful one is sent. A value of zero or less leaves the
// default in place.
func WithCursorTerminationGrace(d time.Duration) CursorOption {
	return func(s *cursorSettings) {
		if d > 0 {
			s.grace = d
		}
	}
}

// WithCursorMaxOutput sets the largest standard output, in bytes, one invocation may
// produce. An invocation that produces more fails with FailureOversize and its
// output is discarded rather than truncated. A value of zero or less leaves
// the default in place.
func WithCursorMaxOutput(n int64) CursorOption {
	return func(s *cursorSettings) {
		if n > 0 {
			s.maxOut = n
		}
	}
}

// WithCursorRecorder sets the Recorder every invocation's cost is reported to. The
// default records nothing, and Result.Record carries the same value either
// way.
func WithCursorRecorder(r Recorder) CursorOption {
	return func(s *cursorSettings) { s.recorder = r }
}

// WithCursorSweep supplies the identity-based sweep described on Sweep. The default
// is none, and the residual gap that leaves is stated there.
func WithCursorSweep(sw Sweep) CursorOption {
	return func(s *cursorSettings) { s.sweep = sw }
}

func newCursorSettings(opts []CursorOption) cursorSettings {
	s := cursorSettings{
		bin:    CursorName,
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

// cursorFactory is the Factory for Cursor.
type cursorFactory struct{ settings cursorSettings }

// CursorFactory returns the Factory for the Cursor adapter. It follows the same
// pattern as Claude Code with a result envelope and resumable sessions.
func CursorFactory(opts ...CursorOption) Factory {
	return &cursorFactory{settings: newCursorSettings(opts)}
}

// Name returns "cursor".
func (f *cursorFactory) Name() string { return CursorName }

// New reports whether Cursor can be run here, and returns a Runner when
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
func (f *cursorFactory) New(ctx context.Context, args []string) (Runner, error) {
	_ = ctx
	resolved, err := exec.LookPath(f.settings.bin)
	if err != nil {
		return nil, fmt.Errorf("%s is not runnable: %w", f.settings.bin, err)
	}
	return &cursorRunner{
		settings: f.settings,
		resolved: resolved,
		extra:    slices.Clone(args),
	}, nil
}

// cursorRunner runs prompts through one Cursor installation.
type cursorRunner struct {
	settings cursorSettings
	resolved string
	extra    []string
}

// Name returns "cursor".
func (r *cursorRunner) Name() string { return CursorName }

// Capabilities is what this adapter declares.
//
// Resumable sessions are declared because the mechanism is here: a fixer's
// rounds after the first carry --resume with the session identifier the
// envelope reported, which arguments and invoke build, and the Fixer method
// below is what makes that reachable.
//
// Instruction suppression is not declared, because nothing here implements it.
// This adapter carries a configured entry's own flags and the four the run
// manages, and none of those tells Cursor to ignore a repository's
// instruction files. Declaring it would buy a run that suppresses nothing
// while reporting that it did, which is the substitution PRD section 8 refuses.
// Leaving it undeclared is what lets internal/pipeline refuse a run that asks
// for suppression against this adapter before it launches one.
func (r *cursorRunner) Capabilities() Capabilities {
	return Declare(CapabilityResumableSessions)
}

// Run executes one invocation with no session. It resumes nothing, and the
// session the agent opens for itself is not kept, so a record it produces
// always reads SessionNone whatever its purpose.
func (r *cursorRunner) Run(ctx context.Context, purpose Purpose, inv Invocation) (Result, error) {
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
func (r *cursorRunner) Fixer(ctx context.Context, resume string) (Fixer, error) {
	_ = ctx
	return &cursorFixer{runner: r, reference: resume}, nil
}

// cursorFixer is one run's fixer session. Apply takes no purpose and no
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
// A failing round may already have edited files, so the next round resumes the
// conversation those edits were made in rather than starting blind, which is
// the same reasoning the paragraph above applies to the round that reports
// nothing. What is recorded follows the same fact: SessionOpened is written
// only where this fixer holds the reference.
//
// None of this reaches P4. Runner.Run still passes no session in and keeps
// none out, so a review invocation records SessionNone whatever it does, and
// an invocation asking for a review shape is refused here rather than answered
// from this conversation; the paragraphs here are about which fix rounds share
// one conversation.
type cursorFixer struct {
	runner *cursorRunner

	mu        sync.Mutex
	reference string
}

// Apply runs one fix round in this session, opening it on the first round and
// resuming it afterwards. An invocation asking for a review shape is refused
// with ErrReviewInFixerSession before anything starts, so this session never
// answers a review.
func (f *cursorFixer) Apply(ctx context.Context, inv Invocation) (Result, error) {
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
func (f *cursorFixer) Reference() string {
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
func (r *cursorRunner) invoke(ctx context.Context, purpose Purpose, inv Invocation, s session) (Result, string, error) {
	// A session-carrying invocation is the memory P4 keeps a review out of, so
	// it is put to ValidateForFixer, which is Validate plus the refusal of a
	// review shape there.
	validate := inv.Validate
	if s.carried() {
		validate = inv.ValidateForFixer
	}
	if err := validate(); err != nil {
		// Nothing started, so nothing cost anything and there is no record to
		// write.
		return Result{}, "", err
	}

	// What the invocation carried in is recorded before the agent runs,
	// because carrying a session is a fact about the invocation whatever
	// becomes of it.
	record := Record{
		Purpose: purpose,
		Agent:   CursorName,
		Model:   inv.Model,
		Session: s.use(),
		Started: time.Now(),
	}
	// reference is what this invocation hands back to the Fixer. It is set
	// once, from the envelope, and every exit below returns it, so the record
	// and what the Fixer holds cannot disagree about the session.
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
			Agent:    CursorName,
			Failure:  category,
			ExitCode: code,
			Message:  message,
			Err:      cause,
		}
	}

	proc := runProcess(ctx, procSpec{
		bin:    r.resolved,
		args:   r.arguments(inv, s.resume),
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
	var envelope cursorEnvelope
	decodeErr := json.Unmarshal(proc.stdout, &envelope)
	if decodeErr == nil {
		record.Usage = envelope.usage()
		if envelope.Model != "" {
			record.Model = envelope.Model
		}
	}
	// What this round did with the session is settled here too, once, ahead of
	// every classification below. A round that reported a session hands it
	// back whatever category it then failed in, so the next round continues
	// the conversation it may already have edited files in, and SessionOpened
	// is recorded only where the Fixer really holds the reference.
	if s.keep && decodeErr == nil && envelope.SessionID != "" {
		reference = envelope.SessionID
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
func (r *cursorRunner) arguments(inv Invocation, resume string) []string {
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
func (r *cursorRunner) report(rec Record) {
	if r.settings.recorder != nil {
		r.settings.recorder.RecordInvocation(rec)
	}
}

// sweep calls the identity-based sweep seam, on a context the invocation's own
// cancellation does not cancel, since a cancelled invocation is when a sweep
// matters most.
func (r *cursorRunner) sweep(ctx context.Context, dir string) {
	if r.settings.sweep == nil {
		return
	}
	r.settings.sweep.SweepUnder(context.WithoutCancel(ctx), dir)
}

// cursorEnvelope is the result envelope this adapter reads, holding only the
// fields it uses. Every field is optional at the decoding layer and what is
// required is decided above, so a field the agent stops reporting produces a
// named refusal rather than a decoding error a caller cannot act on.
//
// A field this struct does not name is ignored, so an agent that adds one does
// not cost an otherwise usable result.
//
// This describes what the adapter reads, not what the agent guarantees to
// write. An envelope this adapter cannot read is FailureOutput.
type cursorEnvelope struct {
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
func (e cursorEnvelope) usage() Usage {
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
func (e cursorEnvelope) failureText() string {
	if e.Result != "" {
		return e.Result
	}
	return e.Subtype
}
