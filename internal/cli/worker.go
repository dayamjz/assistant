package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
)

// workerVerbName is the fourth command this binary answers to that PRD
// section 9's table does not name: a task's worker's route back to the
// program, which #226 asked for. It is dispatched beside the table, as
// coordinatorVerbName is, rather than added to it, and section 9 names it
// there by the owner's decision. That decision is not a precedent: the three
// before it are not a reason for it, and it is not a reason for a fifth.
//
// It is the interface a launched worker requires. The service starts every
// worker with workerRoute's text as its route: the grant internal/agents
// writes allows commands beginning with exactly that text, and the
// instructions internal/service appends tell the worker what to type after
// it. Every subcommand is one call of task.event, and internal/service
// decides every one of them: who the caller is the worker of comes from the
// credentials the socket reports and the caller's parent chain, never from
// anything this command sends, so a person's shell running the same command
// line is refused with ipc.ErrNotWorker, which arrives typed and exits as an
// operational failure through failureExit. This command decides nothing
// about who may call it and names no task. The gap in that placement is the
// one coordinatorVerbName names, from the other side: a worker's command
// whose parent chain the kernel rewrote is placed as no worker's and
// refused.
const workerVerbName = "worker"

// workerActions are the worker's subcommands, on coordinatorActions' terms:
// the one owner of which there are. Each is one task.event: acknowledge and
// report are the two kinds with a record beside the history, given commands
// of their own because their detail is a number and a document rather than
// a word; event is every other kind internal/taskstate gives a worker.
var workerActions = []struct {
	name     string
	operands string
	summary  string
	run      func(context.Context, *invocation) (any, error)
}{
	{"acknowledge", "<message>", "Record that the message with this number was read and acted on.", workerAcknowledge},
	{"report", "", "File the investigation's report, read from --file and removed once filed; - reads standard input.", workerReport},
	{"event", "<kind>", "Record how the work stands, as one of the kinds a worker may append.", workerEvent},
}

// workerRoute is the route the service gives a worker: this program's
// absolute path, --home naming root, and this verb, each word shell-quoted
// where it needs it, as hostInstall writes the host's command line. It is
// the one composition of that text; cli's service start passes it to
// internal/service, which hands it to the grant and the instructions alike.
func workerRoute(executable, root string) string {
	return shellQuote(executable) + " --" + homeFlagName + " " + shellQuote(root) + " " + workerVerbName
}

// workerVerb runs the subcommand the command line names, which has to be the
// first thing after the verb, on coordinatorVerb's terms.
func workerVerb(ctx context.Context, in *invocation) (any, error) {
	if len(in.args) > 0 {
		named := in.args[0]
		for _, action := range workerActions {
			if named != action.name {
				continue
			}
			in.args = in.args[1:]
			answer, err := action.run(ctx, in)
			var help helpAnswer
			if action.operands != "" && errors.As(err, &help) {
				help.text = strings.Replace(help.text, " [flags]", " "+action.operands+" [flags]", 1)
				return nil, help
			}
			return answer, err
		}
		if isHelp(named) {
			return nil, subcommandHelp(workerVerbName, workerUsage())
		}
	}
	return nil, subcommandRefusal(workerVerbName, in.args, workerActionNames())
}

// workerActionNames is the subcommands read out, for a refusal that says
// what was expected.
func workerActionNames() []string {
	names := make([]string, 0, len(workerActions))
	for _, action := range workerActions {
		names = append(names, action.name)
	}
	return names
}

// workerUsage is the subcommands read out under a heading saying who runs
// them, built from workerActions.
func workerUsage() string {
	var b strings.Builder
	b.WriteString("\nRun by a task's worker to tell the program about its work, and refused by the service for anyone else:\n")
	for _, action := range workerActions {
		fmt.Fprintf(&b, "  %s %-11s %-9s %s\n", workerVerbName, action.name, action.operands, action.summary)
	}
	return strings.TrimRight(b.String(), "\n")
}

// workerCommand is how a subcommand names itself to the flag set and in its
// own help.
func workerCommand(action string) string {
	return workerVerbName + " " + action
}

// workerAcknowledge is task.event for store.MessageAcknowledgedEventKind,
// naming the message by the number it arrived under. A number that is not
// one is incorrect usage here; one that names no message of the caller's
// task, or one already acknowledged, is the service's refusal.
func workerAcknowledge(ctx context.Context, in *invocation) (any, error) {
	name := workerCommand("acknowledge")
	if err := in.parseArgs(name, 1, func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	if len(in.args) != 1 {
		return nil, usagef("assistant %s names the message it acknowledges, by the number it arrived under", name)
	}
	if _, err := strconv.ParseInt(in.args[0], 10, 64); err != nil {
		return nil, usagef("assistant %s takes a message number, and %q is not one", name, printable(in.args[0]))
	}
	return in.appendEvent(ctx, store.MessageAcknowledgedEventKind, in.args[0])
}

// workerReport is task.event for store.ReportReadyEventKind, with the
// report as the request's detail. The report is read whole from --file, a
// path resolved against the directory the command was run in, or - for
// standard input. Whether the report can be read, and whether the task may
// file one, is the service's; an empty report, a missing --file and one over
// the protocol's bound on a request are incorrect usage here. A report under
// that bound whose request is still over it once encoded is refused by the
// protocol rather than here.
//
// A file named by --file is removed once the service answers that the report
// was filed, and not before, so a report the service refused stays where the
// worker wrote it to be corrected. That is #267's form: internal/service's
// route tells a worker to write its report into a file in its own copy with
// its file editing tool and name it here, because the Claude Code CLI's Bash
// check refused a here-document carrying the report on standard input. A
// file left in the copy would be an untracked file that keeps the copy from
// being cleaned up, and one that could be committed if the investigation is
// promoted to a delivery, so this command is what removes it. A removal that
// fails is reported as a reportFileKept, an operational failure naming the
// entry that was filed, so a worker reading the answer knows the report is in
// and the file is still there. Standard input leaves nothing to remove.
//
// What that does not cover: a worker whose command is ended between the
// answer and the removal leaves the file, which the copy's cleanup then
// refuses on as untracked rather than removing; and the file named is
// removed whatever it is, a file the repository tracks included, which
// leaves that copy's tree dirty and its cleanup refused in the same way.
func workerReport(ctx context.Context, in *invocation) (any, error) {
	name := workerCommand("report")
	var file string
	if err := in.parseFlags(name, func(set *flag.FlagSet) {
		set.StringVar(&file, "file", "", "the report, removed once it is filed, or - to read it from standard input")
	}); err != nil {
		return nil, err
	}
	if file == "" {
		return nil, usagef("assistant %s needs --file, naming the report or - for standard input", name)
	}
	var source io.Reader
	switch {
	case file == "-" && in.env.Stdin == nil:
		return nil, usagef("assistant %s --file - was given no standard input", name)
	case file == "-":
		source = in.env.Stdin
	default:
		if !filepath.IsAbs(file) {
			file = filepath.Join(in.env.WorkingDir, file)
		}
		f, err := os.Open(file)
		if err != nil {
			return nil, usagef("--file could not be read: %v", err)
		}
		defer func() { _ = f.Close() }()
		source = f
	}
	raw, err := io.ReadAll(io.LimitReader(source, ipc.DefaultMaxFrameBytes+1))
	if err != nil {
		return nil, usagef("the report could not be read: %v", err)
	}
	switch {
	case len(raw) > ipc.DefaultMaxFrameBytes:
		return nil, usagef("the report is over %d bytes, the protocol's bound on one request, and is refused rather than cut", ipc.DefaultMaxFrameBytes)
	case strings.TrimSpace(string(raw)) == "":
		return nil, usagef("the report is empty")
	}
	answer, err := in.appendEvent(ctx, store.ReportReadyEventKind, string(raw))
	if err != nil || file == "-" {
		return answer, err
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		appended, _ := answer.(machine.Appended)
		return nil, reportFileKept{path: file, entry: appended.Event.Sequence, err: err}
	}
	return answer, nil
}

// reportFileKept is a report the service filed whose file could not be
// removed afterwards. It is not a refusal of the report, which stands; it
// exits as an operational failure so the worker is told the file is still in
// its copy and how to remove it.
type reportFileKept struct {
	path  string
	entry int64
	err   error
}

func (e reportFileKept) Error() string {
	return fmt.Sprintf("the report was filed as entry %d of the history, and %s could not be removed afterwards: %v; "+
		"remove it with git clean -f -- followed by that path, because a file left in the copy keeps it from being cleaned up",
		e.entry, e.path, e.err)
}

func (e reportFileKept) Unwrap() error { return e.err }

// workerEvent is task.event for the kind named as the operand, with
// --detail as its detail. Which kinds a worker may append, and what detail
// each takes, are internal/taskstate's and the service refuses the rest, so
// nothing here holds a second list of them.
func workerEvent(ctx context.Context, in *invocation) (any, error) {
	name := workerCommand("event")
	var detail string
	if err := in.parseArgs(name, 1, func(set *flag.FlagSet) {
		set.StringVar(&detail, "detail", "", "what the worker says about it")
	}); err != nil {
		return nil, err
	}
	if len(in.args) != 1 || strings.TrimSpace(in.args[0]) == "" {
		return nil, usagef("assistant %s names the kind it records", name)
	}
	return in.appendEvent(ctx, in.args[0], detail)
}

// appendEvent is the one call every subcommand makes.
func (in *invocation) appendEvent(ctx context.Context, kind, detail string) (any, error) {
	var appended machine.Appended
	err := in.callService(ctx, ipc.MethodTaskEvent, machine.TaskEventRequest{Kind: kind, Detail: detail}, &appended)
	return appended, err
}
