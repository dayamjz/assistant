package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// ErrUnproven is returned by Create for an Isolation whose Proven reports
// false: nothing was asserted about the path, so nothing is launched in it.
var ErrUnproven = errors.New("session: the isolation was not proven by tasks.AssertIsolated")

// ErrNotHeld is returned by Capture, Send and Close for a Session this
// provider did not create or no longer holds, so an operation on another
// provider's handle, or on one already closed, is refused rather than
// answered about some other process.
var ErrNotHeld = errors.New("session: the provider does not hold this session")

// ErrIdentity is returned by Create when the worker started and its identity
// could not be read on a platform that can read one: the process ended
// before it was looked at, or the read failed. The worker is ended before
// the refusal, so no session this provider cannot name goes on running.
var ErrIdentity = errors.New("session: the worker's identity could not be read after it started")

// ErrNotAttachable is AttachReference's answer for a session the provider
// holds whose worker has no terminal a person can attach to, which every
// session Pipe holds is.
var ErrNotAttachable = errors.New("session: this session's worker has no terminal to attach to")

// Host is a Provider as the service that launches through it holds one: the
// five operations, the worker group set the provider produces, and the two
// reads a caller holding only a recorded reference makes, a capture and the
// command a person attaches with. Both reads answer out of the same held set
// Probe answers Live from, so a reference reads Live, captures, and attaches
// through one lookup; every other reference is ErrNotHeld. Pipe and Tmux are
// the two in this build.
//
// It is not a widening of Provider: the five operations stay the whole of
// what PRD section 8's row names, and these three are what a service needs
// of a provider it holds, which a second implementation of the row would
// owe as well.
type Host interface {
	Provider
	// Workers is the worker group set, on the terms Workers states.
	Workers() *Workers
	// CaptureReference is Capture addressed by an encoded reference.
	CaptureReference(ctx context.Context, ref string) (Capture, error)
	// AttachReference is the command line a person runs to attach to the
	// held session ref names, the binary first; ErrNotAttachable for a held
	// session with no terminal, ErrNotHeld for any other reference.
	AttachReference(ref string) ([]string, error)
}

// Provider is PRD section 8's terminal session provider: the five operations
// and nothing else. Pipe is the reference implementation and Tmux the second;
// a further provider implements these five and adds any optional
// behaviour it has as a row in internal/agents' capability table, never as a
// method here.
type Provider interface {
	// Create starts one worker in the proven isolated copy, with the
	// instructions on its standard input, and returns the handle. It refuses
	// an unproven Isolation with ErrUnproven before anything starts, a task
	// identifier home.ValidTaskID refuses with that package's error, and
	// whatever agents.OpenWorker refuses with that package's error wrapped.
	// The caller records Session.Reference through store.SetAttemptSession.
	Create(ctx context.Context, spec Spec) (*Session, error)

	// Capture returns the session's retained output and a digest over its
	// normalized stream, on the terms Capture states. ErrNotHeld for a
	// session this provider does not hold.
	Capture(s *Session) (Capture, error)

	// Send delivers text on the worker's standard input, newline-terminated,
	// on the terms agents.Worker.Send states for ctx and for a worker that
	// has ended. ErrNotHeld for a session this provider does not hold.
	Send(ctx context.Context, s *Session, text string) error

	// Probe answers what the terminal named by a stored reference looks like
	// now, from what this provider can see, on the terms the package
	// documentation states. It never fails: every answer it cannot give is
	// Unobserved with the Reason saying why. ref is the encoded reference as
	// store.TaskAttempt.SessionRef holds it.
	Probe(ctx context.Context, ref string) Observation

	// Close ends the session's worker and releases the handle; a later Probe
	// of its reference answers by identity alone. It returns once the
	// process tree has been ended on the terms agents.Worker.Close states,
	// and ErrNotHeld for a session this provider does not hold, which a
	// second Close is.
	Close(s *Session) error
}

// Spec is what a session is created with.
type Spec struct {
	// TaskID is the task the session belongs to, which the reference
	// embeds. It must satisfy home.ValidTaskID.
	TaskID string
	// Isolation is the proof the copy was asserted isolated. Create refuses
	// one Proven reports false.
	Isolation tasks.Isolation
	// Instructions is what the worker is told first, on the terms
	// agents.WorkerSpec.Instructions states.
	Instructions string
	// Route is the command line prefix the worker reaches the program
	// through, on the terms agents.WorkerSpec.Route states, and empty for a
	// worker given none.
	Route string
	// Model is the model identifier the worker is started with, empty to
	// leave the choice to the agent. It is the adapter's translation of the
	// role's provider, type, and version.
	Model string
	// Effort is the effort the worker is started with, empty when the role
	// named none. It is low, medium, or high when set.
	Effort string
}

// Session is the handle to one terminal session a provider created and
// holds. It is a handle and nothing else: it exposes the reference and no
// route to the worker, the runner, or anything that starts an agent, which
// the route walk in the tests holds it to.
type Session struct {
	ref Reference
	// owner is the provider that created the session, compared by identity.
	owner  any
	worker agents.Worker
	// attach is the command a person runs to attach to the worker's
	// terminal, and nil for a worker that has none.
	attach []string
	// leave takes the worker's group out of the provider's Workers; it is
	// idempotent and runs when the process ends and when Close ends it.
	leave func()
}

// Reference is the session's identity, which the caller records as the
// attempt's session reference.
func (s *Session) Reference() Reference { return s.ref }

// AvailabilityFailure reads this held process's adapter-owned failure without
// copying its transcript. None is absence of failure evidence, not liveness.
func (s *Session) AvailabilityFailure() agents.Failure {
	if s == nil {
		return agents.FailureNone
	}
	return agents.CurrentWorkerFailure(s.worker)
}

// AvailabilityError is AvailabilityFailure's typed account on the terms
// agents.CurrentWorkerError states: nil when the adapter recorded none.
func (s *Session) AvailabilityError() error {
	if s == nil {
		return nil
	}
	return agents.CurrentWorkerError(s.worker)
}

// Capture is what Provider.Capture hands back.
type Capture struct {
	// Failure is the adapter-owned availability state, independent of history.
	Failure agents.Failure
	// Text is the retained output, the most recent of it, interleaved from
	// both streams in the order it was read, within the agents runner's
	// output limit.
	Text string
	// Dropped is how many bytes of earlier output fell off the front to keep
	// Text within the limit; zero means Text is everything, unless
	// Uncounted says otherwise.
	Dropped int64
	// Uncounted is true when output may also have fallen off before it was
	// read, uncounted, which agents.Capture.Uncounted states; a tmux-hosted
	// worker's server keeps a bounded history of its own. A pipe-backed
	// worker never sets it.
	Uncounted bool
	// Digest is Digest(Text): a hash over the normalized stream, equal for
	// two captures whose normalized text is equal. It is over the retained
	// window only.
	Digest string
	// Format is what Text is written in, as the worker's adapter reported
	// it, which is what agents.ReadTranscript reads Text by.
	Format agents.OutputFormat
}

// Reason is why a probe answered what it did. The set is closed, and the
// table in reasons is the one place a Reason's Liveness is decided.
type Reason string

// The reasons. Their liveness is stated beside each and decided by the table
// below, which the tests hold to the rule the package documentation states.
const (
	// ReasonHeld is Live: this provider holds the session and its agent
	// process has not exited.
	ReasonHeld Reason = "held"
	// ReasonExited is Dead: this provider holds the session and its agent
	// process has exited.
	ReasonExited Reason = "exited"
	// ReasonAbsent is Dead: no process has the reference's identifier.
	ReasonAbsent Reason = "absent"
	// ReasonReplaced is Dead: the process with the reference's identifier
	// started at a different time, so it is another process given the same
	// number.
	ReasonReplaced Reason = "replaced"
	// ReasonUnheld is Unobserved: a process with the reference's identifier
	// and start time is present, and this provider holds no handle to it,
	// so nothing can be captured from or sent to it.
	ReasonUnheld Reason = "unheld"
	// ReasonForeignHome is Unobserved: the reference carries another home's
	// marker, and this home answers nothing about another home's sessions.
	ReasonForeignHome Reason = "foreign-home"
	// ReasonMalformed is Unobserved: the reference could not be decoded.
	ReasonMalformed Reason = "malformed"
	// ReasonUnreadable is Unobserved: the start time could not be read, on
	// this platform at all or for this process, or the reference recorded
	// none, so presence of the identifier proves nothing.
	ReasonUnreadable Reason = "unreadable"
)

// reasons is the closed set in order, each with its liveness. It is the one
// owner of the mapping; Observation is built from it and nowhere else.
var reasons = []struct {
	reason   Reason
	liveness taskstate.Liveness
}{
	{ReasonHeld, taskstate.Live},
	{ReasonExited, taskstate.Dead},
	{ReasonAbsent, taskstate.Dead},
	{ReasonReplaced, taskstate.Dead},
	{ReasonUnheld, taskstate.Unobserved},
	{ReasonForeignHome, taskstate.Unobserved},
	{ReasonMalformed, taskstate.Unobserved},
	{ReasonUnreadable, taskstate.Unobserved},
}

// Reasons returns the closed set in table order. The result is a copy.
func Reasons() []Reason {
	out := make([]Reason, len(reasons))
	for i, row := range reasons {
		out[i] = row.reason
	}
	return out
}

// Liveness is the taskstate word this reason maps to. A reason outside the
// table is Unobserved, so a Reason nobody wrote a row for cannot read as
// Dead or Live.
func (r Reason) Liveness() taskstate.Liveness {
	for _, row := range reasons {
		if row.reason == r {
			return row.liveness
		}
	}
	return taskstate.Unobserved
}

// String renders the reason.
func (r Reason) String() string { return string(r) }

// Observation is a probe's answer: the liveness, which is the taskstate word
// a Reader consumes, the reason, and a detail in words for a log.
type Observation struct {
	// Liveness is Reason.Liveness(), carried so a caller reads one field.
	Liveness taskstate.Liveness
	// Reason is why.
	Reason Reason
	// Detail says what was seen, for a person. It never carries output.
	Detail string
}

// observe builds an Observation from the table, so a Liveness is never set
// by hand.
func observe(reason Reason, format string, args ...any) Observation {
	return Observation{Liveness: reason.Liveness(), Reason: reason, Detail: fmt.Sprintf(format, args...)}
}
