package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// coordinatorVerbName is the third command this binary answers to that PRD
// section 9's table does not name. The owner decided on #227 that the
// coordinator's service methods get a client dispatched beside the table,
// as gateVerbName and hostVerbName are, rather than rows in it; section 9
// says so beside the table.
//
// It is the interface the coordinator agent requires. The methods it calls
// are internal/ipc's and internal/service decides every one of them: each
// is restricted, and each is refused for a caller the service places inside
// a task's worker group from the credentials the socket reports, never from
// anything this command sends. So this command decides nothing about who
// may call it and holds no second copy of that rule; a worker that runs it
// is refused by the service with ipc.ErrWorker, which arrives typed and
// exits as an operational failure through failureExit like every other
// refusal.
//
// The refusal is only as good as that placement, and the placement has a
// gap this command cannot close: a process is placed by its process group,
// and #225 records a Claude Code worker whose git ran outside the worker's
// group. A call made from a process outside every worker group is placed as
// the coordinator's and served, so the refusal is not established for such
// a worker; the tests place their caller in the worker's group. What a
// worker may run at all is its grant's, which allows git without asking and
// denies nothing else.
//
// It is one verb rather than one per method, so the door beside the table
// is one door: the subcommands are coordinatorActions, the one owner of
// which there are. The methods listing fleet work, tasks.list and task.get,
// are not here, because section 9's table already serves both as assistant
// tasks; nor is answering a task's hold, which section 9 makes --answer
// given --task. task.interrupt is not here either: the service refuses it
// on every call, because no adapter declares the capability it needs, so a
// subcommand for it could only ever fail.
const coordinatorVerbName = "coordinator"

// coordinatorActions are the coordinator's subcommands. The list is the one
// owner of which there are, on gateHooks' terms: it names the subcommand,
// the operands it takes, what it does, and the function that runs it.
var coordinatorActions = []struct {
	name     string
	operands string
	summary  string
	run      func(context.Context, *invocation) (any, error)
}{
	{"create", "", "Record a new task through intake; a task nothing blocks has its worker begun after the answer.", coordinatorCreate},
	{"message", "<task>", "Send the worker of a running task a message, recorded before it is written.", coordinatorMessage},
	{"stop", "<task>", "End a task's worker, prove it ended, and hold the task for an answer.", coordinatorStop},
	{"relaunch", "<task>", "Begin a new attempt of a task whose worker is gone, in its own copy.", coordinatorRelaunch},
	{"promote", "<task>", "Turn an investigation task into a delivery on a branch, in its own copy.", coordinatorPromote},
	{"wakes", "", "List the queued wakes, oldest first, acknowledging none.", coordinatorWakes},
	{"acknowledge", "<wake>...", "Record that the named wakes were handled, so a later list leaves them out.", coordinatorAcknowledge},
	{"supervision", "", "Verify that this home's service is watching the fleet.", coordinatorSupervision},
}

// coordinatorVerb runs the subcommand the command line names, which has to
// be the first thing after the verb, for gateVerb's reason: several
// subcommands take flags with values, so a scan that took the subcommand
// from wherever it stood would read "--text stop" as naming stop. --json
// and --home written before the verb are already read by parseGlobal, and
// written after the subcommand they reach its own flag set.
//
// --help where the subcommand belongs is answered with the subcommand
// table, and anything else there, or nothing, is incorrect usage on
// gateVerb's terms.
func coordinatorVerb(ctx context.Context, in *invocation) (any, error) {
	if len(in.args) > 0 {
		named := in.args[0]
		for _, action := range coordinatorActions {
			if named != action.name {
				continue
			}
			in.args = in.args[1:]
			answer, err := action.run(ctx, in)
			// A subcommand's help is its flag set read out, which knows
			// nothing of operands, so the usage line is given the ones the
			// table names.
			var help helpAnswer
			if action.operands != "" && errors.As(err, &help) {
				help.text = strings.Replace(help.text, " [flags]", " "+action.operands+" [flags]", 1)
				return nil, help
			}
			return answer, err
		}
		if isHelp(named) {
			return nil, subcommandHelp(coordinatorVerbName, coordinatorUsage())
		}
	}
	return nil, subcommandRefusal(coordinatorVerbName, in.args, coordinatorActionNames())
}

// coordinatorActionNames is the subcommands read out, for a refusal that
// says what was expected.
func coordinatorActionNames() []string {
	names := make([]string, 0, len(coordinatorActions))
	for _, action := range coordinatorActions {
		names = append(names, action.name)
	}
	return names
}

// coordinatorUsage is the subcommands read out under a heading saying who
// runs them, built from coordinatorActions so a subcommand added there is
// one this describes.
func coordinatorUsage() string {
	var b strings.Builder
	b.WriteString("\nRun by the coordinator to drive the fleet, and refused by the service for a task's worker:\n")
	for _, action := range coordinatorActions {
		fmt.Fprintf(&b, "  %s %-11s %-9s %s\n", coordinatorVerbName, action.name, action.operands, action.summary)
	}
	return strings.TrimRight(b.String(), "\n")
}

// coordinatorCommand is how a subcommand names itself to the flag set and
// in its own help.
func coordinatorCommand(action string) string {
	return coordinatorVerbName + " " + action
}

// coordinatorCreate is task.create: what intake records is internal/tasks',
// and which combinations it refuses are intake's too, answered as
// ipc.ErrInvalidRequest and so as incorrect usage. The one thing decided
// here is where the instructions come from: --instructions or
// --instructions-file, exactly one, because a command line that gave both
// would leave which one the worker reads to an order nobody wrote down.
func coordinatorCreate(ctx context.Context, in *invocation) (any, error) {
	var request machine.TaskCreateRequest
	var instructions, instructionsFile string
	err := in.parseFlags(coordinatorCommand("create"), func(set *flag.FlagSet) {
		set.StringVar(&request.Shape, "shape", "", `"delivery" or "investigation"`)
		set.StringVar(&request.Project, "project", "", "what the task is for")
		set.StringVar(&instructions, "instructions", "", "what the worker is launched with")
		set.StringVar(&instructionsFile, "instructions-file", "", "a file holding what the worker is launched with, in place of --instructions")
		set.StringVar(&request.Mode, "mode", "", "how the worker is to be launched, recorded as given")
		set.StringVar(&request.Repository, "repository", "", "the repository record's identifier, which assistant --json status shows")
		set.StringVar(&request.Branch, "branch", "", "the branch a delivery task works on")
		set.StringVar(&request.Overlaps, "overlaps", "", "a task this one overlaps, or empty for a claim of independence")
		set.StringVar(&request.Delivery, "delivery", "", `"gated" or "local" in place of the project's posture, never lighter than it`)
	})
	if err != nil {
		return nil, err
	}
	if request.Instructions, err = in.textOrFile("instructions", instructions, instructionsFile); err != nil {
		return nil, err
	}
	var created machine.TaskCreated
	err = in.callService(ctx, ipc.MethodTaskCreate, request, &created)
	return created, err
}

// coordinatorMessage is task.message: the text goes to the worker of the
// task's latest attempt, which the service reads and this does not name.
// Who sent it is derived by the service from the surface it arrived on.
func coordinatorMessage(ctx context.Context, in *invocation) (any, error) {
	var text, textFile string
	task, err := in.taskOperand(coordinatorCommand("message"), func(set *flag.FlagSet) {
		set.StringVar(&text, "text", "", "what the worker is sent")
		set.StringVar(&textFile, "text-file", "", "a file holding what the worker is sent, in place of --text")
	})
	if err != nil {
		return nil, err
	}
	request := machine.TaskMessageRequest{Task: task}
	if request.Text, err = in.textOrFile("text", text, textFile); err != nil {
		return nil, err
	}
	var messaged machine.TaskMessaged
	err = in.callService(ctx, ipc.MethodTaskMessage, request, &messaged)
	return messaged, err
}

// coordinatorStop is task.stop. It answers once the probe read the worker
// dead; a stop the probe did not prove is the service's refusal, and no
// hold is registered for it.
func coordinatorStop(ctx context.Context, in *invocation) (any, error) {
	task, err := in.taskOperand(coordinatorCommand("stop"), func(*flag.FlagSet) {})
	if err != nil {
		return nil, err
	}
	var stopped machine.TaskStopped
	err = in.callService(ctx, ipc.MethodTaskStop, machine.TaskRequest{Task: task}, &stopped)
	return stopped, err
}

// coordinatorRelaunch is task.relaunch, through the one anchored relaunch
// PRD section 11's reconciliation uses. A task whose worker is still live,
// and one a stop holds, are refused by the service: the relaunch after a
// stop is the answer to the stop's hold, given with --task and --answer.
func coordinatorRelaunch(ctx context.Context, in *invocation) (any, error) {
	task, err := in.taskOperand(coordinatorCommand("relaunch"), func(*flag.FlagSet) {})
	if err != nil {
		return nil, err
	}
	var relaunched machine.TaskRelaunched
	err = in.callService(ctx, ipc.MethodTaskRelaunch, machine.TaskRequest{Task: task}, &relaunched)
	return relaunched, err
}

// coordinatorPromote is task.promote: the investigation named becomes a
// delivery on the branch given, in place. What promotion checks is
// internal/tasks'; the intent comes from --intent or --intent-file on
// coordinatorCreate's terms.
func coordinatorPromote(ctx context.Context, in *invocation) (any, error) {
	var request machine.TaskPromoteRequest
	var intent, intentFile string
	task, err := in.taskOperand(coordinatorCommand("promote"), func(set *flag.FlagSet) {
		set.StringVar(&request.Repository, "repository", "", "the repository record's identifier, the one the task's copy was cut from")
		set.StringVar(&request.Branch, "branch", "", "the branch the delivery works on")
		set.StringVar(&intent, "intent", "", "what the delivery is to do")
		set.StringVar(&intentFile, "intent-file", "", "a file holding what the delivery is to do, in place of --intent")
		set.StringVar(&request.Delivery, "delivery", "", `"gated" or "local" in place of the project's posture, never lighter than it`)
	})
	if err != nil {
		return nil, err
	}
	request.Task = task
	if request.Intent, err = in.textOrFile("intent", intent, intentFile); err != nil {
		return nil, err
	}
	var promoted machine.TaskPromoted
	err = in.callService(ctx, ipc.MethodTaskPromote, request, &promoted)
	return promoted, err
}

// coordinatorWakes is wakes.list. It acknowledges nothing, so a wake it
// lists is listed again until coordinatorAcknowledge names it.
func coordinatorWakes(ctx context.Context, in *invocation) (any, error) {
	var request machine.WakesRequest
	err := in.parseFlags(coordinatorCommand("wakes"), func(set *flag.FlagSet) {
		set.IntVar(&request.Limit, "limit", 0, fmt.Sprintf("at most this many wakes, oldest first; 0 is %d", machine.DefaultWakesLimit))
	})
	if err != nil {
		return nil, err
	}
	if request.Limit < 0 {
		return nil, usagef("assistant %s --limit is %d, and a limit is never negative", coordinatorCommand("wakes"), request.Limit)
	}
	var wakes machine.Wakes
	err = in.callService(ctx, ipc.MethodWakesList, request, &wakes)
	return wakes, err
}

// coordinatorAcknowledge is wakes.acknowledge, naming the wakes as
// operands. Who acknowledged is derived by the service from the surface
// and is not something this sends. A batch naming a wake no row has, or one
// already acknowledged, is the store's refusal of the whole batch, answered
// as ipc.ErrInvalidRequest.
func coordinatorAcknowledge(ctx context.Context, in *invocation) (any, error) {
	name := coordinatorCommand("acknowledge")
	if err := in.parseArgs(name, math.MaxInt, func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	if len(in.args) == 0 {
		return nil, usagef("assistant %s names the wakes it acknowledges, as assistant %s --json lists them", name, coordinatorCommand("wakes"))
	}
	request := machine.AcknowledgeRequest{Wakes: make([]int64, 0, len(in.args))}
	for _, arg := range in.args {
		id, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			return nil, usagef("assistant %s takes wake identifiers, and %q is not one", name, printable(arg))
		}
		request.Wakes = append(request.Wakes, id)
	}
	var acknowledged machine.Acknowledged
	err := in.callService(ctx, ipc.MethodWakesAcknowledge, request, &acknowledged)
	return acknowledged, err
}

// coordinatorSupervision is supervision.assert, PRD section 5's arm
// verification. The answer is internal/supervisor's Arm as it stands, and
// one that could not establish a live loop exits as an operational failure
// through exitFor, so a coordinator's session start reads it from the code.
func coordinatorSupervision(ctx context.Context, in *invocation) (any, error) {
	if err := in.parseFlags(coordinatorCommand("supervision"), func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	var arm supervisor.Arm
	err := in.callService(ctx, ipc.MethodSupervisionAssert, nil, &arm)
	return arm, err
}

// taskOperand parses a subcommand that names exactly one task as its
// operand, and returns it.
func (in *invocation) taskOperand(name string, declare func(*flag.FlagSet)) (string, error) {
	if err := in.parseArgs(name, 1, declare); err != nil {
		return "", err
	}
	if len(in.args) != 1 || strings.TrimSpace(in.args[0]) == "" {
		return "", usagef("assistant %s names the task it acts on, as assistant tasks --json lists it", name)
	}
	return in.args[0], nil
}

// textOrFile is the text a flag gave, or the contents of the file its -file
// sibling named, resolved against the directory the command was run in. Both
// is incorrect usage, and so is a file that cannot be read; neither is
// passed on, so what the service checks is the text alone.
func (in *invocation) textOrFile(flagName, text, file string) (string, error) {
	switch {
	case text != "" && file != "":
		return "", usagef("--%s and --%s-file were both given; give one", flagName, flagName)
	case file == "":
		return text, nil
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(in.env.WorkingDir, file)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", usagef("--%s-file could not be read: %v", flagName, err)
	}
	return string(raw), nil
}
