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
	// Worker is what a stand-in started as a worker does, line by line. It
	// is read by a process the scripted adapter started through
	// agents.WorkerRunner.Start and by no one-shot invocation, so a script
	// may carry both halves and each answers its own kind of process. A nil
	// Worker is a worker that answers no line, which still reads and still
	// lingers.
	Worker *WorkerScript `json:"worker,omitempty"`
}

// WorkerScript is what the stand-in does when it is started as a worker: a
// long-lived process that reads its standard input line by line and replies
// as the exchanges say. It reads the instructions the adapter delivers first
// as lines like any other, because that is all the wire carries: nothing on
// it marks where the instructions end and the text sent afterwards begins.
type WorkerScript struct {
	// Greeting is printed on standard output, followed by a newline, before
	// the first line is read. Empty prints nothing.
	Greeting string `json:"greeting,omitempty"`
	// Exchanges are tried in order for every line read, and the first one
	// that matches answers it, however many lines match it. A line no
	// exchange matches is answered with nothing on standard output and a
	// note on standard error, so a worker's silence is never a default
	// nobody wrote.
	Exchanges []Exchange `json:"exchanges,omitempty"`
	// Exit is the status the worker exits with once its standard input has
	// been closed.
	Exit int `json:"exit,omitempty"`
	// Startup is how long an interactive worker takes to draw its interface
	// after it starts, during which an Enter it reads is dropped. Zero is
	// DefaultStartup. Line and stream workers do not read it.
	Startup time.Duration `json:"startup,omitempty"`
	// Turn is how long an interactive worker shows a turn as running before
	// it answers, during which a message submitted to it is queued. Zero is
	// DefaultTurn. Line and stream workers do not read it.
	Turn time.Duration `json:"turn,omitempty"`
}

// DefaultStartup and DefaultTurn are an interactive worker's Startup and
// Turn when a script leaves them zero. They are shorter than what the
// Claude Code CLI took when it was observed, about three seconds to draw
// its interface against a local server, so a test pays little for them,
// and long enough that a typist who does not wait for the interface, or
// for a turn to end, is caught by them.
const (
	DefaultStartup = 300 * time.Millisecond
	DefaultTurn    = 200 * time.Millisecond
)

// Exchange is one scripted reply to a line a worker reads.
type Exchange struct {
	// Contains requires the line to contain this text. Empty matches every
	// line.
	Contains string `json:"contains,omitempty"`
	// Reply is printed on standard output, followed by a newline, for a line
	// this exchange answers.
	Reply string `json:"reply"`
	// Ends makes the worker exit, with WorkerScript.Exit, once it has
	// printed Reply, without waiting for its input to close. It is how a
	// script says the agent finished on its own, which is a shape a real
	// worker produces and the one a liveness probe has to tell from a
	// worker that was ended by its caller.
	Ends bool `json:"ends,omitempty"`
	// Run makes the worker run a shell command before it answers: the rest
	// of the line after the first occurrence of Contains, which must
	// therefore be set, given to /bin/sh -c in the worker's own working
	// directory, in a process group of its own that the worker does not
	// lead, which is the shape #225 observed of a command the Claude Code
	// CLI's Bash tool runs. The worker waits for the command and answers
	// with its Ran, encoded as one line of JSON, in place of Reply, which
	// must therefore be empty; a script breaking either rule is refused when
	// the Agent is built. The command's standard input is empty. runCommand
	// says what it does not model, and bashCheck the one refusal of the
	// CLI's it does.
	Run bool `json:"run,omitempty"`
	// Write makes the worker write a file before it answers, the way the
	// Claude Code CLI's file editing tool writes one: the rest of the line
	// after the first occurrence of Contains, which must therefore be set,
	// is a path on its first line and the file's content after that line.
	// The path is resolved against the worker's own working directory, and
	// one that is absolute or resolves outside that directory is refused
	// without writing, which is #267's observation that writing outside the
	// copy needed an approval nobody was there to give. The worker answers
	// with its Wrote, encoded as one line of JSON, in place of Reply, which
	// must therefore be empty, and an exchange may not both Run and Write; a
	// script breaking either rule is refused when the Agent is built.
	Write bool `json:"write,omitempty"`
	// Recorded are lines printed on standard output verbatim, each followed
	// by a newline, in place of the answer: in stream mode in place of the
	// three lines a turn otherwise prints, in app-server mode after the
	// turn/start result and in place of the agent message and the idle
	// that otherwise follow it, and in line mode in place of Reply. With
	// Ends, an app-server that plays back a partial turn exits before the
	// turn settles, which is the app-server dying mid-turn. It is how a
	// test plays back output a real agent was recorded
	// writing, so what it prints is that agent's shape rather than one the
	// stand-in made up, which is the test's to keep true of what it plays.
	// Reply must be empty and Run and Write unset beside it, and no line
	// may carry a newline; a script breaking either rule is refused when the
	// Agent is built.
	Recorded []string `json:"recorded,omitempty"`
	// Refuse makes a JSON-RPC worker answer the request that carried the
	// line with a JSON-RPC error whose message is this text, in place of a
	// result: in app-server mode the turn/start, in ACP mode the
	// session/prompt. Nothing else is printed for that turn, so no
	// turn/completed, idle or agent message follows it, which is the shape
	// of a server that refuses a request rather than failing a turn it
	// accepted. The error's code is -32603, JSON-RPC's internal error,
	// chosen here rather than recorded from a CLI; both adapters read only
	// the message. Line, stream and interactive workers have no request to
	// answer that way and exit rather than print anything for such an
	// exchange. Reply, Run, Write, Recorded and Ends must be unset beside
	// it; a script breaking that rule is refused when the Agent is built.
	Refuse string `json:"refuse,omitempty"`
}

// Wrote is what a worker answers for a line a Write exchange took: the path
// as the line named it and, when nothing was written, why.
type Wrote struct {
	// Path is the path as the line named it.
	Path string `json:"wrote"`
	// Refused is why nothing was written, and empty when the file was.
	Refused string `json:"refused,omitempty"`
}

// Ran is what a worker answers for a line a Run exchange took: the command
// it ran, how that ended, what it wrote, and the process group it ran in, as
// the worker read it from the operating system. A test decodes it from the
// worker's output, which is the only place it exists.
type Ran struct {
	// Command is the command as the worker gave it to the shell.
	Command string `json:"ran"`
	// Exit is the shell's exit status, or -1 when it did not start or was
	// ended by a signal.
	Exit int `json:"exit"`
	// Stdout and Stderr are what the command wrote to each.
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	// Group is the process group the shell ran in, read after it started
	// and before the worker waited for it, so a shell that has already
	// exited still reports the group it led (#360), and zero where this
	// platform reports none or the read failed.
	Group int `json:"group"`
	// Refused is why the command was not run at all, the refusal bashCheck
	// models, and empty for a command the worker ran. A refused command has
	// Exit -1 and no group.
	Refused string `json:"refused,omitempty"`
}

// answers reports whether this exchange accepts the line.
func (e Exchange) answers(line string) bool {
	return e.Contains == "" || strings.Contains(line, e.Contains)
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

// The two landmarks a review invocation's prompt is recognized and read by.
// Both are sentences findings.Demand.Guidance writes, quoted here rather than
// computed, because the stand-in reads the prompt the way the reviewer it
// replaces does: off the text the invocation carries. A change to that
// package's wording breaks these loudly - the derived report is refused and
// the invocation fails naming the missing landmark - rather than quietly
// answering with a report the binding would refuse.
const (
	// reviewRevisionPrefix precedes the one revision a review report may
	// carry.
	reviewRevisionPrefix = "It must be exactly "
	// reviewRevisionSuffix follows it.
	reviewRevisionSuffix = ". A report of any other revision is refused whole"
	// reviewTouchedHeading precedes the list of paths the change touched.
	reviewTouchedHeading = "The change touches these paths."
)

// MatchReview returns a Match selecting review invocations: the ones whose
// prompt carries the evidence demand a review report is bound to. It is the
// same landmark Reviewed derives the revision from, so an invocation this
// selects is one that reply can answer.
func MatchReview() Match {
	return Match{PromptContains: reviewRevisionPrefix}
}

// Reviewed replies with a review of whatever change the invocation carries:
// a report whose revision and read set are derived from the evidence demand
// in the prompt, and whose findings are exactly those given, so none makes it
// a clean review that approves the change.
//
// It exists because a review report is checked against the run's own facts,
// the commit under review and the paths it touched, and a script is data
// written before the run those facts belong to. A test that knows its head
// states a report with Report instead; one that drives the product surface,
// where the head is the run's to know, answers with this.
//
// The derivation happens in the stand-in process, off the prompt on standard
// input, which is the same text the reviewer this stands in for is told those
// facts through. An invocation whose prompt carries no evidence demand cannot
// be answered this way and fails with a message naming what was missing, so a
// step that matched too broadly is a loud failure rather than a fabricated
// review.
func Reviewed(summary string, found ...findings.Finding) Reply {
	return Reply{Review: &Review{Summary: summary, Findings: found}}
}

// Review is the part of a derived review answer the script states; Reviewed
// builds one and Reply.Review carries it. The revision and the read set are
// deliberately absent: both are derived from the invocation's own prompt when
// the reply is emitted.
type Review struct {
	// Summary is the report's summary, which findings.Validate requires.
	Summary string `json:"summary"`
	// Findings is what the derived review reports, empty for a clean one. A
	// finding here is checked by the product like any other: one whose
	// location or citations reach past the derived read set is refused by the
	// binding, which is the product behaving as specified rather than this
	// package failing.
	Findings []findings.Finding `json:"findings,omitempty"`
}

// report derives the findings.Report this review answers with from the
// prompt: the demanded revision between the two revision landmarks, and the
// read set declared as exactly the listed touched paths.
func (r Review) report(prompt string) (findings.Report, error) {
	_, tail, ok := strings.Cut(prompt, reviewRevisionPrefix)
	if !ok {
		return findings.Report{}, fmt.Errorf("deriving a review: the prompt carries no %q, so there is no demanded revision to answer with", reviewRevisionPrefix)
	}
	revision, _, ok := strings.Cut(tail, reviewRevisionSuffix)
	if !ok {
		return findings.Report{}, fmt.Errorf("deriving a review: the prompt names a revision without %q after it", reviewRevisionSuffix)
	}
	_, tail, ok = strings.Cut(prompt, reviewTouchedHeading)
	if !ok {
		return findings.Report{}, fmt.Errorf("deriving a review: the prompt carries no %q, so there is no read set to declare", reviewTouchedHeading)
	}
	var read []string
	for line := range strings.Lines(tail) {
		path, listed := strings.CutPrefix(line, "  - ")
		if !listed {
			if len(read) > 0 {
				break
			}
			continue
		}
		read = append(read, strings.TrimRight(path, "\n"))
	}
	if len(read) == 0 {
		return findings.Report{}, fmt.Errorf("deriving a review: the prompt lists no paths under %q", reviewTouchedHeading)
	}
	return findings.Report{
		Summary:  r.Summary,
		Revision: revision,
		Read:     findings.Paths(read),
		Findings: r.Findings,
	}, nil
}

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
	// Review, when the Envelope is nil, has the stand-in derive a review
	// report from the invocation's own prompt and answer with that inside a
	// well-formed envelope. Reviewed builds a reply carrying one, and its doc
	// comment says why the derivation cannot happen at scripting time.
	Review *Review `json:"review,omitempty"`
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
	// IgnoresTerminate has the stand-in ignore SIGTERM for the whole
	// invocation, as an agent that does not stop on a polite signal does, so
	// only the forceful signal ends it before its Hold is over. The
	// disposition is set before the call is recorded, so a test that has seen
	// the call in Agent.Calls knows a SIGTERM sent from then on is ignored.
	// It is honoured by the one-shot invocation modes, Claude Code's and
	// Codex exec's, and nowhere else.
	IgnoresTerminate bool `json:"ignores_terminate,omitempty"`
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

// WithIgnoredTerminate returns the reply with SIGTERM ignored for the whole
// invocation, on the terms IgnoresTerminate states.
func (r Reply) WithIgnoredTerminate() Reply { r = r.own(); r.IgnoresTerminate = true; return r }

// own returns the reply with an envelope and review of its own, so a reply
// derived from another can be adjusted through either pointer without
// reaching the one it came from. Every other field is already a value the
// copy owns.
func (r Reply) own() Reply {
	if r.Envelope != nil {
		envelope := *r.Envelope
		r.Envelope = &envelope
	}
	if r.Review != nil {
		review := *r.Review
		r.Review = &review
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
// to a ShapeText invocation, and under ShapeReport and ShapeReview it is also
// how output that arrived intact and is not a report is scripted.
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
	// Result is the agent's final text, which for a ShapeReport or a
	// ShapeReview invocation is what internal/findings is given.
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

// session is the session identifier the envelope reports to c: the one it
// states, else the one c resumed, else a fresh one derived from c's arrival
// order, on Envelope.Session's terms.
func (e Envelope) session(c Call) string {
	switch {
	case e.Session != nil:
		return *e.Session
	case c.Session() != "":
		return c.Session()
	default:
		return fmt.Sprintf("standin-session-%d", c.Seq)
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
		"session_id": e.session(c),
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
