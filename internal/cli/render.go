package cli

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
)

// readOut writes an answer for a person.
//
// It is a second rendering of the same value the structured mode writes, not a
// second source of it: every branch here reads fields off a shape
// internal/machine owns, so the two surfaces cannot report different things.
//
// There is no colour and no cursor movement. PRD section 9 asks the terminal
// interface to use only the terminal's own palette so it follows the user's
// theme; the safest reading of that for a command that prints and exits is to
// emit no styling at all.
func readOut(w io.Writer, answer any) {
	switch v := answer.(type) {
	case nil:
	case machine.Status:
		readOutStatus(w, v)
	case machine.Run:
		readOutRun(w, v)
	case machine.Runs:
		readOutRuns(w, v)
	case machine.Tasks:
		readOutTasks(w, v)
	case machine.Task:
		readOutTask(w, v)
	case machine.Doctor:
		readOutDoctor(w, v)
	case machine.Service:
		readOutService(w, v)
	case machine.Lifecycle:
		readOutLifecycle(w, v)
	case machine.Init:
		readOutInit(w, v)
	case machine.Eject:
		writef(w, "Removed the gate %s and %d run record(s).\n", v.Repository, v.Runs)
	case machine.Plan:
		readOutPlan(w, v)
	case machine.Health:
		writef(w, "ready: %v, home %s, build %s\n", v.Ready, v.Home, v.Build)
	case ipc.Event:
		writeln(w, eventLine(v))
	default:
		writef(w, "%+v\n", v)
	}
}

func readOutStatus(w io.Writer, s machine.Status) {
	writef(w, "Home       %s\n", s.Home)
	readOutService(w, s.Service)
	if s.Branch != nil {
		writef(w, "Working    %s\n", s.Branch.WorkingPath)
		if s.Branch.Name != "" {
			writef(w, "Branch     %s at %s\n", s.Branch.Name, short(s.Branch.Head))
		}
		if s.Branch.Detail != "" {
			writef(w, "Branch     %s\n", s.Branch.Detail)
		}
	}
	if s.Repository != nil {
		writef(w, "Repository %s, default branch %s\n", s.Repository.ID, s.Repository.DefaultBranch)
		writef(w, "Upstream   %s\n", s.Repository.UpstreamURL)
	}
	if s.Gate != nil {
		if s.Gate.Present {
			writef(w, "Gate       %s\n", s.Gate.ID)
		} else {
			writef(w, "Gate       %s\n", s.Gate.Detail)
		}
	}
	if s.ActiveRun != nil {
		writeln(w)
		readOutRun(w, *s.ActiveRun)
		return
	}
	if s.Detail != "" {
		writeln(w, s.Detail)
		return
	}
	writeln(w, "No run is in flight for this branch.")
}

func readOutService(w io.Writer, s machine.Service) {
	if !s.Running {
		writef(w, "Service    not running (%s)\n", serviceDetail(s))
		return
	}
	writef(w, "Service    running on %s, build %s\n", s.Socket, s.Build)
}

func readOutRun(w io.Writer, r machine.Run) {
	writef(w, "Run        %s on %s (%s)\n", r.Record.ID, r.Record.Branch, r.Record.Status)
	writef(w, "Outcome    %s\n", r.Outcome)
	if r.Progress != nil {
		writef(w, "Position   %s after %d of %d steps\n", positionOf(r), r.Steps, r.Budget)
	}
	if r.Reason != "" {
		writef(w, "Reason     %s\n", r.Reason)
	}
	if len(r.Stages) > 0 {
		writeln(w, "\nStages")
		for _, stage := range r.Stages {
			writef(w, "  %-9s %s%s\n", stage.Stage, outcomeWord(stage), fixNote(stage))
		}
	}
	if r.Decision != nil {
		readOutDecision(w, *r.Decision)
	}
	if r.NextAction != "" {
		writef(w, "\nNext: %s\n", r.NextAction)
	}
}

// readOutDecision prints the decision and the findings behind it in full.
//
// PRD section 9 requires a finding that needs a decision to be relayed with
// its full text, unsummarized and unjudged. Nothing here truncates a
// description, drops a finding, or reorders them.
func readOutDecision(w io.Writer, d machine.Decision) {
	writef(w, "\nWaiting on you: %s\n", printable(d.Question))
	if len(d.Options) > 0 {
		options := make([]string, len(d.Options))
		for i, option := range d.Options {
			options[i] = printable(option)
		}
		writef(w, "Options: %s\n", strings.Join(options, ", "))
		writef(w, "Answer with: assistant --answer %s\n", options[0])
	}
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

func readOutRuns(w io.Writer, runs machine.Runs) {
	if len(runs.Runs) == 0 {
		writeln(w, "No runs recorded for this repository.")
		return
	}
	for _, record := range runs.Runs {
		writef(w, "%s  %-10s %-24s %s\n", record.ID, record.Status, record.Branch,
			record.CreatedAt.Format("2006-01-02 15:04:05"))
	}
}

func readOutTasks(w io.Writer, tasks machine.Tasks) {
	if len(tasks.Tasks) == 0 {
		writeln(w, "No fleet work is recorded.")
		return
	}
	for _, task := range tasks.Tasks {
		readOutTask(w, task)
	}
}

func readOutTask(w io.Writer, task machine.Task) {
	state := task.State.State
	if state == "" {
		state = "no state recorded"
	}
	writef(w, "%s  %-10s %-16s %s\n", task.Record.ID, task.Record.Shape, state, task.Record.Project)
}

func readOutDoctor(w io.Writer, report machine.Doctor) {
	for _, check := range report.Checks {
		mark := "no "
		if check.OK {
			mark = "yes"
		}
		writef(w, "%s  %-13s %s\n", mark, check.Name, check.Detail)
	}
	writeln(w)
	if report.CanStartRun {
		writeln(w, "A run can start.")
		return
	}
	writef(w, "A run cannot start. %s\n", report.Detail)
}

func readOutLifecycle(w io.Writer, answer machine.Lifecycle) {
	if answer.Accepted {
		if answer.Restarting {
			writeln(w, "The service is restarting.")
			return
		}
		writeln(w, "The service is stopping.")
		return
	}
	writeln(w, answer.Detail)
	for _, record := range answer.Active {
		writef(w, "  %s  %-10s %s\n", record.ID, record.Status, record.Branch)
	}
}

func readOutInit(w io.Writer, answer machine.Init) {
	word := "Created"
	if answer.Reattached {
		word = "Reattached to"
	}
	writef(w, "%s the gate %s at %s\n", word, answer.Gate.ID, answer.Gate.Repository)
	writef(w, "Repository %s, default branch %s\n", answer.Repository.ID, answer.Repository.DefaultBranch)
	writef(w, "Push to it with: git push %s\n", gate.RemoteName)
}

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

// short renders a commit for reading, and leaves anything that is not one
// alone.
func short(commit string) string {
	if len(commit) <= 12 {
		return commit
	}
	return commit[:12]
}

// writef and writeln are the two writes this rendering makes. A write that
// fails has nowhere to be reported: the stream it failed on is the one a
// report would go to, and a command that stopped rendering because a pipe
// closed would tell a reader less than one that carried on.
func writef(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

func writeln(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
