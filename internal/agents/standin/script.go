package standin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/findings"
)

// Always is the Step.Times value that answers every matching invocation
// instead of a bounded number of them.
const Always = -1

// HangFor is how long a held reply stays alive. It is far longer than any
// assertion should wait, so a stand-in still running when a test looks is one
// the invocation failed to end, and short enough that a process orphaned by a
// crashed test leaves on its own.
const HangFor = 60 * time.Second

// ExitUnscripted is the status the stand-in exits with when no step of its
// script matched the invocation. It prints nothing on standard output and says
// what it was asked on standard error, so the adapter reports FailureExit with
// that text rather than the run reading a default nobody wrote.
const ExitUnscripted = 3

// Script is what the stand-in agent does, invocation by invocation. It is
// serialized to a file the stand-in process reads, so everything in it has to
// be data: a matcher is a set of fields rather than a function.
type Script struct {
	// Steps are tried in order for every invocation, and the first step that
	// matches and has a use left answers it. Steps that match nothing in
	// particular are therefore answered in the order they are written, one
	// invocation each, which is the ordinary case; a step given a Match and
	// Always answers whichever invocations look like that, however many there
	// are and whenever they arrive.
	Steps []Step `json:"steps"`
}

// Step is one scripted answer and the invocations it is for.
type Step struct {
	// Match narrows which invocations this step answers. The zero Match
	// answers any of them.
	Match Match `json:"match"`
	// Times bounds how many invocations this step may take. The zero value
	// takes one, and Always takes every invocation it matches.
	//
	// It bounds what is taken rather than what is answered: a stand-in claims
	// a use before it records the call and before it replies, so one ended in
	// between has consumed the use and left no Call, and the invocation after
	// it falls through to a later step. Nothing makes the two one act, so a
	// script whose steps must be told apart under cancellation tells them
	// apart with a Match rather than by count.
	Times int `json:"times"`
	// Reply is what the agent does for an invocation this step answers.
	Reply Reply `json:"reply"`
}

// uses is the number of invocations this step may answer, and whether that
// number bounds it at all.
func (s Step) uses() (int, bool) {
	switch {
	case s.Times < 0:
		return 0, false
	case s.Times == 0:
		return 1, true
	default:
		return s.Times, true
	}
}

// Match narrows a Step to some invocations. Every field is compared only when
// it is set, so the zero Match accepts everything and a Match with two fields
// set requires both.
type Match struct {
	// PromptContains requires the prompt on standard input to contain this
	// text. It is a substring rather than a pattern because a stage's prompt is
	// assembled text a test should be naming a landmark in, not matching the
	// whole of.
	PromptContains string `json:"prompt_contains,omitempty"`
	// Model requires the invocation to have asked for this model.
	Model string `json:"model,omitempty"`
	// Resumed, when set, requires the invocation to have carried a session
	// (true) or to have carried none (false). Only a fixer round can carry
	// one, so false is every invocation agents.Runner.Run makes and true is a
	// fixer round after the one that opened the session.
	Resumed *bool `json:"resumed,omitempty"`
}

// answers reports whether this Match accepts the call.
func (m Match) answers(c Call) bool {
	if m.PromptContains != "" && !strings.Contains(c.Prompt, m.PromptContains) {
		return false
	}
	if m.Model != "" && c.Model() != m.Model {
		return false
	}
	if m.Resumed != nil && *m.Resumed != (c.Session() != "") {
		return false
	}
	return true
}

// Resumed returns a Match.Resumed selecting invocations that carried a
// session, which is a fixer round after the first one.
func Resumed() *bool { yes := true; return &yes }

// Fresh returns a Match.Resumed selecting invocations that carried no session.
func Fresh() *bool { no := false; return &no }

// Reply is everything one invocation of the stand-in does: what it prints on
// each stream, how long it stays alive, and what status it exits with. The
// fields compose, so a well-formed envelope followed by a non-zero exit, or a
// complete report from a process that then outlives the invocation, are
// written as one Reply rather than as special cases.
//
// Standard output is written as Stdout, then the encoded Envelope, then Pad
// bytes of filler. The adapter decodes the whole of standard output as one
// JSON document, so anything printed alongside an envelope makes it unreadable
// and is how a Reply describes an agent that talked instead of answering.
//
// Envelope is a pointer, so a Reply assigned to a second variable shares one
// envelope with the first and a field set through either is set on both. The
// With methods derive a Reply that owns its envelope, which is what makes one
// built from a shared base safe to adjust; a plain copy is not.
type Reply struct {
	// Stdout is printed on standard output before the envelope.
	Stdout string `json:"stdout,omitempty"`
	// Envelope is the result envelope, encoded on standard output after
	// Stdout. A nil Envelope prints none, which the adapter reports as
	// FailureOutput unless something else already ended the invocation.
	Envelope *Envelope `json:"envelope,omitempty"`
	// Pad is a number of filler bytes appended to standard output. It is how a
	// reply exceeds the adapter's output limit without a script carrying
	// megabytes of text.
	Pad int64 `json:"pad,omitempty"`
	// Stderr is printed on standard error, where the adapter keeps it as the
	// message on an *agents.InvocationError.
	Stderr string `json:"stderr,omitempty"`
	// Hold is how long the stand-in stays alive after printing, before
	// exiting. It is what a hang is: the invocation ends by being cancelled or
	// by its deadline elapsing rather than by the agent finishing.
	Hold time.Duration `json:"hold,omitempty"`
	// Exit is the status the stand-in exits with once it has printed and
	// waited.
	Exit int `json:"exit,omitempty"`
}

// WithExit returns the reply with a different exit status, so an envelope and
// a non-zero exit can be scripted together.
func (r Reply) WithExit(code int) Reply { r = r.own(); r.Exit = code; return r }

// WithStderr returns the reply with text printed on standard error.
func (r Reply) WithStderr(text string) Reply { r = r.own(); r.Stderr = text; return r }

// WithHold returns the reply held alive for d after it has printed. An
// invocation answered by it ends on its context rather than on the agent.
func (r Reply) WithHold(d time.Duration) Reply { r = r.own(); r.Hold = d; return r }

// own returns the reply with an envelope of its own, so a reply derived from
// another can be adjusted through Envelope without reaching the one it came
// from. Every other field is already a value the copy owns.
func (r Reply) own() Reply {
	if r.Envelope != nil {
		envelope := *r.Envelope
		r.Envelope = &envelope
	}
	return r
}

// Report replies with a well-formed envelope whose result is r, encoded with
// the struct tags internal/findings decodes, so what a test describes is what
// that package reads back. A report with a finding whose action is empty or is
// a word nobody recognizes is written the same way and is how P3's fail-closed
// default is driven from real vocabulary rather than from hand-written JSON.
func Report(r findings.Report) Reply {
	encoded, err := json.Marshal(r)
	if err != nil {
		// Report has no field that can fail to encode, so this is
		// unreachable. Printing the reason as the result keeps a change that
		// made it reachable visible as a refused report rather than as an
		// empty one.
		return Text("standin: encoding the report failed: " + err.Error())
	}
	return Text(string(encoded))
}

// Prose replies with a well-formed envelope whose result is the report inside
// a fenced block after intro, which is the shape an agent that explained
// itself before answering produces. internal/findings reads a report out of
// it, so the difference from Report is what the parser is given, not what a
// stage ends up with.
func Prose(intro string, r findings.Report) Reply {
	body := Report(r)
	return Text(intro + "\n\n```json\n" + body.Envelope.Result + "\n```\n")
}

// Text replies with a well-formed envelope carrying result. It is the answer
// to a ShapeText invocation, and under ShapeReport it is also how output that
// arrived intact and is not a report is scripted.
func Text(result string) Reply {
	return Reply{Envelope: &Envelope{Result: result, Usage: DefaultUsage()}}
}

// Malformed replies with output that is not a result envelope at all. The
// adapter reports FailureOutput naming what it could not decode.
func Malformed(stdout string) Reply { return Reply{Stdout: stdout} }

// Failed replies with an envelope in which the agent reports its own failure,
// carrying message as what it said about it. The adapter reports FailureAgent
// whatever the exit status is, so the status stays available for describing an
// agent whose exit and verdict disagree.
func Failed(message string) Reply {
	return Reply{Envelope: &Envelope{
		IsError: true,
		Result:  message,
		Usage:   DefaultUsage(),
	}}
}

// Oversize replies with more output than the adapter's default limit, and
// therefore more than any smaller limit a runner was built with. A runner
// built with a larger agents.WithMaxOutput needs a Reply whose Pad exceeds
// that instead.
func Oversize() Reply { return Reply{Pad: agents.DefaultMaxOutput + 1} }

// Hang replies with nothing and stays alive for HangFor, so the invocation
// ends by being cancelled or by its deadline elapsing. The stand-in prints
// before it holds, so Report(r).WithHold describes the other case: an agent
// that answered completely and was then ended by something other than itself.
func Hang() Reply { return Reply{Hold: HangFor} }

// Envelope is the result envelope the stand-in prints, in the fields the
// adapter reads. It is the wire contract of internal/agents written from the
// other side; see the package documentation for what checks that the two still
// agree.
type Envelope struct {
	// IsError is the agent's own verdict on the invocation. The adapter treats
	// a true one as a failure whatever the exit status was.
	IsError bool `json:"is_error,omitempty"`
	// Subtype is the envelope's subtype. Empty is "success", or
	// "error_during_execution" when IsError is set. The adapter reads it in
	// one place only: it is what a failure the agent reported no result for
	// says about itself.
	Subtype string `json:"subtype,omitempty"`
	// Result is the agent's final text, which for a ShapeReport invocation is
	// what internal/findings is given.
	Result string `json:"result"`
	// Session is the session identifier the envelope reports. A nil Session
	// reports back the session the invocation asked to resume, or a fresh
	// identifier derived from the invocation's arrival order when it asked for
	// none, which is what an agent that always reports one does. SessionID
	// states one instead, and NoSession reports none, which is the case that
	// leaves a fixer with nothing to continue.
	Session *string `json:"session,omitempty"`
	// Model is the model the envelope reports as having run. Empty reports the
	// model the invocation asked for, or DefaultModel when it asked for none.
	Model string `json:"model,omitempty"`
	// Usage is what the envelope reports the invocation cost. Its counts are
	// written to the wire and read back by the adapter, so an unreported count
	// here is one the adapter reports as unreported.
	Usage agents.Usage `json:"usage"`
}

// DefaultModel is the model an envelope reports when neither it nor the
// invocation named one.
const DefaultModel = "standin-model"

// DefaultUsage is what the envelopes built by this package report they cost.
// The counts are distinct so a test reading one back can tell which field it
// got, and every one of them is reported, so a test that wants an unreported
// count states it by leaving that field's Count zero.
func DefaultUsage() agents.Usage {
	return agents.Usage{
		InputTokens:         agents.ReportedCount(101),
		OutputTokens:        agents.ReportedCount(202),
		CacheReadTokens:     agents.ReportedCount(303),
		CacheCreationTokens: agents.ReportedCount(404),
		Turns:               agents.ReportedCount(5),
	}
}

// SessionID returns an Envelope.Session reporting exactly id.
func SessionID(id string) *string { return &id }

// NoSession returns an Envelope.Session reporting no session at all, which is
// the agent that gives a fixer nothing to continue.
func NoSession() *string { none := ""; return &none }

// wire encodes the envelope as the adapter reads it, resolving the session and
// the model against the call it is answering.
//
// Every key here but "type" is one claudeEnvelope in internal/agents decodes.
// They are spelled out rather than derived from a shared type because the
// adapter's reader is unexported and holds only the fields it uses; what keeps
// the two aligned is a test that reads every one of them back through the
// adapter. "type" is the exception: the adapter's reader does not name it, so
// it is written for the shape a real agent prints and no test can fail on it.
func (e Envelope) wire(c Call) ([]byte, error) {
	subtype := e.Subtype
	if subtype == "" {
		subtype = "success"
		if e.IsError {
			subtype = "error_during_execution"
		}
	}
	session := ""
	switch {
	case e.Session != nil:
		session = *e.Session
	case c.Session() != "":
		session = c.Session()
	default:
		session = fmt.Sprintf("standin-session-%d", c.Seq)
	}
	model := e.Model
	if model == "" {
		model = c.Model()
	}
	if model == "" {
		model = DefaultModel
	}
	return json.Marshal(map[string]any{
		"type":       "result",
		"subtype":    subtype,
		"is_error":   e.IsError,
		"result":     e.Result,
		"session_id": session,
		"model":      model,
		"num_turns":  e.Usage.Turns,
		"usage": map[string]any{
			"input_tokens":                e.Usage.InputTokens,
			"output_tokens":               e.Usage.OutputTokens,
			"cache_read_input_tokens":     e.Usage.CacheReadTokens,
			"cache_creation_input_tokens": e.Usage.CacheCreationTokens,
		},
	})
}
