package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
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
// --finding, which may be repeated, is a finding of the caller's own.
// --select and --finding are read only with --answer fix and refused with
// anything else, because an instruction given and then ignored is worse than
// one refused. --note is also the free text recorded with a task hold's
// resolution (issue #279); with a run's answer that is not a fix round it is
// refused, because a run's RespondRequest carries no resolution note.
//
// --task names a task instead of this branch's run: PRD section 9 has
// answering a hold take a task as well as a run, so an investigation's
// decision and a task held at its relaunch bound are answered with the same
// flag and no verb of their own. A task with no hold of its own open has its
// answer given to the hold on its current run instead, the run --answer
// reaches from a checkout of the task's branch, so a coordinator answers a
// task's run without checking that branch out anywhere. --hold names which
// of the task's own holds to close (full key, or a finding id under the
// task); without it the only open one is answered, and more than one open
// is refused and listed. --run with --task
// answers the task's current run even while the task still has holds of its
// own open, so a promoted delivery run is reachable without first clearing
// every report hold. It is read only with --answer or --standing-consent,
// and the fix round's flags go with --answer fix to the task's run, as they
// would to this branch's. With --standing-consent it puts the task's current
// run under standing consent and attaches to it, the way the flag does for
// this branch's run, and the flags that start a run are refused beside it.
// None of these needs a working copy, so the command answers from wherever
// it is run.
//
// --cancel ends this branch's run, and given a run as --cancel=RUN it ends
// that run instead, which PRD section 9 has it do from anywhere: like --task,
// it reads no working copy, so it is answered from a directory that is not
// one. cancelFlag says why the run is joined to the flag rather than the word
// after it, and a run written as that word is refused by name.
func attachOrStart(ctx context.Context, in *invocation) (any, error) {
	var (
		intent       string
		suppliedFlag bool
		answer       string
		skip         string
		cancel       cancelFlag
		selected     string
		note         string
		own          repeated
		task         string
		hold         string
		runFlag      runTarget
		confirm      string
		domain       string
		consent      bool
	)
	if err := in.parseArgs("", 1, func(set *flag.FlagSet) {
		set.StringVar(&intent, "intent", "", "what this change sets out to do, in your own terms")
		set.BoolVar(&suppliedFlag, "intent-supplied", false, "treat the intent as authoritative acceptance criteria rather than a hint")
		set.StringVar(&answer, "answer", "", "answer the decision the run is holding on, or with --task the task's")
		set.StringVar(&task, "task", "", "with --answer: the task whose hold is answered, or with none of its own open, the hold on the task's current run; "+
			"with --standing-consent: the task whose current run is put under standing consent")
		set.StringVar(&domain, "domain", "", "with --answer: the delegated domain whose hold is answered, the one raised once its "+
			"coordinator was begun again as many times as the program begins it on its own; retry begins it once more and stop ends the delegation")
		set.StringVar(&hold, "hold", "", "with --task --answer: which of the task's own holds to close "+
			"(the hold's key, or a finding id under that task); without it the only open one is answered, and more than one open is refused; "+
			"a kept work entry's key, kept-work/N, answers that entry, with --task or --run=RUN naming whose copy it is")
		set.StringVar(&confirm, "confirm", "", "with --answer discard and --hold kept-work/N: the digest a discard shown beforehand carried; "+
			"without it the discard shows what would be lost and removes nothing; with it the answer is accepted only at a terminal, "+
			"in the foreground and without --json, and is then refused: removal on a confirmed discard is not in this build")
		set.Var(&runFlag, "run", "with --task --answer: answer the task's current run even while "+
			"the task still has holds of its own open; written --run=RUN with --answer and --hold kept-work/N, the run whose kept copy is answered")
		set.Var(&cancel, cancelFlagName, "end this branch's run, wherever it stands; written --"+cancelFlagName+
			"=RUN it ends the run RUN instead, from anywhere")
		set.StringVar(&skip, "skip", "", "comma-separated stages this one run does not take")
		set.StringVar(&selected, "select", "", "with --answer fix: comma-separated identifiers of findings the held stage reported, for the round to resolve; "+
			"the hold lists only the findings holding it, and "+everyFindingIsIn)
		set.StringVar(&note, "note", "", "with --answer fix: a note for the fixer; with --task answering a task hold: "+
			"free text recorded with the resolution about what was decided")
		set.Var(&own, "finding", "with --answer fix: a finding of your own for the round to resolve; may be repeated")
		set.BoolVar(&consent, "standing-consent", false, "put the run this starts or attaches to under standing consent, "+
			"where the repository's default branch sets "+string(config.KeyAllowStandingConsent)+": the program then answers "+
			"the decisions about findings before the push itself, and never the push, pull request or checks ones")
	}); err != nil {
		return nil, err
	}
	// parseArgs let one word through so a run written after --cancel can be
	// refused by name; the command still takes no argument of its own.
	if len(in.args) > 0 {
		if cancel.on && cancel.run == "" {
			return nil, usagef("--%s takes its run joined to it, as --%s=%s", cancelFlagName, cancelFlagName, in.args[0])
		}
		return nil, usagef("%s takes %s, and was given %d: %s",
			commandName(""), arguments(0), len(in.args), strings.Join(in.args, " "))
	}
	if cancel.on && cancel.named && strings.TrimSpace(cancel.run) == "" {
		return nil, usagef("--%s= was given no run; it takes the identifier of the run to end, or nothing to end this branch's", cancelFlagName)
	}
	answered := flagWasSet(in.flags, "answer")
	fixing := answered && answer == string(pipeline.OutcomeFixRequested)
	for _, name := range []string{"select", "finding"} {
		if flagWasSet(in.flags, name) && !fixing {
			return nil, usagef("--%s says what a fix round resolves, and it is read only with --answer %s",
				name, pipeline.OutcomeFixRequested)
		}
	}
	tasked := flagWasSet(in.flags, "task")
	holding := flagWasSet(in.flags, "hold")
	targetRun := runFlag.on && !runFlag.named
	running := flagWasSet(in.flags, "run") && !runFlag.named
	domained := flagWasSet(in.flags, "domain")
	if _, kept := store.ParseKeptWorkKey(hold); kept || runFlag.named || flagWasSet(in.flags, "confirm") {
		return in.keptWorkAnswer(ctx, keptWorkFlags{
			answered: answered, answer: answer, hold: hold, kept: kept, task: task, tasked: tasked,
			run: runFlag, runSet: flagWasSet(in.flags, "run"), fixing: fixing, others: in.flagsBeside(),
			confirm: confirm, confirmSet: flagWasSet(in.flags, "confirm"),
		})
	}
	switch {
	case domained && strings.TrimSpace(domain) == "":
		return nil, usagef("--domain was given no domain; it takes the name of the delegated domain whose hold is answered")
	case domained && !answered:
		return nil, usagef("--domain names the domain whose hold --answer closes, and it is read only with --answer")
	case domained && (tasked || holding || running || consent):
		return nil, usagef("--domain answers a domain's hold on its own; pass it without --task, --hold, --run or --standing-consent")
	case domained && cancel.on:
		return nil, usagef("--domain and --cancel are two different things to do; pass one")
	}
	if running && !targetRun {
		// Writing --run=false is "do not target the run"; honour that rather
		// than treating the flag's presence as the instruction.
		running = false
	}
	if holding && strings.TrimSpace(hold) == "" {
		return nil, usagef("--hold was given no hold; it takes the hold's key or a finding id under the task")
	}
	if (holding || running) && !tasked {
		which := "--hold"
		if running && !holding {
			which = "--run"
		}
		return nil, usagef("%s is read only with --task", which)
	}
	if holding && running {
		return nil, usagef("--hold closes a task's own hold and --run answers its run; pass one")
	}
	if holding && fixing {
		return nil, usagef("--hold closes a task's own hold, and --answer %s is a fix round on its run; pass one",
			pipeline.OutcomeFixRequested)
	}
	if (holding || running) && !answered {
		return nil, usagef("--hold and --run name what --answer closes, and they are read only with --answer")
	}
	if flagWasSet(in.flags, "note") && !fixing {
		// A resolution note rides on a task's or a domain's hold. A run's
		// answer has no field for one, and --run makes the answer the
		// run's even while the task still holds, so a note beside it would
		// be ignored.
		if (!tasked && !domained) || running {
			return nil, usagef("--note records what was decided on a task's hold, or with --answer %s a note for the fixer",
				pipeline.OutcomeFixRequested)
		}
	}
	if tasked && !answered && !consent {
		return nil, usagef("--task names the task whose hold --answer closes, or whose run --standing-consent is given to, " +
			"and it is read only with one of the two")
	}
	if tasked && strings.TrimSpace(task) == "" {
		return nil, usagef("--task was given no task; it takes the identifier of the task whose hold is answered " +
			"or whose run is put under standing consent")
	}
	if answered && strings.TrimSpace(answer) == "" {
		return nil, usagef("--answer was given an empty answer; it takes one of the options the decision offered")
	}
	if answered && cancel.on {
		return nil, usagef("--answer and --cancel are two different things to do with one run; pass one")
	}
	if acting := actingOnTheRunInFlight(answered, cancel.on); acting != "" {
		for _, name := range []string{"intent", "intent-supplied", "skip", "standing-consent"} {
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
	if tasked && !answered {
		for _, name := range []string{"intent", "intent-supplied", "skip"} {
			if flagWasSet(in.flags, name) {
				return nil, usagef("--task with --standing-consent attaches to the task's run, and --%s is for starting one; pass one", name)
			}
		}
		return in.consentGivenTask(ctx, task)
	}
	if domained {
		if fixing {
			return nil, usagef("--domain answers a domain's hold, which offers retry or stop and never a fix round")
		}
		return in.answerDomain(ctx, domain, answer, note)
	}
	if tasked {
		return in.answerGivenTask(ctx, task, answer, hold, note, targetRun, request)
	}
	if cancel.on && cancel.run != "" {
		return in.endNamedRun(ctx, cancel.run)
	}
	working, err := in.workingCopy(ctx)
	if err != nil {
		return nil, err
	}
	if cancel.on {
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
		// Whether the repository allows it is the service's to read, from
		// the trusted copy; a refusal comes back as an invalid request and
		// exits as incorrect usage.
		StandingConsent: consent,
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
// A request the service answers with ipc.ErrInvalidRequest is incorrect usage,
// which render decides for every method alike: the service raises it here for
// an answer whose fix request is wrong, such as an identifier the held stage's
// current report does not carry, and for a request body it could not read.
// What this adds is where every reported identifier is, so a caller holding
// one from an earlier report can correct the selection; the refusal keeps the
// code it crossed the wire with.
func (in *invocation) answerDecision(ctx context.Context, working, answer string, request *pipeline.FixRequest) (any, error) {
	active, err := in.activeRun(ctx, working, "answer")
	if err != nil {
		return nil, err
	}
	in.progressf("answering %s with %q; this blocks until the run needs another decision", active, answer)
	var run machine.Run
	err = in.callService(ctx, ipc.MethodRunRespond, machine.RespondRequest{Run: active, Answer: answer, Fix: request}, &run)
	if errors.Is(err, ipc.ErrInvalidRequest) {
		return nil, fmt.Errorf("%w; %s", err, everyFindingIsIn)
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
// --run skips that read and answers the run while the task's own holds stay
// open. --hold names the task hold to close and skips the run path. The
// read routes and decides nothing: the service refuses an answer to the
// task's run while the task has a hold of its own open (unless TargetRun),
// and refuses an answer to the task's own hold when none is, so a hold that
// opened or closed after the read is a typed refusal and never the other
// hold answered. The basis counts a hold naming the task and a run as well,
// and the service's own hold is one naming the task and no run; nothing in
// this build registers the first shape, and one that did would be routed to
// the task's own hold and refused there, never answered past.
func (in *invocation) answerGivenTask(ctx context.Context, task, answer, hold, note string, targetRun bool, request *pipeline.FixRequest) (any, error) {
	if hold != "" {
		return in.answerTask(ctx, task, answer, hold, note)
	}
	if request == nil && !targetRun {
		var view machine.Task
		if err := in.callService(ctx, ipc.MethodTaskGet, machine.TaskRequest{Task: task}, &view); err != nil {
			return nil, err
		}
		if view.State.Basis.Hold != "" {
			return in.answerTask(ctx, task, answer, "", note)
		}
		if note != "" {
			return nil, usagef("--note records what was decided on a task's hold, or with --answer %s a note for the fixer; "+
				"task %s has no hold of its own open, so this answer is its run's", pipeline.OutcomeFixRequested, task)
		}
	}
	in.progressf("answering the decision task %s's run is holding on with %q; this blocks until the run needs another decision", task, answer)
	var run machine.Run
	err := in.callService(ctx, ipc.MethodRunRespond, machine.RespondRequest{
		Task: task, Answer: answer, Fix: request, TargetRun: targetRun,
	}, &run)
	return run, err
}

// consentGivenTask puts the task's current run under standing consent and
// attaches to it, the run --standing-consent reaches from a checkout of the
// task's branch, so a coordinator gives consent to a task's run without
// checking that branch out anywhere. Which run that is, and every refusal, are
// the service's, on the terms an answer given the task meets, except that the
// run need not be held yet. It reads no working copy, so it is answered from
// wherever it is run.
func (in *invocation) consentGivenTask(ctx context.Context, task string) (any, error) {
	in.progressf("putting task %s's run under standing consent; this blocks while the run advances", task)
	var run machine.Run
	err := in.callService(ctx, ipc.MethodRunStart, machine.StartRequest{Task: task, StandingConsent: true}, &run)
	return run, err
}

// answerTask answers an open hold the task raised with no run behind it.
// Without hold, it is the oldest; with hold set, that hold is named. note is
// free text recorded with the resolution. Which hold that was, what the
// answer did and the task as it then stands are in the answer; who resolved
// it is the service's to derive and nothing here says.
func (in *invocation) answerTask(ctx context.Context, task, answer, hold, note string) (any, error) {
	if hold != "" {
		in.progressf("answering hold %s of task %s with %q", hold, task, answer)
	} else {
		in.progressf("answering the hold task %s is waiting on with %q", task, answer)
	}
	var answered machine.TaskAnswered
	err := in.callService(ctx, ipc.MethodTaskRespond, machine.TaskRespondRequest{
		Task: task, Hold: hold, Answer: answer, Note: note,
	}, &answered)
	return answered, err
}

// answerDomain answers the hold a delegated domain raised once its
// coordinator was begun again as many times as the program begins one on its
// own, through task.respond given the domain: retry begins the coordinator
// once more and stop ends the delegation, which the service decides and
// records; a domain holding on nothing is its refusal.
func (in *invocation) answerDomain(ctx context.Context, domain, answer, note string) (any, error) {
	in.progressf("answering the hold domain %s is waiting on with %q", domain, answer)
	var answered machine.TaskAnswered
	err := in.callService(ctx, ipc.MethodTaskRespond, machine.TaskRespondRequest{
		Domain: domain, Answer: answer, Note: note,
	}, &answered)
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
	return in.endNamedRun(ctx, active)
}

// endNamedRun ends the run named, wherever it stands and wherever the command
// was run. A run the home has no record of, and one that has already ended,
// are the service's to refuse, with machine.CancelRefused as an invalid
// request, so they exit as incorrect usage through failureExit like any other
// request the service will not read as given.
func (in *invocation) endNamedRun(ctx context.Context, id string) (any, error) {
	in.progressf("ending %s", id)
	var run machine.Run
	err := in.callService(ctx, ipc.MethodRunCancel, machine.CancelRequest{Run: id}, &run)
	return run, err
}

// cancelFlagName is the flag on the command with no verb that ends a run,
// named once so the declaration, the refusals and a test read the same word.
const cancelFlagName = "cancel"

// cancelFlag is --cancel: whether it asks for a run to be ended, and which run
// when one was named.
//
// It is a boolean flag to the flag package, for the reason usageFlag is one:
// --cancel alone has to stay the command that ends this branch's run, so the
// run is written joined to it as --cancel=RUN. A boolean's own words keep
// their meaning, so --cancel=true is --cancel alone and --cancel=false asks
// for nothing to be ended, which actingOnTheRunInFlight relies on. A run
// identifier is never either word, because the service mints identifiers as
// hexadecimal.
type cancelFlag struct {
	// on is whether a run is to be ended.
	on bool
	// named is whether the flag was written with a value other than a
	// boolean's, which is a run even when it is empty.
	named bool
	// run is the run named, empty for this branch's.
	run string
}

// String is the run as given, for the flag package's usage text.
func (c *cancelFlag) String() string { return c.run }

// Set records the flag.
func (c *cancelFlag) Set(value string) error {
	switch value {
	case "true":
		c.on, c.named, c.run = true, false, ""
	case "false":
		c.on, c.named, c.run = false, false, ""
	default:
		c.on, c.named, c.run = true, true, value
	}
	return nil
}

// IsBoolFlag tells the flag package --cancel may be given alone.
func (c *cancelFlag) IsBoolFlag() bool { return true }

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
//
// The log view is --log on this command with a run named, on the same terms:
// PRD section 9 makes it a flag on runs and adds no verb. It names the stage whose log is read, and a
// fix round is in the log of the stage whose findings it fixed. readLog says
// what it refuses.
func listRuns(ctx context.Context, in *invocation) (any, error) {
	var limit int
	var usage usageFlag
	var stage string
	if err := in.parseArgs("runs", 1, func(set *flag.FlagSet) {
		set.IntVar(&limit, "limit", 0, "how many runs to report, newest first; zero means all of them")
		set.Var(&usage, usageFlagName, "report what the agent calls of the last WINDOW cost, written --"+usageFlagName+
			"=WINDOW as 24h, 90m or 7d; alone it is "+machine.DefaultUsageWindow)
		set.StringVar(&stage, logFlagName, "", "with a run named: print the end of that run's log for the stage STAGE, "+
			"which holds its fix rounds too; one of "+strings.Join(stageNames(), ", "))
	}); err != nil {
		return nil, err
	}
	if flagWasSet(in.flags, logFlagName) {
		return readLog(ctx, in, stage, limit, usage.set)
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

// readLog is runs RUN --log=STAGE: the end of one stage's log in the run
// named. It names one run and reads one file, so a run must be named, the
// stage must be one of the nine, and --limit and --usage are refused beside
// it rather than ignored; each refusal is incorrect usage, decided here before
// the service is asked. Which part of the log is returned, and how much is
// left out, is the service's to bound and machine.StageLog's to say.
func readLog(ctx context.Context, in *invocation, stage string, limit int, usage bool) (any, error) {
	if len(in.args) != 1 {
		return nil, usagef("--%s reads one run's log and takes the run as its argument, as assistant runs RUN --%s=%s",
			logFlagName, logFlagName, pipeline.StageReview)
	}
	if _, ok := pipeline.ParseStage(stage); !ok {
		return nil, usagef("--%s takes one of the stages: %s; it was given %q", logFlagName, strings.Join(stageNames(), ", "), stage)
	}
	if limit != 0 {
		return nil, usagef("--limit counts runs, and --%s reads one run's log", logFlagName)
	}
	if usage {
		return nil, usagef("--%s and --%s are two different reads; pass one", logFlagName, usageFlagName)
	}
	var answer machine.StageLog
	err := in.callService(ctx, ipc.MethodRunLog, machine.StageLogRequest{Run: in.args[0], Stage: stage}, &answer)
	return answer, err
}

// logFlagName is the flag on runs that asks for the log view, named once so
// the declaration, the refusals and a test read the same word.
const logFlagName = "log"

// stageNames is the nine stage names in the order a run takes them, for a
// flag's help and a refusal that says what was expected.
func stageNames() []string {
	var names []string
	for _, stage := range pipeline.Order() {
		names = append(names, stage.String())
	}
	return names
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
//
// --intent replaces the inherited intent for the fresh run, which records it
// as supplied: acceptance criteria, as an intent given to a start with nothing
// said about its standing is. It changes nothing else about the rerun, so the
// head validated and the refusal above are the same with it. An intent that is
// empty or whitespace is refused as incorrect usage here before the service is
// asked, because a caller who wrote the flag asked for a replacement.
func rerun(ctx context.Context, in *invocation) (any, error) {
	var intent string
	if err := in.parseFlags("rerun", func(set *flag.FlagSet) {
		set.StringVar(&intent, "intent", "", "replace the recorded intent for the fresh run; it is recorded as acceptance criteria")
	}); err != nil {
		return nil, err
	}
	if flagWasSet(in.flags, "intent") && strings.TrimSpace(intent) == "" {
		return nil, usagef("--intent was given no intent; it takes what the change sets out to do, or leave it out to inherit the recorded one")
	}
	working, err := in.workingCopy(ctx)
	if err != nil {
		return nil, err
	}
	in.progressf("starting a fresh run of this branch from its last known head; this blocks until it needs a decision")
	var run machine.Run
	err = in.callService(ctx, ipc.MethodRunRerun, machine.RerunRequest{
		Working: machine.Working{WorkingPath: working},
		Intent:  intent,
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

// runTarget is --run: alone, with --task --answer, it answers the task's
// current run, and written --run=RUN it names the run whose kept copy a
// kept work answer is for, PRD section 9's hold answer. It is a boolean flag
// to the flag package on cancelFlag's terms, so --run alone keeps its
// meaning and the run is written joined to it.
type runTarget struct {
	// on is whether --run was given true or with a run.
	on bool
	// named is whether a run was written joined to it.
	named bool
	// run is the run named.
	run string
}

// String is the run as given, for the flag package's usage text.
func (r *runTarget) String() string { return r.run }

// Set records the flag.
func (r *runTarget) Set(value string) error {
	switch value {
	case "true":
		r.on, r.named, r.run = true, false, ""
	case "false":
		r.on, r.named, r.run = false, false, ""
	default:
		r.on, r.named, r.run = true, true, value
	}
	return nil
}

// IsBoolFlag tells the flag package --run may be given alone.
func (r *runTarget) IsBoolFlag() bool { return true }

// keptWorkFlags is what keptWorkAnswer reads off the command line.
type keptWorkFlags struct {
	answered, kept, tasked, runSet, fixing bool
	answer, hold, task                     string
	run                                    runTarget
	// confirm is --confirm, read with --answer discard alone.
	confirm    string
	confirmSet bool
	// others names every other flag that was given, which an answer to kept
	// work reads none of.
	others []string
}

// keptWorkAnswer answers a kept work entry, PRD section 9's hold answer
// given --hold kept-work/N: --answer is land, carry-on or discard, and
// exactly one of --task and --run=RUN names whose copy the entry is. Every
// other flag is refused rather than ignored.
func (in *invocation) keptWorkAnswer(ctx context.Context, f keptWorkFlags) (any, error) {
	switch {
	case !f.kept && f.confirmSet:
		return nil, usagef("--confirm takes the digest a discard of kept work was shown, and is read only with --answer discard and --hold kept-work/N")
	case !f.kept:
		return nil, usagef("--run=RUN names the run whose kept copy is answered, and is read only with --hold kept-work/N and --answer")
	case !f.answered:
		return nil, usagef("--hold %s names a kept work entry, and it is read only with --answer land, carry-on or discard", f.hold)
	case f.tasked && f.run.named:
		return nil, usagef("--task and --run=RUN both name whose copy the kept work is; pass one")
	case !f.tasked && !f.run.named:
		if f.runSet {
			return nil, usagef("--run with kept work names its run joined to it, as --run=RUN")
		}
		return nil, usagef("--hold %s names a kept work entry; name whose copy it is with --task or --run=RUN", f.hold)
	case f.tasked && f.runSet:
		return nil, usagef("--run answers a task's current run, and --hold %s a kept work entry; pass one", f.hold)
	case f.run.named && strings.TrimSpace(f.run.run) == "":
		return nil, usagef("--run= was given no run; it takes the identifier of the run whose kept copy is answered")
	case f.confirmSet && f.answer != string(store.KeptDiscard):
		return nil, usagef("--confirm is read with --answer discard alone, and was given with --answer %s", f.answer)
	case f.confirmSet && strings.TrimSpace(f.confirm) == "":
		return nil, usagef("--confirm was given no digest; it takes the one a discard shown beforehand carried")
	case len(f.others) > 0:
		return nil, usagef("an answer to kept work reads --answer, --hold, --task or --run=RUN, and --confirm with discard, and not --%s", f.others[0])
	}
	// A discard is confirmed only at a terminal, in the foreground, PRD
	// section 6's "Removing a discarded copy": a confirmation under --json
	// or off a terminal is refused here, before the service is asked, with
	// the row that says so. This is the early refusal, not the mechanism:
	// internal/service decides it from the peer's lineage either way
	// (keptdiscard.go, foreground.go).
	if f.confirmSet && (in.json || !in.env.Interactive) {
		return nil, discardNeedsTerminal{}
	}
	in.progressf("answering kept work %s with %q", f.hold, f.answer)
	var answered machine.KeptWorkAnswered
	err := in.callService(ctx, ipc.MethodKeptWorkAnswer, machine.KeptWorkAnswerRequest{
		Task: f.task, Run: f.run.run, Hold: f.hold, Answer: f.answer, Confirm: f.confirm,
	}, &answered)
	return answered, err
}

// discardNeedsTerminal is the command line's refusal of a discard
// confirmation given under --json or off a terminal. Its text is the row a
// person reads.
type discardNeedsTerminal struct{}

// Error is the row, which says all there is.
func (discardNeedsTerminal) Error() string { return machine.PhraseDiscardNeedsTerminal.String() }

// flagsBeside names the flags given to the command with no verb other than
// the four an answer to kept work reads and the two every command takes, in
// the order the flag package visits them.
func (in *invocation) flagsBeside() []string {
	var out []string
	in.flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "answer", "hold", "task", "run", "confirm", jsonFlagName, homeFlagName:
		default:
			out = append(out, f.Name)
		}
	})
	return out
}
