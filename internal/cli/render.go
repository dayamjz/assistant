package cli

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// readOut writes an answer for a person.
//
// It is a second rendering of the same value the structured mode writes, not a
// second source of it: every branch here reads fields off a shape
// internal/machine owns, so the two surfaces cannot report different things.
// Which renderer answers which shape is the renderings table, and that table
// is also what says which renderers speak through internal/machine's phrase
// table, so the two cannot drift apart.
//
// There is no colour and no cursor movement. PRD section 9 asks the terminal
// interface to use only the terminal's own palette so it follows the user's
// theme; the safest reading of that for a command that prints and exits is to
// emit no styling at all.
//
// An answer of a type no row claims is refused rather than printed: the
// refusal is a noReading, which writes nothing here and which render turns
// into a failure on standard error, so an answer type added without a
// renderer fails loudly instead of handing a person a Go struct.
func readOut(w io.Writer, answer any) error {
	if answer == nil {
		return nil
	}
	of := reflect.TypeOf(answer)
	for _, r := range renderings {
		if r.of == of {
			if r.voice == VoiceEvidence {
				writePointed(w, r, answer)
				return nil
			}
			r.render(w, answer)
			return nil
		}
	}
	return noReading{of: of}
}

// noReading is the refusal readOut returns for an answer no row claims. What
// it says is internal/machine's row for that refusal; the type it names is
// for a test or a log, and is not in what a person reads.
type noReading struct{ of reflect.Type }

// Error is the refusal as a person reads it.
func (noReading) Error() string { return machine.PhraseNoReading.String() }

// Rendering is one human renderer of this surface: what it is called and
// which rule P13 holds its lines to.
type Rendering struct {
	// Name is the renderer's name, as internal/journey's coverage note and
	// this package's tests refer to it.
	Name string
	// Voice is the rule its lines answer to.
	Voice Voice
}

// Voice is the rule P13 holds one renderer's lines to. It is one of three,
// and the renderings table gives every row exactly one.
type Voice int

const (
	// VoiceUnsettled is a renderer that still prints this build's own words
	// because the wording it would be held to is not settled; the renderings
	// table says why beside each such row. It is the zero value, so a row
	// that states no voice claims nothing.
	VoiceUnsettled Voice = iota
	// VoicePhrased is a renderer every line of which is a row of
	// internal/machine's phrase table, which P13 holds to the derived
	// forbidden set, or one beside text that is the person's own or an
	// agent's, escaped.
	VoicePhrased
	// VoiceEvidence is a renderer that writes one of the four detailed
	// evidence documents P13 and PRD section 9 name - the detail view, the
	// usage view, the log view, and the relay of a finding that needs a
	// decision - which P13 lets keep exact identifiers and stage names. Its
	// row carries the phrase-table row that points a person at it, and writeEvidence,
	// which the run renderer writes the first and last through, and
	// writePointed, which readOut writes the usage view and the log view
	// through, write that row first,
	// because the message that points at an evidence document stays
	// phrased.
	VoiceEvidence
)

// String is the voice's name, for a test's message.
func (v Voice) String() string {
	switch v {
	case VoicePhrased:
		return "phrased"
	case VoiceEvidence:
		return "evidence"
	default:
		return "unsettled"
	}
}

// Renderings is every human renderer this surface has, in the order the
// table lists them and the fallback last, so a reader can hold a claim about
// which are phrased to an enumeration rather than to a list someone kept. The
// fallback is the refusal readOut returns for an answer no row claims, which
// writes nothing on standard output and is phrased on standard error. The
// result is a copy.
func Renderings() []Rendering {
	out := make([]Rendering, 0, len(renderings)+1)
	for _, r := range renderings {
		out = append(out, Rendering{Name: r.name, Voice: r.voice})
	}
	return append(out, Rendering{Name: fallback.name, Voice: fallback.voice})
}

// rendering is one row of the table readOut dispatches through: the
// renderer, its voice, the answer type it renders, which is nil for a
// renderer reached only from another renderer, and for an evidence document
// the row that points a person at it.
type rendering struct {
	name   string
	voice  Voice
	of     reflect.Type
	render func(io.Writer, any)
	points machine.Phrase
}

// writeEvidence writes an evidence document behind the phrase that points at
// it, and nothing at all when the document is empty, so a pointer never
// stands over nothing. It is how the run renderer reaches each evidence row;
// the renderings table says what that does and does not guarantee.
func writeEvidence(w io.Writer, row rendering, value any) {
	var body strings.Builder
	row.render(&body, value)
	if body.Len() == 0 {
		return
	}
	writeln(w)
	writeln(w, row.points)
	_, _ = io.WriteString(w, body.String())
}

// writePointed is writeEvidence for an evidence document that is a whole
// answer rather than part of one, which readOut dispatches here by the row's
// voice: the pointer, then the document, with nothing before them. Because
// the dispatch reads the voice, an evidence row that names an answer type is
// written behind its pointer by readOut itself, and no renderer has to
// remember to.
func writePointed(w io.Writer, row rendering, value any) {
	var body strings.Builder
	row.render(&body, value)
	if body.Len() == 0 {
		return
	}
	writeln(w, row.points)
	_, _ = io.WriteString(w, body.String())
}

// renderings is the one table of human renderers. readOut dispatches by the
// answer's type through it, and Renderings reads it out, so a renderer added
// here is enumerated and one added anywhere else is not reached.
//
// Every row but four is phrased: what it prints is a phrase from
// internal/machine's table, or one beside a word that is the person's own -
// a branch name, a project, a reference a change carried, the repository's
// default branch - or an agent's, escaped. The stop guard's answer is phrased
// on different terms: its renderer prints nothing, because the host reads the
// message on standard error and hostStop writes it there, and that message is
// the turn row followed by the pull guard's row, both from the table.
//
// The installer's answer is phrased with two lines beside the row that are
// machine text rather than this build's words: the host's settings file and
// the configuration line, printed exactly because a person copies them into
// the host, and for a conflict the command line the file already holds, as
// it holds it. readOutHostInstall prints each on a line of its own after a
// label, so the row is never mixed with them.
//
// The four that are not phrased are named, so that the claim about the
// rest is bounded by an enumeration. They are evidence documents, which P13
// names as the exception to its rule and P13 and PRD section 9 name as four
// documents:
//
//   - details is the detail view's evidence for a run: where its execution
//     stands, the reason a bound parked it, the keys its configuration
//     resolution dropped, a line per stage by name with its timing and fix
//     note, and the proposals raised about it. Section 9 says the detail view
//     names the nine stages. It does not list the rejections or the
//     proposals; they are records of the run rather than lines about the
//     person's request, and this table classes them as the detail view's
//     evidence, a reading of this build's rather than one the PRD states.
//   - findings relays the findings holding a decision in full, with their
//     exact names, locations and text, which section 9's relay-verbatim row
//     requires and says is an evidence document in P13's sense.
//   - usage is the usage view, runs --usage's answer: what the agent calls
//     in a window cost, grouped by agent, model and purpose and by run and
//     stage, under those exact names.
//   - log is the log view, runs RUN --log=STAGE's answer: the end of one
//     stage's log, with each agent call's account and reply as the log
//     holds them. It is an answer of its own and not part of the detail
//     view, so it is the fourth evidence document P13's list names.
//
// The first two name no answer type, so readOut never dispatches to either;
// the run renderer reaches each through writeEvidence, behind the phrased
// row its table entry carries, so what points a person at the evidence is
// held to P13 even though the evidence is not. That is a convention inside
// this package rather than a mechanism: readOutDetails and readOutFindings
// are callable from anywhere here, and the evidence test reads the run
// renderer, so a new caller that skipped writeEvidence would print no
// pointer and nothing would fail until a test rendered it. The usage and
// log rows name their answer types, and readOut writes any evidence row it
// dispatches to through writePointed, so on that path the pointer is the
// dispatch's rather than a caller's to remember; readOutUsage and
// readOutStageLog are still callable directly, on the same terms as the
// other two. What a run says about a request it
// did not apply - starting inputs that went to no run, or an answer the
// service closed the decision ahead of - is a status line rather than
// evidence, so readOutRun prints it as a phrased row and not in details.
//
// A plan is phrased: the row for what custody found or did, then for a plan
// not yet carried out a row per step, each from internal/machine's table.
// The commits, references and the run a plan names are the structured
// rendering's, which the rows that need one point at.
var renderings = []rendering{
	{name: "status", voice: VoicePhrased, of: typeOf[machine.Status](), render: func(w io.Writer, v any) { readOutStatus(w, v.(machine.Status)) }},
	{name: "run", voice: VoicePhrased, of: typeOf[machine.Run](), render: func(w io.Writer, v any) { readOutRun(w, v.(machine.Run)) }},
	{name: "runs", voice: VoicePhrased, of: typeOf[machine.Runs](), render: func(w io.Writer, v any) { readOutRuns(w, v.(machine.Runs)) }},
	{name: "tasks", voice: VoicePhrased, of: typeOf[machine.Tasks](), render: func(w io.Writer, v any) { readOutTasks(w, v.(machine.Tasks)) }},
	{name: "task", voice: VoicePhrased, of: typeOf[machine.Task](), render: func(w io.Writer, v any) { readOutTask(w, v.(machine.Task)) }},
	{name: "service", voice: VoicePhrased, of: typeOf[machine.Service](), render: func(w io.Writer, v any) { readOutService(w, v.(machine.Service)) }},
	{name: "event", voice: VoicePhrased, of: typeOf[ipc.Event](), render: func(w io.Writer, v any) { writeln(w, eventLine(v.(ipc.Event))) }},
	{name: "host stop", voice: VoicePhrased, of: typeOf[machine.HostStop](), render: func(w io.Writer, v any) { readOutHostStop(w, v.(machine.HostStop)) }},
	{name: "host install", voice: VoicePhrased, of: typeOf[machine.HostInstall](), render: func(w io.Writer, v any) { readOutHostInstall(w, v.(machine.HostInstall)) }},
	{name: "decision", voice: VoicePhrased, render: func(w io.Writer, v any) { readOutDecision(w, v.(machine.Decision)) }},
	detailsRendering,
	findingsRendering,
	{name: "doctor", voice: VoicePhrased, of: typeOf[machine.Doctor](), render: func(w io.Writer, v any) { readOutDoctor(w, v.(machine.Doctor)) }},
	{name: "lifecycle", voice: VoicePhrased, of: typeOf[machine.Lifecycle](), render: func(w io.Writer, v any) { readOutLifecycle(w, v.(machine.Lifecycle)) }},
	{name: "managed removal", voice: VoicePhrased, of: typeOf[machine.ManagedRemoval](), render: func(w io.Writer, _ any) { writeln(w, machine.PhraseManagedRemoved) }},
	{name: "init", voice: VoicePhrased, of: typeOf[machine.Init](), render: func(w io.Writer, v any) { readOutInit(w, v.(machine.Init)) }},
	{name: "plan", voice: VoicePhrased, of: typeOf[machine.Plan](), render: func(w io.Writer, v any) { readOutPlan(w, v.(machine.Plan)) }},
	{name: "eject", voice: VoicePhrased, of: typeOf[machine.Eject](), render: func(w io.Writer, v any) { writeln(w, machine.EjectPhrase(v.(machine.Eject))) }},
	{name: "health", voice: VoicePhrased, of: typeOf[machine.Health](), render: func(w io.Writer, v any) { writeln(w, machine.HealthPhrase(v.(machine.Health))) }},
	{name: "admission", voice: VoicePhrased, of: typeOf[machine.Admission](), render: func(w io.Writer, v any) { readOutAdmission(w, v.(machine.Admission)) }},
	{name: "notification", voice: VoicePhrased, of: typeOf[machine.Notification](), render: func(w io.Writer, v any) { readOutNotification(w, v.(machine.Notification)) }},
	{name: "task answer", voice: VoicePhrased, of: typeOf[machine.TaskAnswered](), render: func(w io.Writer, v any) {
		a := v.(machine.TaskAnswered)
		writeln(w, machine.TaskAnsweredPhrase(a))
		// The note is the person's own words beside the closed Phrase
		// table: HoldPhrase and TaskAnsweredPhrase stay fixed rows, and
		// the note is written after them when the hold carries one.
		if note, ok := a.Hold.ResolutionNote.Get(); ok && strings.TrimSpace(note) != "" {
			writeln(w, "What was decided: "+printable(note))
		}
	}},
	{name: "catch up", voice: VoicePhrased, of: typeOf[machine.CatchUp](), render: func(w io.Writer, v any) { readOutCatchUp(w, v.(machine.CatchUp)) }},
	{name: "marked seen", voice: VoicePhrased, of: typeOf[machine.Marked](), render: func(w io.Writer, v any) {
		writeln(w, machine.MarkedPhrase(v.(machine.Marked)))
	}},
	{name: "task created", voice: VoicePhrased, of: typeOf[machine.TaskCreated](), render: func(w io.Writer, v any) {
		writeln(w, machine.TaskCreatedPhrase(v.(machine.TaskCreated)))
	}},
	{name: "task message", voice: VoicePhrased, of: typeOf[machine.TaskMessaged](), render: func(w io.Writer, v any) {
		writeln(w, machine.MessagePhrase(v.(machine.TaskMessaged).Message))
	}},
	{name: "task stop", voice: VoicePhrased, of: typeOf[machine.TaskStopped](), render: func(w io.Writer, v any) {
		writeln(w, machine.StopPhrase(v.(machine.TaskStopped)))
	}},
	{name: "task peek", voice: VoicePhrased, of: typeOf[machine.TaskPeek](), render: func(w io.Writer, v any) {
		writeln(w, machine.PeekPhrase(v.(machine.TaskPeek)))
	}},
	{name: "task relaunch", voice: VoicePhrased, of: typeOf[machine.TaskRelaunched](), render: func(w io.Writer, v any) {
		writeln(w, machine.TaskRelaunchedPhrase(v.(machine.TaskRelaunched)))
	}},
	{name: "task relay", voice: VoicePhrased, of: typeOf[machine.TaskRelayed](), render: func(w io.Writer, v any) {
		writeln(w, machine.TaskRelayedPhrase(v.(machine.TaskRelayed)))
	}},
	{name: "task promotion", voice: VoicePhrased, of: typeOf[machine.TaskPromoted](), render: func(w io.Writer, v any) {
		writeln(w, machine.TaskPromotedPhrase(v.(machine.TaskPromoted)))
	}},
	{name: "wakes", voice: VoicePhrased, of: typeOf[machine.Wakes](), render: func(w io.Writer, v any) {
		writeln(w, machine.WakesPhrase(v.(machine.Wakes)))
	}},
	{name: "queued wakes", voice: VoicePhrased, of: typeOf[machine.QueuedWakes](), render: func(w io.Writer, v any) { readOutQueuedWakes(w, v.(machine.QueuedWakes)) }},
	{name: "wakes acknowledged", voice: VoicePhrased, of: typeOf[machine.Acknowledged](), render: func(w io.Writer, v any) {
		writeln(w, machine.AcknowledgedPhrase(v.(machine.Acknowledged)))
	}},
	{name: "supervision", voice: VoicePhrased, of: typeOf[supervisor.Arm](), render: func(w io.Writer, v any) {
		writeln(w, machine.ArmPhrase(v.(supervisor.Arm)))
	}},
	{name: "worker event", voice: VoicePhrased, of: typeOf[machine.Appended](), render: func(w io.Writer, v any) {
		writeln(w, machine.AppendedPhrase(v.(machine.Appended)))
	}},
	usageRendering,
	logRendering,
}

// fallback is the row for an answer no row claims. It is not in the table,
// because it has no type to dispatch on, and it renders nothing: readOut
// returns noReading in its place, whose text is internal/machine's row for
// the refusal and which render writes on standard error.
var fallback = rendering{name: "fallback", voice: VoicePhrased, render: func(io.Writer, any) {}}

// detailsRendering, findingsRendering and usageRendering are the evidence
// rows. The first two are named so that readOutRun reaches each through
// writeEvidence by its row, which carries the phrase that points at it,
// rather than by its function.
var (
	detailsRendering = rendering{name: "details", voice: VoiceEvidence,
		render: func(w io.Writer, v any) { readOutDetails(w, v.(machine.Run)) },
		points: machine.PhraseRunRecordFollows}
	findingsRendering = rendering{name: "findings", voice: VoiceEvidence,
		render: func(w io.Writer, v any) { readOutFindings(w, v.(machine.Decision)) },
		points: machine.PhraseFindingsFollow}
	// usageRendering is the third evidence row, and the one that is an answer
	// of its own: runs --usage answers with a machine.Usage, so the row names
	// that type and readOut writes it through writePointed.
	usageRendering = rendering{name: "usage", voice: VoiceEvidence, of: typeOf[machine.Usage](),
		render: func(w io.Writer, v any) { readOutUsage(w, v.(machine.Usage)) },
		points: machine.PhraseUsageFollows}
	// logRendering is the log view, runs RUN --log=STAGE's answer, the
	// fourth evidence document P13 names: it is an answer of its own, so it
	// names its type and
	// readOut writes it through writePointed, on the usage row's terms.
	logRendering = rendering{name: "log", voice: VoiceEvidence, of: typeOf[machine.StageLog](),
		render: func(w io.Writer, v any) { readOutStageLog(w, v.(machine.StageLog)) },
		points: machine.PhraseStageLogFollows}
)

func typeOf[T any]() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }

// readOutStatus prints PRD section 9's status - service, gate, branch, the
// branch's custody classification and active run, or the branch's latest run
// when a bound parked it and none is active - each through the phrase
// table. What it does not print is in
// the structured rendering: the home root, the socket, the repository's
// identifier and upstream, the gate's identifier and the head commit are
// mechanics, and a person who needs one to act reads --json. A working copy
// with no repository record and one bound to no gate read alike, because
// the next decision is the same for both.
func readOutStatus(w io.Writer, s machine.Status) {
	readOutService(w, s.Service)
	switch {
	case s.Repository == nil, s.Gate == nil || !s.Gate.Present:
		writeln(w, machine.PhraseUngated)
	default:
		writeln(w, machine.PhraseGated)
	}
	if s.Branch != nil && s.Branch.Name != "" {
		writef(w, "Branch     %s\n", printable(s.Branch.Name))
	}
	if s.Custody != nil {
		if phrase, ok := machine.CustodyPhrase(*s.Custody); ok {
			writeln(w, phrase)
		}
	}
	readOutSupervision(w, s.Supervision)
	if s.Repository == nil {
		return
	}
	if s.ActiveRun != nil {
		writeln(w)
		readOutRun(w, *s.ActiveRun)
		return
	}
	writeln(w, machine.PhraseNoRun)
	// The branch's latest run, when a bound parked it, is read out after
	// that row so the findings it stopped on stay in view once it has ended;
	// its own phrase says which bound.
	if s.ParkedRun != nil {
		writeln(w)
		readOutRun(w, *s.ParkedRun)
	}
}

// readOutSupervision prints the pull guard's warning when there is one. The
// phrase is internal/machine's and is printed as it stands, so what a person
// reads here is what the vocabulary test there held to P13.
func readOutSupervision(w io.Writer, s *machine.Supervision) {
	if s == nil || s.Warning == "" {
		return
	}
	writeln(w, s.Warning)
}

// readOutService prints whether a service answered, through the phrase
// table, and the row for a home whose service is handed to the operating
// system's service manager, whose definition's path is the structured
// rendering's. The detail of why one did not is a dial error naming the socket,
// which is the structured rendering's; assistant doctor is the verb that
// diagnoses it for a person.
func readOutService(w io.Writer, s machine.Service) {
	if !s.Running {
		writeln(w, machine.PhraseServiceAbsent)
	} else {
		writeln(w, machine.PhraseServiceUp)
	}
	if s.Managed != nil {
		writeln(w, machine.PhraseServiceManaged)
	}
}

// readOutRun prints a run: its phrase, the branch, the agent it resolved,
// whether it runs under standing consent, what became of its local copy and whether something it started may still
// be at work there, what of the request was not applied, and
// then its details, its decision and the findings holding the decision. The
// details and the findings are the two evidence documents, each written
// through writeEvidence behind the row that points at it; the decision
// renderer is phrased. The phrase carries the outcome, the consequence and
// the next decision, so no outcome word, status or next action is printed
// beside it; the record's identifier is the structured rendering's.
func readOutRun(w io.Writer, r machine.Run) {
	writeln(w, r.Phrase())
	writef(w, "Branch     %s\n", printable(r.Record.Branch))
	// The agent the run resolved, straight off the record. It is absent for a
	// run recorded before one was resolved, and rendering nothing then is the
	// record's own honesty carried through: an unknown is not a name.
	if agent, ok := r.Record.ResolvedAgent.Get(); ok {
		writef(w, "Agent      %s\n", printable(agent))
	}
	// Standing consent, straight off the record: the program answered some
	// of this run's decisions, and a person reading the run is told so
	// before anything else about it. When it was given is the structured
	// rendering's.
	if r.Record.StandingConsent.IsKnown() {
		writeln(w, machine.PhraseStandingConsent)
	}
	if r.Cleanup != nil {
		if phrase, ok := machine.CleanupPhrase(*r.Cleanup); ok {
			writeln(w, phrase)
		}
	}
	if r.CopySweep != nil {
		if phrase, ok := machine.CopySweepPhrase(*r.CopySweep); ok {
			writeln(w, phrase)
		}
	}
	// What a request carried and the run did not take is said about the
	// request, as a status line, so it is a row here and not evidence: which
	// inputs they were, and the service's sentence for why an answer was not
	// taken, are the structured rendering's.
	if len(r.NotApplied) > 0 {
		writeln(w, machine.PhraseInputsNotApplied)
	}
	if r.AnswerNotApplied != "" {
		writeln(w, machine.PhraseAnswerNotApplied)
	}
	writeEvidence(w, detailsRendering, r)
	if r.Decision != nil {
		readOutDecision(w, *r.Decision)
		writeEvidence(w, findingsRendering, *r.Decision)
	}
}

// readOutDetails prints the run's details: where its execution stands, the
// reason a bound parked it, which bound its record names and the findings the
// stage that park names last reported, the keys its configuration resolution dropped,
// what became of each stage, and the proposals raised about it. These are
// this build's own words - node positions, stage names, configuration keys -
// because this is the detail view's evidence, which P13 lets keep them; it
// is reached through writeEvidence, behind the phrased row that points at it,
// and prints nothing for a run with none of them.
func readOutDetails(w io.Writer, r machine.Run) {
	if r.Progress != nil {
		writef(w, "Position   %s after %d of %d steps\n", positionOf(r), r.Steps, r.Budget)
	}
	if a := r.Activity; a != nil {
		writef(w, "Active     %s\n", activityLine(*a, r.Position))
	}
	if r.Reason != "" {
		writef(w, "Reason     %s\n", printable(r.Reason))
	}
	// Which bound parked the run, off its record, and the findings the stage
	// the park names last reported, off that stage's report on this answer.
	if park, ok := r.Record.Park.Get(); ok {
		writef(w, "Parked     %s\n", parkLine(park))
		if parked := parkedFindings(r, park); len(parked) > 0 {
			writef(w, "\nFindings the %s stage parked on\n", printable(park.Stage))
			writeFindings(w, parked)
		}
	}
	// The keys the run's configuration resolution dropped, one line each as
	// the record carries them, so an author whose setting had no effect learns
	// that here rather than from the service log. A resolution that dropped
	// nothing, and a run with no recorded resolution, both print nothing: the
	// lines exist to be acted on, not to say none were needed.
	if rejected, ok := r.Record.ConfigRejections.Get(); ok && len(rejected) > 0 {
		writeln(w, "Rejected from this run's configuration:")
		for _, line := range rejected {
			writef(w, "  %s\n", printable(line))
		}
	}
	if len(r.Stages) > 0 {
		writeln(w, "\nStages")
		for _, stage := range r.Stages {
			writef(w, "  %-9s %s%s%s\n", stage.Stage, outcomeWord(stage), timing(stage), fixNote(stage))
		}
	}
	if len(r.Proposals) > 0 {
		writeln(w, "\nProposals")
		for _, p := range r.Proposals {
			writef(w, "  %s\n", printable(p.Description))
		}
	}
}

// readOutDecision prints the decision a run is waiting on, through the
// phrase table: that it waits on the person, and the answers it takes, with
// the fix round's offer when the hold takes a request for one. The halt
// point's question names the stage holding, so it is the structured
// rendering's; the answers are printed because they are what a person types.
// They come off the run's checkpoint, so the line is escaped as a whole.
func readOutDecision(w io.Writer, d machine.Decision) {
	writeln(w)
	writeln(w, printable(machine.DecisionPhrase(d).String()))
	for _, option := range d.Detailed {
		if option == string(pipeline.OutcomeFixRequested) {
			writeln(w, printable(machine.FixRoundPhrase(option).String()))
		}
	}
}

// readOutFindings prints the findings holding a decision in full.
//
// PRD section 9 requires a finding that needs a decision to be relayed with
// its full text, unsummarized and unjudged, and calls that relay a detailed
// evidence document in P13's sense, so each keeps its exact identifier and
// location. Nothing here truncates a description, drops a finding, or
// reorders them. It is reached through writeEvidence, behind the phrased row
// that points at it; the message asking for the decision is the decision
// renderer's, which is phrased.
func readOutFindings(w io.Writer, d machine.Decision) {
	writeFindings(w, d.Findings)
}

// parkLine is the details line for a park: the bound in the record's own
// word, the stage it names, and its limit where it has one.
func parkLine(p store.RunPark) string {
	line := string(p.Bound)
	if p.Stage != "" {
		line += " at " + p.Stage
	}
	if p.Limit > 0 {
		line += fmt.Sprintf(" (limit %d)", p.Limit)
	}
	return printable(line)
}

// parkedFindings is what the stage a park names last reported, read off that
// stage's report on the run's answer, and nil when it has none.
func parkedFindings(r machine.Run, p store.RunPark) []findings.Finding {
	for _, stage := range r.Stages {
		if stage.Stage == p.Stage && stage.Report != nil {
			return stage.Report.Findings
		}
	}
	return nil
}

// writeFindings prints findings in full, on readOutFindings' terms: nothing
// truncated, dropped or reordered, and every agent-written line escaped.
func writeFindings(w io.Writer, list []findings.Finding) {
	for _, finding := range list {
		writef(w, "\n  [%s] %s %s\n", printable(string(finding.Action)), printable(string(finding.Severity)), printable(finding.ID))
		if where := locationOf(finding); where != "" {
			writef(w, "  %s\n", printable(where))
		}
		for _, line := range strings.Split(finding.Description, "\n") {
			writef(w, "  %s\n", printable(line))
		}
	}
}

// printable renders text a stage's agent wrote so that a terminal displays it
// rather than acts on it.
//
// A finding's text is whatever an agent put there, and a terminal reads an
// escape sequence in it as an instruction: clearing the screen, rewriting the
// lines above, or setting the clipboard. machine.Encoder makes the same pass
// over the structured rendering on the same predicate, unicode.IsControl, so
// the two surfaces are safe by one rule; what differs is the form each writes,
// a readable escape here and a \uXXXX escape there.
//
// A line break is a control character like any other, so a caller that wants
// the text's own line breaks to survive splits on them first and passes each
// line: what reaches this is then one line, and every control character in it
// is one that does not belong.
func printable(text string) string {
	if !strings.ContainsFunc(text, unicode.IsControl) {
		return text
	}
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if unicode.IsControl(r) {
			fmt.Fprintf(&b, "\\x%02x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// readOutRuns prints the run listing: when each run was recorded, its branch,
// and its phrase from the record alone, on machine.RecordPhrase's terms, with
// the standing consent row beneath a run whose record carries it. The
// record's identifier is the structured rendering's, which is where a verb
// that takes one is driven from.
func readOutRuns(w io.Writer, runs machine.Runs) {
	if len(runs.Runs) == 0 {
		writeln(w, machine.PhraseNoRuns)
		return
	}
	for _, record := range runs.Runs {
		writef(w, "%s  %s: %s\n", record.CreatedAt.Format("2006-01-02 15:04:05"),
			printable(record.Branch), machine.RecordPhrase(record))
		// A run under standing consent says so on its own line beneath,
		// off the same record, as status says it.
		if record.StandingConsent.IsKnown() {
			writef(w, "  %s\n", machine.PhraseStandingConsent)
		}
	}
}

// readOutTasks prints the pull guard's warning and then one line per task.
func readOutTasks(w io.Writer, tasks machine.Tasks) {
	readOutSupervision(w, tasks.Supervision)
	if len(tasks.Tasks) == 0 {
		writeln(w, machine.PhraseNoFleetWork)
		return
	}
	for _, task := range tasks.Tasks {
		readOutTask(w, task)
	}
}

// readOutTask prints one task: what it is for, which is the person's own
// word, and its phrase. The identifier, the shape, the state label and its
// source are the structured rendering's: P13 keeps a task identifier and a
// state label off this line, and a task with no run shows its own state and
// no outcome word, which the phrase table holds to. An investigation's
// stored report is never printed here: TaskPhrase points at assistant --json
// tasks for it, and the structured answer carries Report.
func readOutTask(w io.Writer, task machine.Task) {
	writef(w, "%s: %s\n", printable(task.Record.Project), printable(machine.TaskPhrase(task).String()))
}

// readOutCatchUp prints PRD section 9's catching up: the pull guard's
// warning when there is one, and then the four parts in the read's order,
// each under its row from the phrase table, or that part's empty row. A
// task's line is the fleet listing's, the person's project and the task's
// phrase, so the two cannot read differently; a run's is its branch and its
// phrase, the decision's row for a run held at one; a hold's is its row,
// beside the project of the task it names. Landed records past the read's
// bound are counted on a row of their own. Identifiers, the answer's
// command line and the records themselves are the structured rendering's,
// which the rows that need one point at.
func readOutCatchUp(w io.Writer, c machine.CatchUp) {
	readOutSupervision(w, c.Supervision)
	waiting, landed, underWay, stopped := machine.CatchUpPhrases(c)
	writeln(w, waiting)
	for _, item := range c.Waiting {
		writeln(w, waitingLine(item))
	}
	writeln(w)
	writeln(w, landed)
	for _, l := range c.Landed {
		writef(w, "%s: %s\n", printable(l.Task.Record.Project), machine.LandedPhrase(l))
	}
	if c.MoreLanded > 0 {
		writeln(w, machine.LandedMorePhrase(c.MoreLanded))
	}
	writeln(w)
	writeln(w, underWay)
	for _, task := range c.UnderWay.Tasks {
		readOutTask(w, task)
	}
	for _, run := range c.UnderWay.Runs {
		writef(w, "%s: %s\n", printable(run.Record.Branch), run.Phrase())
	}
	writeln(w)
	writeln(w, stopped)
	for _, task := range c.Stopped {
		readOutTask(w, task)
	}
}

// readOutQueuedWakes is a person's listing of the queue: the count row, then
// one line per row, the project of the task it is about, which is the
// person's own text and escaped, beside the phrase-table rows for what the
// notice says and whether the coordinator has been handed it. Neither a
// task's identifier nor a notice's kind is printed; the --json form carries
// both.
func readOutQueuedWakes(w io.Writer, q machine.QueuedWakes) {
	writeln(w, machine.QueuedWakesPhrase(q))
	for _, row := range q.Wakes {
		writef(w, "%s: %s %s\n", printable(row.Project), machine.QueuedWakePhrase(row), machine.QueuedWakeStanding(row))
	}
}

// waitingLine is one decision waiting on the person: a run's branch and the
// decision's row with the answers it takes, or a hold's row beside the
// project of the task it names. The hold's subject is the program's own
// sentence and the options come off a checkpoint, so the line is escaped.
func waitingLine(item machine.Waiting) string {
	switch {
	case item.Run != nil:
		phrase := item.Run.Phrase()
		if item.Run.Decision != nil {
			phrase = machine.DecisionPhrase(*item.Run.Decision)
		}
		return printable(item.Run.Record.Branch) + ": " + printable(phrase.String())
	case item.Hold != nil && item.Task != nil:
		return printable(item.Task.Record.Project) + ": " + printable(machine.HoldPhrase(*item.Hold).String())
	case item.Hold != nil:
		return printable(machine.HoldPhrase(*item.Hold).String())
	default:
		return machine.PhraseUnfamiliarUpdate.String()
	}
}

// readOutDoctor prints a row per check and then the verdict, all through
// the phrase table. What each check found - a path, a socket, an error, an
// agent's name - is the structured rendering's, and a row that needs it to be
// acted on points there.
func readOutDoctor(w io.Writer, report machine.Doctor) {
	for _, check := range report.Checks {
		writeln(w, machine.CheckPhrase(check))
	}
	writeln(w)
	writeln(w, machine.DoctorPhrase(report))
}

// readOutLifecycle prints what became of a request to stop or restart the
// service, and for a refusal each run that caused it, by its branch and its
// phrase from the record, as the run listing prints one. A restart refused on
// the home's configuration file is followed by that file's refusal, which
// names the key and the value.
func readOutLifecycle(w io.Writer, answer machine.Lifecycle) {
	writeln(w, machine.LifecyclePhrase(answer))
	if answer.ConfigurationRefused {
		writeln(w, "  "+printable(answer.Detail))
	}
	for _, record := range answer.Active {
		writef(w, "  %s: %s\n", printable(record.Branch), machine.RecordPhrase(record))
	}
}

// readOutInit prints what became of the checkout's gate, the row for a fork
// when the record names one, whose URL is the structured rendering's, and the branch
// trusted configuration is read from, which is the person's own branch name
// and the one fact here they may want to correct with --default-branch, after
// internal/machine's row for where that answer came from, so a wrong one is
// visible when it is written. The gate's identifier and where it lives are
// the structured rendering's. Then the command offer, through
// readOutCommands, whose file name and commands are printed as machine text
// beside its rows.
func readOutInit(w io.Writer, answer machine.Init) {
	writeln(w, machine.InitPhrase(answer))
	if answer.Repository.ForkURL.IsKnown() {
		writeln(w, machine.PhraseInitFork)
	}
	if branch := answer.Repository.DefaultBranch; branch != "" {
		writeln(w, machine.DefaultBranchPhrase(answer.DefaultBranchSource))
		writef(w, "Default branch %s\n", printable(branch))
	}
	readOutCommands(w, answer.Commands)
}

// readOutPlan prints what custody found or did for the branch, beside the
// branch's name, which is the person's own, and for a plan not yet carried
// out each step it would take, all through the phrase table. The commits and
// references the steps name are the structured rendering's.
func readOutPlan(w io.Writer, plan machine.Plan) {
	writeln(w, machine.PlanPhrase(plan))
	if plan.Classification != nil && plan.Classification.Branch != "" {
		writef(w, "Branch     %s\n", printable(plan.Classification.Branch))
	}
	if plan.Applied || plan.Refusal != "" {
		return
	}
	for _, step := range plan.Steps {
		writef(w, "  %s\n", machine.StepPhrase(step))
	}
}

// positionOf is where a run stands: the node it has not run, or the fact that
// it has finished, which is what an empty position means.
func positionOf(r machine.Run) string {
	if r.Position == "" {
		return "completed"
	}
	return r.Position
}

// outcomeWord is what became of one stage, said in a way that distinguishes a
// stage that ran from one that was passed over, which the outcome alone does
// not.
func outcomeWord(stage machine.Stage) string {
	if !stage.Ran && stage.Outcome != "" {
		return string(stage.Outcome) + " (did not run)"
	}
	if stage.Outcome == "" {
		return "pending"
	}
	return string(stage.Outcome)
}

// timing says how long a stage's executions took, from the run's record of
// them, and nothing for a stage that executed no body. A stage that executed
// more than once, as a stage taking fix rounds does, says how many times and
// the time across all of them.
func timing(stage machine.Stage) string {
	if len(stage.Executions) == 0 {
		return ""
	}
	var total time.Duration
	for _, e := range stage.Executions {
		total += e.Duration()
	}
	took := total.Round(time.Millisecond).String()
	if len(stage.Executions) == 1 {
		return " in " + took
	}
	return fmt.Sprintf(" in %s over %d executions", took, len(stage.Executions))
}

// fixNote reports the last fix round's summary for a stage that had one.
//
// The summary is what a fixer agent wrote, so it goes through the same
// escaping a finding's text does. It is written on one line, and printable
// escapes a line break like any other control character, so a summary that
// carries one cannot break the line it is part of either.
func fixNote(stage machine.Stage) string {
	if stage.Fix == "" {
		return ""
	}
	return " - fixed: " + printable(stage.Fix)
}

// locationOf renders where a finding is, empty when it names nowhere.
func locationOf(f findings.Finding) string {
	if f.Location.Path == "" {
		return ""
	}
	if f.Location.Line > 0 {
		return fmt.Sprintf("%s:%d", f.Location.Path, f.Location.Line)
	}
	return f.Location.Path
}

// writef and writeln are the two writes this rendering makes. A write that
// fails has nowhere to be reported: the stream it failed on is the one a
// report would go to, and a command that stopped rendering because a pipe
// closed would tell a reader less than one that carried on.
func writef(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

func writeln(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }

// activityLine is the details line for what a segment is doing now: the
// stage, whether it is checking or in a fix round, how many rounds against
// the run's automatic limit there, and how long the node at the run's
// position has been running, in the exact terms the answer carries, because
// the details are evidence. Round counts rounds entered, so the position is
// what tells "in round 1" from "checking after round 1", and a line that
// said only "round 1 of 1" would read the same for both.
func activityLine(a machine.Activity, position string) string {
	limit := "no automatic fix rounds"
	if a.Limit > 0 {
		limit = fmt.Sprintf("%d automatic allowed", a.Limit)
	}
	var doing string
	stage, ok := pipeline.ParseStage(a.Stage)
	switch {
	case ok && (position == stage.FixNode() || position == stage.RequestNode()):
		doing = fmt.Sprintf("in fix round %d (%s)", a.Round, limit)
	case ok && position == stage.HoldNode():
		doing = fmt.Sprintf("taking an answer after %d fix round(s) (%s)", a.Round, limit)
	default:
		doing = fmt.Sprintf("checking after %d fix round(s) (%s)", a.Round, limit)
	}
	return printable(fmt.Sprintf("%s, %s, running for %s since %s",
		a.Stage, doing, a.Active.Round(time.Second), a.Since.UTC().Format(time.RFC3339)))
}

// readOutStageLog prints the log view: which log it is and where the whole
// of it is, how much of its start the read left out, and then the end of it
// line by line, every line escaped, since a log holds an agent's reply as the
// agent wrote it. It is reached through the renderings table's evidence row,
// so the phrase that points at it is written first; a log that was never
// written says so rather than printing nothing, so the pointer never stands
// over an empty document.
func readOutStageLog(w io.Writer, l machine.StageLog) {
	writef(w, "Log        run %s, stage %s, at %s\n", printable(l.Run), printable(l.Stage), printable(l.Path))
	if !l.Written {
		writeln(w, "Written    never: nothing was logged for this stage in this run")
		return
	}
	if l.Omitted > 0 {
		writef(w, "Omitted    the first %d of %d bytes; the whole log is at %s\n", l.Omitted, l.Size, printable(l.Path))
	} else {
		writef(w, "Omitted    nothing; all %d bytes follow\n", l.Size)
	}
	writeln(w)
	for _, line := range strings.Split(strings.TrimRight(l.Text, "\n"), "\n") {
		writef(w, "%s\n", printable(line))
	}
}
