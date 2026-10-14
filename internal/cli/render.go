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
// whether it speaks through internal/machine's phrase table, which is the
// vocabulary P13 holds to the derived forbidden set. A renderer that does not
// is one that still prints this build's own words, and the renderings table
// says, beside each such row, why its wording is not settled yet.
type Rendering struct {
	// Name is the renderer's name, as internal/journey's coverage note and
	// this package's tests refer to it.
	Name string
	// Phrased is whether every line it writes is a row of the phrase table,
	// or one beside text that is the person's own or an agent's, escaped.
	Phrased bool
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
		out = append(out, Rendering{Name: r.name, Phrased: r.phrased})
	}
	return append(out, Rendering{Name: fallback.name, Phrased: fallback.phrased})
}

// rendering is one row of the table readOut dispatches through: the
// renderer, the answer type it renders, which is nil for a renderer reached
// only from another renderer, and whether it is phrased.
type rendering struct {
	name    string
	phrased bool
	of      reflect.Type
	render  func(io.Writer, any)
}

// renderings is the one table of human renderers. readOut dispatches by the
// answer's type through it, and Renderings reads it out, so a renderer added
// here is enumerated and one added anywhere else is not reached.
//
// Every row but three is phrased: what it prints is a phrase from
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
// The three that are not phrased are the ones whose wording the
// specification has not settled, and each is named so that the claim about
// the rest is bounded by an enumeration:
//
//   - details prints a run's position, its park reason, its configuration
//     rejections and a line per stage by name. PRD section 9's terminal
//     interface answers "where am I?" with all nine stages by name, and P13
//     keeps stage names from a person; the two disagree, and that is a
//     finding against the specification rather than a choice to make here.
//   - findings relays a decision's findings in full, which PRD section 9's
//     relay-verbatim row requires and P13's "never text to relay" forbids;
//     the same disagreement, and the text is the stage's report rather than
//     this build's sentence, so it cannot be rephrased without being
//     summarized.
//   - plan prints the steps of a reconciliation, which PRD section 9 has
//     assistant sync print exactly; the module that would produce them does
//     not exist in this build, so there is no step whose wording could be
//     held to the table.
var renderings = []rendering{
	{"status", true, typeOf[machine.Status](), func(w io.Writer, v any) { readOutStatus(w, v.(machine.Status)) }},
	{"run", true, typeOf[machine.Run](), func(w io.Writer, v any) { readOutRun(w, v.(machine.Run)) }},
	{"runs", true, typeOf[machine.Runs](), func(w io.Writer, v any) { readOutRuns(w, v.(machine.Runs)) }},
	{"tasks", true, typeOf[machine.Tasks](), func(w io.Writer, v any) { readOutTasks(w, v.(machine.Tasks)) }},
	{"task", true, typeOf[machine.Task](), func(w io.Writer, v any) { readOutTask(w, v.(machine.Task)) }},
	{"service", true, typeOf[machine.Service](), func(w io.Writer, v any) { readOutService(w, v.(machine.Service)) }},
	{"event", true, typeOf[ipc.Event](), func(w io.Writer, v any) { writeln(w, eventLine(v.(ipc.Event))) }},
	{"host stop", true, typeOf[machine.HostStop](), func(w io.Writer, v any) { readOutHostStop(w, v.(machine.HostStop)) }},
	{"host install", true, typeOf[machine.HostInstall](), func(w io.Writer, v any) { readOutHostInstall(w, v.(machine.HostInstall)) }},
	{"decision", true, nil, func(w io.Writer, v any) { readOutDecision(w, v.(machine.Decision)) }},
	{"details", false, nil, func(w io.Writer, v any) { readOutDetails(w, v.(machine.Run)) }},
	{"findings", false, nil, func(w io.Writer, v any) { readOutFindings(w, v.(machine.Decision)) }},
	{"doctor", true, typeOf[machine.Doctor](), func(w io.Writer, v any) { readOutDoctor(w, v.(machine.Doctor)) }},
	{"lifecycle", true, typeOf[machine.Lifecycle](), func(w io.Writer, v any) { readOutLifecycle(w, v.(machine.Lifecycle)) }},
	{"init", true, typeOf[machine.Init](), func(w io.Writer, v any) { readOutInit(w, v.(machine.Init)) }},
	{"plan", false, typeOf[machine.Plan](), func(w io.Writer, v any) { readOutPlan(w, v.(machine.Plan)) }},
	{"eject", true, typeOf[machine.Eject](), func(w io.Writer, v any) { writeln(w, machine.EjectPhrase(v.(machine.Eject))) }},
	{"health", true, typeOf[machine.Health](), func(w io.Writer, v any) { writeln(w, machine.HealthPhrase(v.(machine.Health))) }},
	{"admission", true, typeOf[machine.Admission](), func(w io.Writer, v any) { readOutAdmission(w, v.(machine.Admission)) }},
	{"notification", true, typeOf[machine.Notification](), func(w io.Writer, v any) { readOutNotification(w, v.(machine.Notification)) }},
	{"task answer", true, typeOf[machine.TaskAnswered](), func(w io.Writer, v any) {
		writeln(w, machine.TaskAnsweredPhrase(v.(machine.TaskAnswered)))
	}},
}

// fallback is the row for an answer no row claims. It is not in the table,
// because it has no type to dispatch on, and it renders nothing: readOut
// returns noReading in its place, whose text is internal/machine's row for
// the refusal and which render writes on standard error.
var fallback = rendering{"fallback", true, nil, func(io.Writer, any) {}}

func typeOf[T any]() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }

// readOutStatus prints PRD section 9's status - service, gate, branch and
// active run - each through the phrase table. What it does not print is in
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
// table. The detail of why one did not is a dial error naming the socket,
// which is the structured rendering's; assistant doctor is the verb that
// diagnoses it for a person.
func readOutService(w io.Writer, s machine.Service) {
	if !s.Running {
		writeln(w, machine.PhraseServiceAbsent)
		return
	}
	writeln(w, machine.PhraseServiceUp)
}

// readOutRun prints a run: its phrase, the branch, the agent it resolved,
// what became of its local copy, and then its details, its decision and the
// findings behind the decision, through the details and findings renderers
// that are not phrased and the decision renderer that is. The phrase carries the outcome,
// the consequence and the next decision, so no outcome word, status or next
// action is printed beside it; the record's identifier is the structured
// rendering's.
func readOutRun(w io.Writer, r machine.Run) {
	writeln(w, r.Phrase())
	writef(w, "Branch     %s\n", printable(r.Record.Branch))
	// The agent the run resolved, straight off the record. It is absent for a
	// run recorded before one was resolved, and rendering nothing then is the
	// record's own honesty carried through: an unknown is not a name.
	if agent, ok := r.Record.ResolvedAgent.Get(); ok {
		writef(w, "Agent      %s\n", printable(agent))
	}
	if r.Cleanup != nil {
		if phrase, ok := machine.CleanupPhrase(*r.Cleanup); ok {
			writeln(w, phrase)
		}
	}
	readOutDetails(w, r)
	if r.Decision != nil {
		readOutDecision(w, *r.Decision)
		readOutFindings(w, *r.Decision)
	}
}

// readOutDetails prints the run's details: where its execution stands, the
// reason a bound parked it, the keys its configuration resolution dropped,
// what became of each stage, the proposals raised about it, and the inputs a
// request carried that were not applied. These are this build's own words -
// node positions, stage names, configuration keys - and the renderer is not
// phrased; it is the evidence a person reads beside the phrase above it.
func readOutDetails(w io.Writer, r machine.Run) {
	if r.Progress != nil {
		writef(w, "Position   %s after %d of %d steps\n", positionOf(r), r.Steps, r.Budget)
	}
	if r.Reason != "" {
		writef(w, "Reason     %s\n", printable(r.Reason))
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
	if len(r.NotApplied) > 0 {
		writef(w, "Not applied %s - this branch already had a run, and these start one\n",
			strings.Join(r.NotApplied, ", "))
	}
	if r.AnswerNotApplied != "" {
		writef(w, "Not applied your answer - %s\n", printable(r.AnswerNotApplied))
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

// readOutFindings prints the findings behind a decision in full.
//
// PRD section 9 requires a finding that needs a decision to be relayed with
// its full text, unsummarized and unjudged. Nothing here truncates a
// description, drops a finding, or reorders them. That is also why this
// renderer is not phrased: a finding is the stage's report, and phrasing it
// would summarize it.
func readOutFindings(w io.Writer, d machine.Decision) {
	for _, finding := range d.Findings {
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
// and its phrase from the record alone, on machine.RecordPhrase's terms. The
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
// no outcome word, which the phrase table holds to.
func readOutTask(w io.Writer, task machine.Task) {
	writef(w, "%s: %s\n", printable(task.Record.Project), printable(machine.TaskPhrase(task).String()))
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
// phrase from the record, as the run listing prints one.
func readOutLifecycle(w io.Writer, answer machine.Lifecycle) {
	writeln(w, machine.LifecyclePhrase(answer))
	for _, record := range answer.Active {
		writef(w, "  %s: %s\n", printable(record.Branch), machine.RecordPhrase(record))
	}
}

// readOutInit prints what became of the checkout's gate, and the branch
// trusted configuration is read from, which is the person's own branch name
// and the one fact here they may want to correct with --default-branch. The
// gate's identifier and where it lives are the structured rendering's.
func readOutInit(w io.Writer, answer machine.Init) {
	writeln(w, machine.InitPhrase(answer))
	if branch := answer.Repository.DefaultBranch; branch != "" {
		writef(w, "Default branch %s\n", printable(branch))
	}
}

// readOutPlan prints a plan's steps and why nothing was done. It is not
// phrased; the renderings table says why.
func readOutPlan(w io.Writer, plan machine.Plan) {
	for _, step := range plan.Steps {
		writef(w, "  %s\n", step)
	}
	if plan.Detail != "" {
		writeln(w, plan.Detail)
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
