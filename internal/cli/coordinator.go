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
	"time"

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
// The refusal is only as good as that placement, which internal/service
// owns: it walks the caller's parent chain to the first process in a worker
// group or that is itself a worker, so a worker's descendant in a group of
// its own is still placed as the worker's, and a chain it cannot read is
// refused rather than served. The gap this command cannot close is the one
// internal/service's placement.go names: a chain the kernel rewrote when an
// intermediate process exited places a worker's command as the
// coordinator's, and that call is served. What a worker may run at all is
// its grant's, which allows git without asking and denies nothing else.
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
	{"peek", "<task>", "Read a task's worker's recent output, redacted and bounded, and how to attach to its terminal.", coordinatorPeek},
	{"relaunch", "<task>", "Begin a new attempt of a task whose worker is gone, in its own copy.", coordinatorRelaunch},
	{"promote", "<task>", "Turn an investigation task into a delivery on a branch, in its own copy.", coordinatorPromote},
	{"relayed", "<task>", "Record that a reported investigation's findings were relayed, which finishes it.", coordinatorRelayed},
	{"wakes", "", "With --json, drain the queued wakes, oldest first: hand them out, closing none; without it, list them and take none.", coordinatorWakes},
	{"await", "", "Block until a wake is on offer or the bound elapses, then drain the queued wakes.", coordinatorAwait},
	{"listen", "", "Hold the wake stream: print each wake as it is queued, until interrupted, handing out none.", coordinatorListen},
	{"acknowledge", "<wake>...", "Record that the named wakes were acted on, which closes them, so no drain hands them out again.", coordinatorAcknowledge},
	{"supervision", "", "Verify that this home's service is watching the fleet.", coordinatorSupervision},
	{"delegate", "<domain>", "Delegate a named domain of tasks to a coordinator of its own, begun by the service under the posture given.", coordinatorDelegate},
	{"end-delegation", "<domain>", "End a domain's delegation, so its notices come back to you and its coordinator is ended.", coordinatorEndDelegation},
}

// coordinatorRoute is the route the service gives a domain's subordinate
// coordinator: this program's absolute path, --home naming root, and this
// verb, composed as workerRoute composes the worker's. It is the one
// composition of that text; cli's service start passes it to
// internal/service, which hands it to the grant and the subordinate's
// instructions alike, so the commands a subordinate is told it may run and
// the prefix its grant allows are one text. What the service then admits
// of each subcommand is decided there from the caller's placement.
func coordinatorRoute(executable, root string) string {
	return shellQuote(executable) + " --" + homeFlagName + " " + shellQuote(root) + " " + coordinatorVerbName
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
		set.StringVar(&request.Role, "role", "", "the configured role the worker runs under; empty means worker")
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

// coordinatorPeek is task.peek: the recent output of the worker of the
// task's latest attempt, redacted and bounded by the service, and the
// command a person attaches to its terminal with where the service's
// session provider hosts it in one. The output is in the structured answer
// only, beside the command; what a person reads is PeekPhrase, because a
// worker's output is never relayed to a person (P13) and the command names
// a path under the home.
func coordinatorPeek(ctx context.Context, in *invocation) (any, error) {
	task, err := in.taskOperand(coordinatorCommand("peek"), func(*flag.FlagSet) {})
	if err != nil {
		return nil, err
	}
	var peeked machine.TaskPeek
	err = in.callService(ctx, ipc.MethodTaskPeek, machine.TaskRequest{Task: task}, &peeked)
	return peeked, err
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

// coordinatorRelayed is task.relay: the coordinator's record that it relayed
// the findings of the investigation named, PRD section 6's Reported to
// Complete edge. Whether the task has findings to relay, an investigation
// that reads Reported from its stored report, is the service's to decide,
// and a task that does not is its refusal.
func coordinatorRelayed(ctx context.Context, in *invocation) (any, error) {
	task, err := in.taskOperand(coordinatorCommand("relayed"), func(*flag.FlagSet) {})
	if err != nil {
		return nil, err
	}
	var relayed machine.TaskRelayed
	err = in.callService(ctx, ipc.MethodTaskRelay, machine.TaskRequest{Task: task}, &relayed)
	return relayed, err
}

// coordinatorWakes is two readings of the queue, chosen by the rendering
// the invocation asked for. With --json it is wakes.list, PRD section 8's
// drain, which is the coordinator's: the rows it lists are handed out to the
// coordinator and stay queued until coordinatorAcknowledge closes them, and
// a row not acknowledged is handed out again once the coordinator it was
// handed to is no longer attached. Without --json it is wakes.peek, a
// person's listing, which hands out and acknowledges nothing, so a person
// who runs it to look takes nothing from the coordinator.
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
	if !in.json {
		var queued machine.QueuedWakes
		err = in.callService(ctx, ipc.MethodWakesPeek, request, &queued)
		return queued, err
	}
	var wakes machine.Wakes
	err = in.callService(ctx, ipc.MethodWakesList, request, &wakes)
	return wakes, err
}

// coordinatorAwait is wakes.wait, named await rather than wait because a
// subcommand name reaches a person in the refusal that lists the
// subcommands, and internal/machine/vocabtest's Forbidden set, which
// internal/cli's P13 tests hold that text to, derives a term from every row
// of internal/supervisor's wake-kind table, its kind and its class:
// supervisor.ClassWait is the bare word "wait".
//
// It is one call that blocks until the queue holds a wake on offer or the
// bound elapses, answered as coordinatorWakes' --json answer is and draining
// as it does, so an empty list is the bound elapsing, and a row it lists
// stays queued until it is acknowledged. It drains in either rendering: a
// wait is the coordinator's, and a person reads the queue with
// coordinatorWakes.
//
// --timeout is a duration in whole seconds, and its range and default are
// machine.WakeWaitRequest.Bounds', which is checked here before the service
// is asked and again by the service: a bound outside the range, a fraction
// of a second, and a negative limit are incorrect usage, never clamped. A
// wait the service ends before its bound, because the service stopped, is
// an operational failure, and arming it again is the caller's to do.
func coordinatorAwait(ctx context.Context, in *invocation) (any, error) {
	name := coordinatorCommand("await")
	var request machine.WakeWaitRequest
	var timeout time.Duration
	err := in.parseFlags(name, func(set *flag.FlagSet) {
		set.DurationVar(&timeout, "timeout", 0, fmt.Sprintf("how long to block, in whole seconds from %s to %s; 0 is %s",
			machine.MinWakeWaitTimeout, machine.MaxWakeWaitTimeout, machine.DefaultWakeWaitTimeout))
		set.IntVar(&request.Limit, "limit", 0, fmt.Sprintf("at most this many wakes, oldest first; 0 is %d", machine.DefaultWakesLimit))
	})
	if err != nil {
		return nil, err
	}
	if timeout%time.Second != 0 {
		return nil, usagef("assistant %s --timeout is %s, and the bound is a whole number of seconds", name, timeout)
	}
	request.TimeoutSeconds = int(timeout / time.Second)
	if _, _, err := request.Bounds(); err != nil {
		return nil, usagef("assistant %s: %v", name, err)
	}
	var wakes machine.Wakes
	err = in.callService(ctx, ipc.MethodWakesWait, request, &wakes)
	return wakes, err
}

// coordinatorAcknowledge is wakes.acknowledge, naming the wakes as
// operands: the coordinator saying it acted on them, which is what closes a
// row, one a drain handed it or one coordinatorListen printed. Who
// acknowledged is derived by the service from the surface and is not
// something this sends. A batch naming a wake no row has, or one already
// acknowledged, is the store's refusal of the whole batch, answered as
// ipc.ErrInvalidRequest.
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
		// A wake about a domain is numbered apart from a task's and is
		// named with the prefix the --json listing shows it under, so the
		// two cannot be confused by number.
		text, domain := strings.CutPrefix(arg, domainWakePrefix)
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, usagef("assistant %s takes wake identifiers, as a number or %sNUMBER for a notice about a domain, and %q is not one",
				name, domainWakePrefix, printable(arg))
		}
		if domain {
			request.DomainWakes = append(request.DomainWakes, id)
			continue
		}
		request.Wakes = append(request.Wakes, id)
	}
	var acknowledged machine.Acknowledged
	err := in.callService(ctx, ipc.MethodWakesAcknowledge, request, &acknowledged)
	return acknowledged, err
}

// domainWakePrefix is what a wake about a domain is named with on the
// acknowledge command line, before its number: the queue's second shape
// numbers its rows apart from the task wakes, so a bare number is always a
// task wake and a prefixed one always a domain wake.
const domainWakePrefix = "d"

// coordinatorListen is wakes.subscribe, the coordinator's wake stream: the
// service pushes each wake as the supervisor queues it, PRD section 5's
// "then wake the coordinator", and this prints each one as it arrives, in
// the rendering the invocation asked for, until it is interrupted. It is
// for a coordinator whose host can run a command in the background and
// start a turn on what it prints; a coordinator that cannot uses await.
//
// It hands out and records nothing, because a push is not a drain: a wake it
// prints stays queued until coordinatorAcknowledge names it. Holding it is
// what the service reads as the coordinator being attached, so a row a
// drain hands out while it runs is not handed out again until it ends. The
// stream opens with a gap, and a gap later on means wakes were missed;
// either is the coordinator's signal to drain, which gives back everything
// still queued that it was not already handed.
//
// An interrupt ends it with success. The stream ending any other way, the
// service stopping under it included, is an operational failure, so a host
// watching it sees that it has to be armed again rather than a quiet exit.
func coordinatorListen(ctx context.Context, in *invocation) (any, error) {
	if err := in.parseFlags(coordinatorCommand("listen"), func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	client, err := in.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	stream, err := client.SubscribeTo(ctx, ipc.MethodWakesSubscribe, nil)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	in.progressf("listening for notices about the fleet; interrupt to stop")
	for {
		event, err := stream.Recv(ctx)
		switch {
		case err == nil:
			in.report(event)
		case ctx.Err() != nil:
			return nil, nil
		case errors.Is(err, ipc.ErrStreamClosed):
			return nil, fmt.Errorf("%w: the service ended the stream of notices; run assistant %s again once it is back",
				ipc.ErrUnavailable, coordinatorCommand("listen"))
		default:
			return nil, err
		}
	}
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

// coordinatorDelegate is domain.delegate: the domain named as the operand,
// the posture its tasks take, and what its coordinator is started with,
// from --instructions or --instructions-file on coordinatorCreate's terms.
// Which names and postures the service accepts is its own; a request it
// cannot read is incorrect usage, and a domain already delegated is its
// refusal. The answer comes once the delegation is recorded and before the
// domain's coordinator exists.
func coordinatorDelegate(ctx context.Context, in *invocation) (any, error) {
	var request machine.DomainDelegateRequest
	var instructions, instructionsFile string
	domain, err := in.taskOperand(coordinatorCommand("delegate"), func(set *flag.FlagSet) {
		set.StringVar(&request.Delivery, "delivery", "", `"gated" or "local": the posture every task of the domain takes`)
		set.StringVar(&instructions, "instructions", "", "what the domain's coordinator is started with")
		set.StringVar(&instructionsFile, "instructions-file", "", "a file holding what the domain's coordinator is started with, in place of --instructions")
	})
	if err != nil {
		return nil, err
	}
	request.Domain = domain
	if request.Instructions, err = in.textOrFile("instructions", instructions, instructionsFile); err != nil {
		return nil, err
	}
	var delegated machine.Delegated
	err = in.callService(ctx, ipc.MethodDomainDelegate, request, &delegated)
	return delegated, err
}

// coordinatorEndDelegation is domain.end: the domain named as the operand.
// A domain with no standing delegation is the service's refusal.
func coordinatorEndDelegation(ctx context.Context, in *invocation) (any, error) {
	domain, err := in.taskOperand(coordinatorCommand("end-delegation"), func(*flag.FlagSet) {})
	if err != nil {
		return nil, err
	}
	var ended machine.DelegationEnded
	err = in.callService(ctx, ipc.MethodDomainEnd, machine.DomainEndRequest{Domain: domain}, &ended)
	return ended, err
}

// taskOperand parses a subcommand that names exactly one task as its
// operand, and returns it. A domain is parsed on the same terms, since both
// are one word the service decides the meaning of.
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
