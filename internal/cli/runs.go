package cli

import (
	"context"
	"flag"
	"strings"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
)

// attachOrStart is the command with no verb: attach to this branch's active
// run, and with no run, start one.
//
// The service decides which of those it is, in one call, because the answer
// depends on records only it should be reading and writing while it holds the
// home. That call blocks until the run reaches its next decision point or a
// terminal outcome, per PRD section 9.
//
// --answer is how a decision is answered without a terminal to answer in. PRD
// section 9's table gives this command no verb of its own for answering, and
// the terminal interface collects the answer by showing the findings and
// taking a selection; an agent driving the same gate needs the same decision
// with the same options, so it supplies the answer as a parameter of the same
// command rather than through a verb the specification does not describe.
func attachOrStart(ctx context.Context, in *invocation) (any, error) {
	var (
		intent       string
		suppliedFlag bool
		answer       string
		skip         string
		cancel       bool
	)
	if err := in.parseFlags("", func(set *flag.FlagSet) {
		set.StringVar(&intent, "intent", "", "what this change sets out to do, in your own terms")
		set.BoolVar(&suppliedFlag, "intent-supplied", false, "treat the intent as authoritative acceptance criteria rather than a hint")
		set.StringVar(&answer, "answer", "", "answer the decision the run is holding on")
		set.BoolVar(&cancel, "cancel", false, "end this branch's run, wherever it stands")
		set.StringVar(&skip, "skip", "", "comma-separated stages this one run does not take")
	}); err != nil {
		return nil, err
	}
	if answer != "" && cancel {
		return nil, usagef("--answer and --cancel are two different things to do with one run; pass one")
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
	working, err := in.workingCopy(ctx)
	if err != nil {
		return nil, err
	}
	if cancel {
		return in.endRun(ctx, working)
	}
	if answer != "" {
		return in.answerDecision(ctx, working, answer)
	}
	in.progressf("starting or attaching to the run for this branch; this blocks until it needs a decision")
	var run machine.Run
	err = in.callService(ctx, ipc.MethodRunStart, machine.StartRequest{
		Working:        machine.Working{WorkingPath: working},
		Intent:         intent,
		IntentSupplied: supplied,
		Skip:           splitList(skip),
	}, &run)
	return run, err
}

// answerDecision answers the branch's active run. The run is found the same
// way attaching finds it, so a caller does not have to name an identifier it
// was never shown.
func (in *invocation) answerDecision(ctx context.Context, working, answer string) (any, error) {
	active, err := in.activeRun(ctx, working, "answer")
	if err != nil {
		return nil, err
	}
	in.progressf("answering %s with %q; this blocks until the run needs another decision", active, answer)
	var run machine.Run
	err = in.callService(ctx, ipc.MethodRunRespond, machine.RespondRequest{Run: active, Answer: answer}, &run)
	return run, err
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
// service, and a service that does not answer leaves its own field saying so
// rather than failing the report.
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
		return in.localStatus(working, err), nil
	}
	return status, nil
}

// localStatus is what a status reports when the service did not answer: the
// home, the working copy the command was run in, and why the service is not
// there.
//
// It reports less than a served status rather than reading the records the
// service owns, and it reads the working copy no further than the path,
// because everything else about a status has one owner and this is not it.
func (in *invocation) localStatus(working string, cause error) machine.Status {
	return machine.Status{
		Home:    in.home.Root(),
		Service: machine.Service{Socket: in.home.Socket(), Detail: cause.Error()},
		Branch:  &machine.Branch{WorkingPath: working},
		Detail:  "the service is not running, so the repository, gate, branch, and run state it owns were not read",
	}
}

// listRuns reports recent runs, newest first, or one run in full when it is
// named. Naming one is an argument rather than a command of its own, because
// PRD section 9's table has one row for runs.
func listRuns(ctx context.Context, in *invocation) (any, error) {
	var limit int
	if err := in.parseArgs("runs", 1, func(set *flag.FlagSet) {
		set.IntVar(&limit, "limit", 0, "how many runs to report, newest first; zero means all of them")
	}); err != nil {
		return nil, err
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

// rerun starts a fresh run of the branch this working copy is standing on,
// from that branch's last known head, inheriting the intent recorded there,
// and blocks on the same terms as attaching does.
//
// The branch is the working copy's, the same way attaching and status read it,
// so a caller never restarts a branch they are not on.
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
