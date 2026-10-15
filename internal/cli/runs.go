package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// attachOrStart is the command with no verb: attach to this branch's active
// run, and with no run, start one.
//
// The service decides which of those it is, in one call, because the answer
// depends on records only it should be reading and writing while it holds the
// home. That call blocks while it advances the run and returns at its next
// decision point or a terminal outcome, per PRD section 9. A run the service
// is already advancing is reported as it stands instead, so the answer can be
// a run still executing.
//
// --answer is how a decision is answered without a terminal to answer in. PRD
// section 9's table gives this command no verb of its own for answering, and
// the terminal interface collects the answer by showing the findings and
// taking a selection; an agent driving the same gate needs the same decision
// with the same options, so it supplies the answer as a parameter of the same
// command rather than through a verb the specification does not describe.
//
// PRD section 5's Fix action is the answer "fix", and what the round is to
// resolve rides on the same command as three more flags: --select names
// findings of the held stage's report, --note is free text for the fixer, and
// --finding, which may be repeated, is a finding of the caller's own. They are
// read only with --answer fix and refused with anything else, because an
// instruction given and then ignored is worse than one refused.
//
// --task names a task instead of this branch's run: PRD section 9 has
// answering a hold take a task as well as a run, so an investigation's
// decision and a task held at its relaunch bound are answered with the same
// flag and no verb of their own. A task with no hold of its own open has its
// answer given to the hold on its current run instead, the run --answer
// reaches from a checkout of the task's branch, so a coordinator answers a
// task's run without checking that branch out anywhere. It is read only
// with --answer, and the fix round's flags go with --answer fix to the
// task's run, as they would to this branch's. Neither needs a working copy,
// so the command answers from wherever it is run.
func attachOrStart(ctx context.Context, in *invocation) (any, error) {
	var (
		intent       string
		suppliedFlag bool
		answer       string
		skip         string
		cancel       bool
		selected     string
		note         string
		own          repeated
		task         string
	)
	if err := in.parseFlags("", func(set *flag.FlagSet) {
		set.StringVar(&intent, "intent", "", "what this change sets out to do, in your own terms")
		set.BoolVar(&suppliedFlag, "intent-supplied", false, "treat the intent as authoritative acceptance criteria rather than a hint")
		set.StringVar(&answer, "answer", "", "answer the decision the run is holding on, or with --task the task's")
		set.StringVar(&task, "task", "", "with --answer: the task whose hold is answered, or with none of its own open, the hold on the task's current run")
		set.BoolVar(&cancel, "cancel", false, "end this branch's run, wherever it stands")
		set.StringVar(&skip, "skip", "", "comma-separated stages this one run does not take")
		set.StringVar(&selected, "select", "", "with --answer fix: comma-separated identifiers of findings the held stage reported, for the round to resolve; "+
			"the hold lists only the findings holding it, and "+everyFindingIsIn)
		set.StringVar(&note, "note", "", "with --answer fix: a note for the fixer")
		set.Var(&own, "finding", "with --answer fix: a finding of your own for the round to resolve; may be repeated")
	}); err != nil {
		return nil, err
	}
	answered := flagWasSet(in.flags, "answer")
	fixing := answered && answer == string(pipeline.OutcomeFixRequested)
	for _, name := range []string{"select", "note", "finding"} {
		if flagWasSet(in.flags, name) && !fixing {
			return nil, usagef("--%s says what a fix round resolves, and it is read only with --answer %s",
				name, pipeline.OutcomeFixRequested)
		}
	}
	tasked := flagWasSet(in.flags, "task")
	if tasked && !answered {
		return nil, usagef("--task names the task whose hold --answer closes, and it is read only with --answer")
	}
	if tasked && strings.TrimSpace(task) == "" {
		return nil, usagef("--task was given no task; it takes the identifier of the task whose hold is answered")
	}
	if answered && strings.TrimSpace(answer) == "" {
		return nil, usagef("--answer was given an empty answer; it takes one of the options the decision offered")
	}
	if answered && cancel {
		return nil, usagef("--answer and --cancel are two different things to do with one run; pass one")
	}
	if acting := actingOnTheRunInFlight(answered, cancel); acting != "" {
		for _, name := range []string{"intent", "intent-supplied", "skip"} {
			if flagWasSet(in.flags, name) {
				return nil, usagef("%s acts on the run this branch already has, and --%s is for starting one; pass one", acting, name)
			}
		}
	}
	// An intent given with nothing said about its standing is acceptance
	// criteria, because an intent somebody stated is one somebody stated and
	// defaulting the other way would leave a run holding a contract nobody was
	// held to. A caller who says otherwise is taken at their word rather than
	// overridden: --intent-supplied=false records the intent as offered, which
	// is the record saying a hint was given and not a contract.
	supplied := intent != ""
	if flagWasSet(in.flags, "intent-supplied") {
		supplied = suppliedFlag
	}
	var request *pipeline.FixRequest
	if fixing {
		request = &pipeline.FixRequest{Selected: splitList(selected), Notes: note}
		for _, text := range own {
			request.Findings = append(request.Findings, findings.Finding{
				Severity: findings.SeverityWarning, Action: findings.ActionFix, Description: text,
			})
		}
	}
	if tasked {
		return in.answerGivenTask(ctx, task, answer, request)
	}
	working, err := in.workingCopy(ctx)
	if err != nil {
		return nil, err
	}
	if cancel {
		return in.endRun(ctx, working)
	}
	if answered {
		return in.answerDecision(ctx, working, answer, request)
	}
	in.progressf("starting or attaching to the run for this branch; this blocks while the run advances")
	var run machine.Run
	err = in.callService(ctx, ipc.MethodRunStart, machine.StartRequest{
		Working:        machine.Working{WorkingPath: working},
		Intent:         intent,
		IntentSupplied: supplied,
		Skip:           splitList(skip),
	}, &run)
	return run, err
}

// actingOnTheRunInFlight names the flag that acts on a run this branch already
// has, empty when the command is starting or attaching to one.
//
// It is what tells the flags that start a run apart from the two that act on
// one already running. A caller who writes both has asked for two different
// things at once, and taking one and discarding the other with nothing said is
// the failure this refuses: explicit input is either acted on or refused.
//
// answering is whether --answer was written rather than what it holds, because
// the two are different questions and reading the value for the second answers
// the first wrong: a caller who writes an empty answer has asked to answer a
// decision, and routing that to a start would create a run they did not ask
// for. --cancel is read from its value instead, because for a boolean the
// value is the instruction: writing it false says do not cancel, and honouring
// that is the same rule rather than an exception to it.
func actingOnTheRunInFlight(answering, cancel bool) string {
	switch {
	case cancel:
		return "--cancel"
	case answering:
		return "--answer"
	default:
		return ""
	}
}

// everyFindingIsIn says where a caller reads every finding a held stage
// reported with the identifier --select takes, which the human rendering of a
// hold does not list in full.
const everyFindingIsIn = "every finding with its identifier is under active_run.stages[].report.findings in assistant --json status"

// answerDecision answers the branch's active run. The run is found the same
// way attaching finds it, so a caller does not have to name an identifier it
// was never shown.
//
// request is what a fix answer asks the round to resolve, nil for every other
// answer. Whether it selects anything the held stage reported is the
// service's to check, against the report it holds.
//
// A request the service answers with ipc.ErrInvalidRequest is incorrect usage:
// the service raises it for an answer whose fix request is wrong, such as an
// identifier the held stage's current report does not carry, and for a
// request body it could not read. The refusal keeps the code it crossed the
// wire with and gains where every reported identifier is, so a caller holding
// one from an earlier report can correct the selection.
func (in *invocation) answerDecision(ctx context.Context, working, answer string, request *pipeline.FixRequest) (any, error) {
	active, err := in.activeRun(ctx, working, "answer")
	if err != nil {
		return nil, err
	}
	in.progressf("answering %s with %q; this blocks until the run needs another decision", active, answer)
	var run machine.Run
	err = in.callService(ctx, ipc.MethodRunRespond, machine.RespondRequest{Run: active, Answer: answer, Fix: request}, &run)
	if errors.Is(err, ipc.ErrInvalidRequest) {
		return nil, usageError{err: fmt.Errorf("%w; %s", err, everyFindingIsIn)}
	}
	return run, err
}

// answerGivenTask answers --task: the task's own hold while one is open, and
// otherwise the hold on the task's current run.
//
// Which of the two is read off the task's resolution, whose basis names the
// oldest open hold naming the task whichever tier decided its state, so a
// task that has landed with a hold still open is routed to that hold too. A
// fix round goes to the run without the read, because only a run takes one.
// The read routes and decides nothing: the service refuses an answer to the
// task's run while the task has a hold of its own open, and refuses an
// answer to the task's own hold when none is, so a hold that opened or
// closed after the read is a typed refusal and never the other hold
// answered. The basis counts a hold naming the task and a run as well, and
// the service's own hold is one naming the task and no run; nothing in this
// build registers the first shape, and one that did would be routed to the
// task's own hold and refused there, never answered past.
func (in *invocation) answerGivenTask(ctx context.Context, task, answer string, request *pipeline.FixRequest) (any, error) {
	if request == nil {
		var view machine.Task
		if err := in.callService(ctx, ipc.MethodTaskGet, machine.TaskRequest{Task: task}, &view); err != nil {
			return nil, err
		}
		if view.State.Basis.Hold != "" {
			return in.answerTask(ctx, task, answer)
		}
	}
	in.progressf("answering the decision task %s's run is holding on with %q; this blocks until the run needs another decision", task, answer)
	var run machine.Run
	err := in.callService(ctx, ipc.MethodRunRespond, machine.RespondRequest{Task: task, Answer: answer, Fix: request}, &run)
	return run, err
}

// answerTask answers the oldest open hold the task raised with no run behind
// it. Which hold that was, what the answer did and the task as it then
// stands are in the answer; who resolved it is the service's to derive and
// nothing here says.
func (in *invocation) answerTask(ctx context.Context, task, answer string) (any, error) {
	in.progressf("answering the hold task %s is waiting on with %q", task, answer)
	var answered machine.TaskAnswered
	err := in.callService(ctx, ipc.MethodTaskRespond, machine.TaskRespondRequest{Task: task, Answer: answer}, &answered)
	return answered, err
}

// endRun ends the branch's active run, wherever it stands.
//
// A run waiting at a hold is ended by answering it with the cancel option,
// which is a decision the stage offered. This is the other case: a run that is
// executing, or one parked where no answer applies, which no answer reaches.
// Work is not undone either way; the run stops.
func (in *invocation) endRun(ctx context.Context, working string) (any, error) {
	active, err := in.activeRun(ctx, working, "end")
	if err != nil {
		return nil, err
	}
	in.progressf("ending %s", active)
	var run machine.Run
	err = in.callService(ctx, ipc.MethodRunCancel, machine.CancelRequest{Run: active}, &run)
	return run, err
}

// activeRun is the identifier of the run this branch has in flight. It is
// found the same way attaching finds it, so a caller does not have to name an
// identifier it was never shown.
func (in *invocation) activeRun(ctx context.Context, working, verb string) (string, error) {
	var status machine.Status
	if err := in.callService(ctx, ipc.MethodStatus, machine.StatusRequest{
		Working: machine.Working{WorkingPath: working},
	}, &status); err != nil {
		return "", err
	}
	if status.ActiveRun == nil {
		return "", usagef("this branch has no active run to %s", verb)
	}
	return status.ActiveRun.Record.ID, nil
}

// reportStatus reports repository, gate, service, active run, and local branch
// state.
//
// It is one of the two verbs that answer when the service is down, because
// that is exactly when a person asks. What the service knows comes from the
// service, and a service that could not be reached leaves its own field saying
// so rather than failing the report.
//
// Not reaching the service and being refused by it are different answers. A
// refusal came from a service that is running, so it is reported as a failure
// rather than as an absence; only a socket that could not be dialled is the
// service being down.
func reportStatus(ctx context.Context, in *invocation) (any, error) {
	if err := in.parseFlags("status", func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	working, err := in.workingCopy(ctx)
	if err != nil {
		return nil, err
	}
	var status machine.Status
	if err := in.callService(ctx, ipc.MethodStatus, machine.StatusRequest{
		Working: machine.Working{WorkingPath: working},
	}, &status); err != nil {
		if !errors.Is(err, errNoService) {
			// The service answered, and what it answered was a refusal. It is
			// reported as one: saying the service is not running would be this
			// command asserting the opposite of the one fact it was asked
			// for, and about the only thing that could have told it.
			return nil, err
		}
		return in.localStatus(ctx, working, err), nil
	}
	return status, nil
}

// localStatus is what a status reports when the service did not answer: the
// home, the working copy the command was run in, why the service is not
// there, and the pull guard's finding.
//
// It reports less than a served status rather than reading the records the
// service owns, and it reads the working copy no further than the path,
// because everything else about a status has one owner and this is not it.
// The one record it does read is the in-flight set, through
// internal/supervisor/feed's read-only open, because PRD section 5's pull
// guard is owed by a command whose job includes reporting the service is
// down, and that is this one. The guard judges freshness by the operator's
// own supervision_beacon_interval; an operator file that does not parse
// leaves the guard uncomputed, with the parse error in Detail, rather than
// judged by a default the operator did not set.
func (in *invocation) localStatus(ctx context.Context, working string, cause error) machine.Status {
	status := machine.Status{
		Home:    in.home.Root(),
		Service: machine.Service{Socket: in.home.Socket(), Detail: cause.Error()},
		Branch:  &machine.Branch{WorkingPath: working},
		Detail:  "the service is not running, so the repository, gate, branch, and run state it owns were not read",
	}
	cfg, err := service.GlobalConfig(in.home.ConfigFile())
	if err != nil {
		status.Detail += "; whether work is under way unwatched was not decided, because the configuration did not resolve: " + err.Error()
		return status
	}
	verdict := feed.Guard(ctx, in.home, supervisor.FreshnessFor(cfg.SupervisionBeaconInterval), time.Now())
	supervision := machine.SupervisionOf(verdict)
	status.Supervision = &supervision
	return status
}

// listRuns reports recent runs, newest first, or one run in full when it is
// named. Naming one is an argument rather than a command of its own, because
// PRD section 9's table has one row for runs.
//
// The usage view is --usage on this command, because section 9 says it is a
// flag on runs and adds no verb. It takes its window as --usage=WINDOW, and
// --usage alone is machine.DefaultUsageWindow; usageFlag says why the window
// cannot follow it as a separate word. A window that does not parse is
// refused here as incorrect usage before the service is asked. The view
// reads the whole home's invocation history rather than this working copy's
// runs, because the record names no repository, so it neither names a run nor
// takes --limit, and either given with it is refused rather than ignored.
func listRuns(ctx context.Context, in *invocation) (any, error) {
	var limit int
	var usage usageFlag
	if err := in.parseArgs("runs", 1, func(set *flag.FlagSet) {
		set.IntVar(&limit, "limit", 0, "how many runs to report, newest first; zero means all of them")
		set.Var(&usage, usageFlagName, "report what the agent calls of the last WINDOW cost, written --"+usageFlagName+
			"=WINDOW as 24h, 90m or 7d; alone it is "+machine.DefaultUsageWindow)
	}); err != nil {
		return nil, err
	}
	if usage.set {
		return readUsage(ctx, in, usage, limit)
	}
	if len(in.args) == 1 {
		var run machine.Run
		err := in.callService(ctx, ipc.MethodRunGet, machine.RunRequest{Run: in.args[0]}, &run)
		return run, err
	}
	working, err := in.workingCopy(ctx)
	if err != nil {
		return nil, err
	}
	var runs machine.Runs
	err = in.callService(ctx, ipc.MethodRunsList, machine.RunsRequest{
		Working: machine.Working{WorkingPath: working},
		Limit:   limit,
	}, &runs)
	return runs, err
}

// readUsage is runs --usage: the usage view over the window the flag named.
func readUsage(ctx context.Context, in *invocation, usage usageFlag, limit int) (any, error) {
	if len(in.args) > 0 {
		if _, err := machine.ParseUsageWindow(in.args[0]); err == nil {
			return nil, usagef("%s takes its window joined to it, as --%s=%s", "--"+usageFlagName, usageFlagName, in.args[0])
		}
		return nil, usagef("--%s reads every run's agent calls and names no run, and was given %s", usageFlagName, in.args[0])
	}
	if limit != 0 {
		return nil, usagef("--limit counts runs, and --%s reports agent calls rather than runs", usageFlagName)
	}
	if _, err := machine.ParseUsageWindow(usage.window); err != nil {
		return nil, usageError{err: err}
	}
	var answer machine.Usage
	err := in.callService(ctx, ipc.MethodUsageRead, machine.UsageRequest{Window: usage.window}, &answer)
	return answer, err
}

// usageFlagName is the flag on runs that asks for the usage view, named once
// so the declaration, the refusals and a test read the same word.
const usageFlagName = "usage"

// usageFlag is --usage: whether it was given, and the window it was given.
//
// It is a boolean flag to the flag package, so --usage alone is accepted and
// means the default window, and the window is written joined to it as
// --usage=7d. That is the price of the default: a flag the flag package reads
// as taking a value would swallow the word after it, and a flag it reads as
// boolean cannot take one as a separate word. readUsage refuses a window given
// as a separate word by name rather than reading it as a run.
type usageFlag struct {
	set    bool
	window string
}

// String is the window as given, for the flag package's usage text.
func (u *usageFlag) String() string { return u.window }

// Set records the flag. The flag package hands a boolean flag given alone the
// value "true", which is the default window and not a window called true.
func (u *usageFlag) Set(value string) error {
	u.set = true
	if value == "true" {
		value = ""
	}
	u.window = value
	return nil
}

// IsBoolFlag tells the flag package --usage may be given alone.
func (u *usageFlag) IsBoolFlag() bool { return true }

// rerun starts a fresh run of the branch this working copy is standing on,
// from that branch's last known head, inheriting the intent recorded there,
// and blocks while it advances that run, answering at its next decision point
// or a terminal outcome. Attaching's exception is not one a rerun reaches: a
// branch whose run is still in flight is refused rather than reported, so this
// never answers with a run still executing.
//
// The branch is the working copy's, the same way attaching and status read it,
// so a caller never restarts a branch they are not on.
//
// The last known head is the commit the branch's last run pushed, or the one
// it was submitted with when it pushed nothing, and the service refuses the
// rerun when the working copy's HEAD is another commit, with
// machine.RerunMoved naming both and a remedy: assistant sync when the local
// commit is established as behind work the run pushed, and otherwise
// the two commands that validate the local one. That refusal is the
// service's, decided while it holds the branch; this command only reports it,
// and neither reads anything but HEAD from the working copy, so a refused
// rerun changes nothing there.
func rerun(ctx context.Context, in *invocation) (any, error) {
	if err := in.parseFlags("rerun", func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	working, err := in.workingCopy(ctx)
	if err != nil {
		return nil, err
	}
	in.progressf("starting a fresh run of this branch from its last known head; this blocks until it needs a decision")
	var run machine.Run
	err = in.callService(ctx, ipc.MethodRunRerun, machine.RerunRequest{
		Working: machine.Working{WorkingPath: working},
	}, &run)
	return run, err
}

// listTasks reports fleet work with its resolved current state, or one task
// when it is named, on the same terms as runs.
func listTasks(ctx context.Context, in *invocation) (any, error) {
	if err := in.parseArgs("tasks", 1, func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	if len(in.args) == 1 {
		var task machine.Task
		err := in.callService(ctx, ipc.MethodTaskGet, machine.TaskRequest{Task: in.args[0]}, &task)
		return task, err
	}
	var tasks machine.Tasks
	err := in.callService(ctx, ipc.MethodTasksList, nil, &tasks)
	return tasks, err
}

// repeated is a flag that may be given more than once, each value kept in the
// order it was given.
type repeated []string

// String renders the values for the flag package's usage text.
func (r *repeated) String() string { return strings.Join(*r, "; ") }

// Set adds one value.
func (r *repeated) Set(value string) error {
	*r = append(*r, value)
	return nil
}

// splitList reads a comma-separated flag value, dropping empty entries so that
// an empty flag is no entries rather than one empty one.
func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
