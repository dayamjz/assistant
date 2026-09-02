package agents

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
)

// SessionUse says what an invocation did with a run's durable agent session.
// It is the recorded form of P4: the assertion that reviewing and fixing have
// separate memory is answered by reading this field on invocation records,
// which is what PRD section 13 asks for.
type SessionUse string

const (
	// SessionNone is an invocation with no memory. It resumed nothing, and any
	// session the agent opened for it is discarded when it ends. Every
	// invocation Runner.Run makes is this one, whatever its purpose, because
	// Run has nowhere to put a session and nothing to keep one in.
	SessionNone SessionUse = "none"
	// SessionOpened is the fixer round that opened the run's durable session.
	// It is recorded only where the Fixer came away holding the reference, so
	// a round that reported none, and a round refused before the agent could
	// report one, are not this.
	SessionOpened SessionUse = "opened"
	// SessionResumed is a fixer round that resumed it.
	SessionResumed SessionUse = "resumed"
)

// Count is one quantity an agent reported about what an invocation cost, or
// the absence of a report. Its fields are unexported, and the two ways to read
// it both carry whether the agent reported it: Value hands that back alongside
// the number, and MarshalJSON writes a reported count as a number and an
// unreported one as null. A caller therefore cannot read a count without
// learning whether it is a reported one. The zero Count is an unreported
// count.
//
// This exists because a reported zero and a silence are different facts and
// only one of them may be stored as a zero. An int cannot hold that
// difference, so the distinction would be lost here, where the data is born,
// and no layer downstream could recover it.
type Count struct {
	value    int64
	reported bool
}

// ReportedCount is a count the agent reported, including a reported zero.
func ReportedCount(n int64) Count { return Count{value: n, reported: true} }

// Value returns the count and whether the agent reported it. The count is zero
// when it was not reported, and that zero is not a measurement: a caller
// storing it must store an unknown rather than a zero.
func (c Count) Value() (int64, bool) { return c.value, c.reported }

// MarshalJSON writes a reported count as a JSON number and an unreported one
// as null. The type exists to carry that difference to whatever stores it, so
// the encoding it reaches that store through has to keep it: a Count whose
// fields are unexported would otherwise encode to an empty object, which loses
// both the number and the fact that there was one.
func (c Count) MarshalJSON() ([]byte, error) {
	if !c.reported {
		return []byte("null"), nil
	}
	return strconv.AppendInt(nil, c.value, 10), nil
}

// UnmarshalJSON reads back what MarshalJSON wrote: a number is a reported
// count, and null is an unreported one. Anything else is refused rather than
// read as an unreported count, because a count that could not be decoded is
// not the same fact as one nothing was reported for.
func (c *Count) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*c = Count{}
		return nil
	}
	var n int64
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*c = ReportedCount(n)
	return nil
}

// String renders the count for a diagnostic, and says so when there is none.
func (c Count) String() string {
	if !c.reported {
		return "unreported"
	}
	return strconv.FormatInt(c.value, 10)
}

// Usage is what an invocation cost in tokens and turns, as the agent reported
// it. Every field is a Count, so a field the agent did not report reads back
// as unreported rather than as a zero, which is the distinction PRD section
// 8's schema rule needs at the point a caller stores one.
//
// What an agent reports is what is here. This package neither adds to it nor
// checks it: a counter an agent reports wrongly is recorded as reported.
type Usage struct {
	// InputTokens is the prompt tokens the agent reported.
	InputTokens Count
	// OutputTokens is the tokens the agent generated.
	OutputTokens Count
	// CacheReadTokens is prompt tokens served from a cache.
	CacheReadTokens Count
	// CacheCreationTokens is prompt tokens written to a cache.
	CacheCreationTokens Count
	// Turns is how many turns the agent took.
	Turns Count
}

// Record is what is kept about one invocation. PRD section 8 lists it as
// history and fixes its contents: purpose, agent, model, timing, failure
// category, and token usage. Never prompts, outputs, diffs, or credentials.
//
// That is a property of the type, not of its callers. Every field here is
// cost, and there is no field a prompt, an agent's text, a diff, or an
// environment value could be written into. The message explaining a failure
// lives on *InvocationError instead, which is returned to the caller and never
// folded in here.
type Record struct {
	// Purpose is the role the invocation played.
	Purpose Purpose
	// Agent is the name of the agent that ran.
	Agent string
	// Model is the model the invocation asked for, or the one the agent
	// reported when it reported one. It is empty when neither named a model.
	Model string
	// Session is what the invocation did with the run's durable session.
	Session SessionUse
	// Started is when the agent process was started.
	Started time.Time
	// Duration is how long it took to reach a result or a failure.
	Duration time.Duration
	// Failure is the category, FailureNone when the invocation produced a
	// result.
	Failure Failure
	// Usage is what the agent reported it cost.
	Usage Usage
}

// Recorder receives one Record per invocation that got past Invocation.
// Validate, including failed and cancelled ones and including one whose
// process could not be started. An invocation refused before that point, for
// an unrecognized purpose or for an Invocation that cannot be run as written,
// produces no record.
//
// It is called synchronously on the invoking goroutine, so an implementation
// that writes to a database should not block for long. A Recorder shared
// across concurrent runs must be safe for concurrent use; this package makes
// no calls of its own to serialize it.
type Recorder interface {
	// RecordInvocation stores one invocation's cost.
	RecordInvocation(Record)
}

// Result is what an invocation produced.
type Result struct {
	// Text is the agent's final message. It is present for both shapes; for
	// ShapeReport it is the text the report was read out of.
	Text string
	// Report is the validated stage report, present only for ShapeReport. It
	// is never a zero Report accompanied by a nil error: output that did not
	// yield a valid report is an *InvocationError with FailureOutput.
	Report findings.Report
	// Record is what was recorded about this invocation, identical to what a
	// Recorder was given. A refusal returns no Result at all, so this is the
	// record of an invocation that produced something; the record of one that
	// did not still reaches the Recorder.
	Record Record
}
