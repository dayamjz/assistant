package agents

import (
	"errors"
	"strconv"
	"strings"
)

// Errors a caller is expected to handle. Each is a typed result rather than a
// warning execution continues past. Test with errors.Is.
var (
	// ErrInvalidInvocation is returned when an Invocation cannot be run as
	// written. The refusal happens before any process starts.
	ErrInvalidInvocation = errors.New("agents: invocation is not runnable as written")
	// ErrUnrecognizedPurpose is returned when a purpose is not one this
	// package defines. An unrecognized purpose is refused rather than
	// recorded, because a record whose purpose means nothing cannot answer the
	// P4 question asked of invocation records.
	ErrUnrecognizedPurpose = errors.New("agents: unrecognized invocation purpose")
	// ErrNoAgent is returned by Resolve when no entry in the ordered list
	// resolved to a runnable agent. It is a refusal before a run starts, not a
	// degraded run: PRD section 10 requires the run to fail before its first
	// stage rather than report command-only validation as a pass.
	ErrNoAgent = errors.New("agents: no configured agent is runnable")
	// ErrInvocationFailed is the class every *InvocationError belongs to, so a
	// caller that only needs to know that the agent did not deliver can match
	// on one error.
	ErrInvocationFailed = errors.New("agents: invocation did not produce a usable result")
)

// invocationFieldError reports one field of an Invocation that cannot be run
// as written. It wraps ErrInvalidInvocation and names the field.
type invocationFieldError struct {
	field  string
	reason string
}

func (e *invocationFieldError) Error() string {
	return "agents: invalid invocation: " + e.field + ": " + e.reason
}

// Unwrap makes every field refusal match ErrInvalidInvocation.
func (e *invocationFieldError) Unwrap() error { return ErrInvalidInvocation }

// Failure classifies why an invocation did not produce a usable result. It is
// the one thing about a failure that is recorded; the message explaining it is
// not, because an agent writes that message and it is content.
type Failure string

const (
	// FailureNone is recorded for an invocation that produced a result.
	FailureNone Failure = ""
	// FailureProcess is an agent process that could not be run to a complete
	// result: it could not be started, waiting on it failed, or its output was
	// abandoned after the grace period because something still held it open.
	// In the last case the output collected may be missing bytes the agent
	// wrote, so it is refused rather than read as whole.
	FailureProcess Failure = "process"
	// FailureExit is an agent process that exited non-zero.
	FailureExit Failure = "exit"
	// FailureAgent is an agent that ran and reported its own failure in the
	// result it printed.
	FailureAgent Failure = "agent"
	// FailureCancelled is an invocation whose context was cancelled.
	FailureCancelled Failure = "cancelled"
	// FailureTimeout is an invocation whose context deadline elapsed.
	FailureTimeout Failure = "timeout"
	// FailureOversize is an agent that printed more than the configured limit.
	// The output is discarded rather than truncated, because a truncated
	// report read as a whole one is worse than no report.
	FailureOversize Failure = "oversize"
	// FailureOutput is output that did not have the shape the invocation asked
	// for: not a result envelope, an envelope with no result in it, or, for
	// ShapeReport, text internal/findings refused to read as a report.
	FailureOutput Failure = "output"
)

// InvocationError reports an invocation that did not produce a usable result.
// It names the purpose, the agent, and the failure category, so a caller can
// decide what to do without reading prose.
//
// Message is bounded text the agent itself wrote. It is content, not cost: it
// belongs in a log a person reads, and it must not be copied into an
// invocation record. Nothing in this package puts it in one.
type InvocationError struct {
	// Purpose is the role the invocation was playing.
	Purpose Purpose
	// Agent is the name of the agent that ran, or was to run.
	Agent string
	// Failure is the category. It is never FailureNone.
	Failure Failure
	// ExitCode is the agent's exit status, or -1 when it never reported one.
	ExitCode int
	// Message is what the agent wrote about the failure, bounded as it was
	// read. It is empty when the agent wrote nothing.
	Message string
	// Err is the underlying cause when there is one: the error that ended the
	// process, the context's error, or the refusal internal/findings raised
	// over the agent's output. It stays matchable with errors.Is.
	Err error
}

// Error renders the purpose, the agent, the category, and what is known about
// the cause.
func (e *InvocationError) Error() string {
	var b strings.Builder
	b.WriteString("agents: " + string(e.Purpose) + " invocation of " + e.Agent + " failed (" + string(e.Failure) + ")")
	if e.ExitCode >= 0 {
		b.WriteString(": exited " + strconv.Itoa(e.ExitCode))
	}
	if e.Err != nil {
		b.WriteString(": " + strings.ReplaceAll(e.Err.Error(), "\n", "; "))
	}
	if e.Message != "" {
		b.WriteString(": " + strings.ReplaceAll(e.Message, "\n", "; "))
	}
	return b.String()
}

// Unwrap returns the cause alongside ErrInvocationFailed, so a caller can
// match either the class or the specific refusal underneath it.
func (e *InvocationError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrInvocationFailed}
	}
	return []error{ErrInvocationFailed, e.Err}
}

// Unavailability is one entry of an ordered agent list that did not resolve,
// and why. Resolve reports every one it passed over, so a person can see that
// the agent they configured was skipped rather than silently substituted.
type Unavailability struct {
	// Entry is the configuration entry as written, such as "claude --model x".
	Entry string
	// Name is the agent name the entry began with.
	Name string
	// Err says why it did not resolve.
	Err error
}

// ResolutionError reports that no entry in an ordered agent list resolved. It
// wraps ErrNoAgent and lists what was tried.
type ResolutionError struct {
	// Tried is every entry that was considered, in order, with its reason.
	Tried []Unavailability
}

// Error names every entry that was tried and why each one did not resolve.
func (e *ResolutionError) Error() string {
	var b strings.Builder
	b.WriteString("agents: no configured agent is runnable")
	if len(e.Tried) == 0 {
		b.WriteString(": no agent was configured")
		return b.String()
	}
	for _, t := range e.Tried {
		b.WriteString("; " + t.Entry + ": " + t.Err.Error())
	}
	return b.String()
}

// Unwrap makes every resolution refusal match ErrNoAgent.
func (e *ResolutionError) Unwrap() error { return ErrNoAgent }
