package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// A worker's route back to the program: the one command line, besides git,
// a launched worker is told it may run and is granted, and what the service
// does with what arrives over it.
//
// # What the route is
//
// Options.WorkerRoute is a command line prefix: the absolute path of this
// program, its --home naming this service's home, and the worker verb
// internal/cli dispatches beside PRD section 9's table, shell-quoted where a
// path needs it. internal/cli composes it, because it owns the verb and the
// flag, and passes it in when it starts the service; this service decides
// nothing about its text. launchTask hands it to the worker twice, as one
// value: as session.Spec.Route, which internal/agents' worker grant allows
// commands beginning with, and in routeSection, which tells the worker what
// to type after it. So the prefix the grant allows and the prefix the
// worker is told are the same text, and internal/cli's
// TestAWorkerReportsBackThroughTheRouteItsGrantAllows, which has a worker
// run the commands the section names, read back out of the instructions it
// was started with, is what holds the section's subcommands to the ones the
// verb serves.
//
// A service opened with no route launches workers with neither: no section
// and git alone, which is what a test that opens the service without
// internal/cli gets. The product always passes one.
//
// # What arrives over it
//
// Every subcommand is task.event, so who may call is workers.go's rule and
// nothing here: the caller is placed from the socket's credentials and its
// parent chain, never from anything the command sends, a caller placed as
// no task's worker is refused with ipc.ErrNotWorker, and the task appended
// to is the placement's. A person's shell running the same command line is
// such a caller. Two kinds carry a record beside the history and are
// written through it: a message acknowledgement, through steer.go's
// acknowledgeMessage, and a report, through fileReport below, whose detail
// on the request is the report and not the entry's.
//
// # Residual gaps
//
//   - The grant allows a prefix. What the worker runs after it is the verb's
//     to refuse, and the verb's subcommands are each one task.event, so the
//     widening is to calls this service already decides; but the prefix is
//     text the agent's own permission system matches, and how it matches is
//     that system's, not this package's.
//   - A route whose program or home path the operating system later moves
//     or replaces names whatever then stands there. The route is fixed when
//     the service starts and is not re-read.
//   - On a platform where internal/ipc identifies no peer, every call over
//     the route is refused as unidentified, so the route is offered and
//     cannot succeed there; workers.go states that limit for every rule.
//   - A report is one frame on the local protocol, so one larger than
//     ipc.DefaultMaxFrameBytes is refused by the protocol before this
//     service reads it.

// routeHeading is the heading routeSection writes the route under.
const routeHeading = "## Reaching the program, written by the program at launch"

// The words routeSection tells a worker to type after the route. They are
// internal/cli's worker subcommands, which that package's workerActions
// table owns; the test the file comment names is what holds the two
// together.
const (
	routeAcknowledge = "acknowledge"
	routeReport      = "report"
	routeEvent       = "event"
)

// routeReportFile is the file routeSection tells an investigation's worker to
// write its report into, at the top of its copy, and to name to the report
// command. #267 found that the Claude Code CLI's Bash check refused the
// here-document this section used to prescribe, a command carrying JSON
// braces and quotes, and that writing a file outside the copy needed an
// approval nobody was there to give; a file in the copy, written with the
// worker's file editing tool, is what a worker launched under the grant can
// make. The report command removes the file once the report is filed, which
// internal/cli's workerReport owns, so it does not stay in the copy to keep
// the copy from being cleaned up or to be committed if the investigation is
// promoted.
const routeReportFile = "assistant-report.json"

// routeSection is what a launch appends to a worker's instructions when this
// service was given a route: the commands the worker may run to tell the
// program about its work, each beginning with the route exactly as the
// grant allows it. The report command is named only for an investigation
// task, whose deliverable it is; a delivery task's report is refused by
// internal/tasks. The event kinds listed are internal/taskstate's rows a
// worker writes, less the two the section names commands of their own for,
// so the list is that table and not a copy of it. It is empty for a service
// given no route.
func (s *Service) routeSection(shape store.TaskShape) string {
	if s.workerRoute == "" {
		return ""
	}
	var kinds []string
	for _, row := range taskstate.Kinds() {
		if row.Writer != taskstate.WriterWorker || row.Kind == store.MessageAcknowledgedEventKind ||
			row.Kind == store.ReportReadyEventKind {
			continue
		}
		kinds = append(kinds, row.Kind)
	}
	var b strings.Builder
	b.WriteString("\n\n" + routeHeading + "\n\n")
	b.WriteString("You tell the program about your work with one command besides git, run in the working copy you were " +
		"started in. Your permissions allow commands that begin exactly with the text below, so type it as written, " +
		"at the start of the command, and check the answer: a refusal says what was wrong.\n")
	b.WriteString("\nThe coordinator's messages reach you each under a heading \"Message NUMBER\". Once you have read one " +
		"and acted on it, acknowledge it once, by its number. A message you do not acknowledge is reported to the " +
		"coordinator as unread.\n\n")
	fmt.Fprintf(&b, "    %s %s NUMBER\n", s.workerRoute, routeAcknowledge)
	if shape == store.TaskInvestigation {
		fmt.Fprintf(&b, "\nWhen the investigation is finished, file your report once. Write it with your file editing tool, "+
			"not through the shell, into the file %s at the top of this working copy, as one JSON object with a \"summary\" "+
			"and a list of \"findings\", each with a \"description\" and an \"action\": \"ask\" for a decision a person has to "+
			"make, \"note\" for something to know, \"fix\" for a change you would make, which no one makes on the strength "+
			"of the report. For example: {\"summary\": \"...\", \"findings\": [{\"action\": \"ask\", \"description\": "+
			"\"...\"}]}. A finding with no action, or one that is not of those three, is held for a person as an ask. "+
			"Beside the findings, the report may carry \"uncertainties\", what you could not settle, and \"follow_ups\", "+
			"further work you recommend, each a list of objects with a \"text\" and an optional \"location\", such as "+
			"\"uncertainties\": [{\"text\": \"...\", \"location\": \"path/file.go:12\"}]. These hold nothing and start "+
			"nothing, so a question a person has to decide is an ask finding and not an uncertainty; an uncertainty or a "+
			"follow-up written with an \"action\" is refused. Then "+
			"run the command below from the top of this working copy. Once the report is filed the command removes that "+
			"file: a file left in this copy keeps the copy from being cleaned up and must never be committed. If the "+
			"answer says the file could not be removed, remove it with git clean -f -- %s. A report that cannot be read is "+
			"refused, nothing is filed, and the file is kept for you to correct; a second report is refused and the "+
			"first stands.\n\n", routeReportFile, routeReportFile)
		fmt.Fprintf(&b, "    %s %s --file %s\n", s.workerRoute, routeReport, routeReportFile)
	}
	fmt.Fprintf(&b, "\nTo record how the work stands, name one of these kinds, with a short detail if it helps: %s.\n\n",
		strings.Join(kinds, ", "))
	fmt.Fprintf(&b, "    %s %s KIND --detail \"...\"\n", s.workerRoute, routeEvent)
	return b.String()
}

// fileReport is task.event for store.ReportReadyEventKind: the request's
// detail is the investigation's report, which storeTaskReport stores
// through internal/tasks with its holds and the history entry in one write,
// and the answer is that entry, published as every worker entry is. A
// report internal/tasks cannot read, one filed for a task that is not an
// investigation, and a second report are ipc.ErrInvalidRequest with nothing
// stored; the first report stands. task is the placement's.
func (s *Service) fileReport(ctx context.Context, task, raw string) (machine.Appended, error) {
	if strings.TrimSpace(raw) == "" {
		return machine.Appended{}, fmt.Errorf("%w: a report carries no text", ipc.ErrInvalidRequest)
	}
	stored, err := s.storeTaskReport(ctx, task, raw)
	switch {
	case errors.Is(err, tasks.ErrReportUnreadable), errors.Is(err, tasks.ErrNotInvestigation),
		errors.Is(err, store.ErrTaskReportStored):
		return machine.Appended{}, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
	case err != nil:
		return machine.Appended{}, err
	}
	return machine.Appended{Event: stored.Event}, nil
}
