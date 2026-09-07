package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
)

// gateVerbName is the one command this binary answers to that PRD section 9's
// table does not name.
//
// That section's table is what a person types and what a driving agent calls,
// and the verbs table in cli.go is that table and nothing else; a verb the
// section does not describe is a finding against the specification rather than
// a row there. This is neither. It is the interface internal/gate requires of
// this command surface: the hooks it installs in a gate repository invoke
// "<this binary> gate admit" and "<this binary> gate notify", and hooks.go
// states that requirement. A gate whose hooks call a command that is not
// served installs executable files that fail, so a push is refused before
// anything starts.
//
// It is dispatched beside that table rather than added to it, because the
// table is a claim and a row added there would quietly weaken it. It is in the
// usage text under a heading that says what it is for, because a command this
// binary answers to and does not mention is worse than either.
const gateVerbName = "gate"

// gateHooks are the two subcommands internal/gate's hooks invoke. The list is
// the one owner of which two there are: it names the subcommand a command line
// holds, says what it is for, and runs the one that was named.
var gateHooks = []struct {
	name    string
	summary string
	run     func(context.Context, *invocation) (any, error)
}{
	{"admit", "Decide whether a push may proceed, before any reference in the gate changes.", admitPush},
	{"notify", "Start the run a push that was accepted calls for.", notifyPush},
}

// gateVerb runs the hook subcommand the command line names, which has to be
// the first thing after the verb.
//
// assistant service takes its four from wherever they stand, so that a global
// flag written between the verb and the subcommand is read as a flag. That
// rule cannot be applied here without a second list of which flags take a
// value: a scan that matched a subcommand name anywhere would read the value
// behind --gate or --home as the subcommand and dispatch on it. This command
// line is one internal/gate writes rather than one a person types, and it
// always writes the subcommand first, so requiring that costs nothing and
// removes the ambiguity. --json and --home written before the verb are already
// read by parseGlobal, and written after the subcommand they reach the verb's
// own flag set.
func gateVerb(ctx context.Context, in *invocation) (any, error) {
	if len(in.args) == 0 {
		return nil, usagef("assistant gate needs one of: %s", strings.Join(gateHookNames(), ", "))
	}
	named := in.args[0]
	for _, hook := range gateHooks {
		if named != hook.name {
			continue
		}
		in.args = in.args[1:]
		return hook.run(ctx, in)
	}
	return nil, usagef("assistant gate %s names none of: %s, which come first after the verb",
		named, strings.Join(gateHookNames(), ", "))
}

// gateHookNames is the two read out, for a refusal that says what was
// expected.
func gateHookNames() []string {
	names := make([]string, 0, len(gateHooks))
	for _, hook := range gateHooks {
		names = append(names, hook.name)
	}
	return names
}

// admitPush answers the gate's admission hook, which runs before any reference
// in the gate changes. Its exit status is what git reads, so a failure here
// rejects the push and PRD section 5's refusal-instead-of-a-change holds.
//
// It decides nothing. The reference update lines are read by internal/gate,
// which owns the hook protocol it wrote them into, and whether the push may
// proceed is the service's answer. A verb that reached for that answer itself
// would be a second admission check, running in the pushing process, over
// records only the service should be reading.
//
// On the path a push takes, nothing is resolved from PATH or from the
// environment the push happens in: internal/gate writes the absolute path of
// this binary, the gate identifier, and the home into the hook script, so what
// admission runs and which home it acts on are both settled where the gate was
// initialized. --home is the global flag every verb takes and is read here on
// exactly those terms, which is also why running this by hand without one
// falls back to the environment the way every other verb does. A hook never
// does.
func admitPush(ctx context.Context, in *invocation) (any, error) {
	request, err := in.gateRequest("admit")
	if err != nil {
		return nil, err
	}
	var admission machine.Admission
	err = in.callService(ctx, ipc.MethodGateAdmit, request, &admission)
	return admission, err
}

// notifyPush answers the gate's notification hook, which runs once a push has
// been accepted. This is where a run begins.
//
// It hands off and returns. PRD section 8 has a push return immediately, with
// the service owning everything long-running, so the call it makes answers
// once the runs are recorded and handed to the service rather than when they
// finish. A verb that blocked here would hold the pushing git open for the
// length of a validation.
func notifyPush(ctx context.Context, in *invocation) (any, error) {
	request, err := in.gateRequest("notify")
	if err != nil {
		return nil, err
	}
	var notification machine.Notification
	err = in.callService(ctx, ipc.MethodGateNotify, request, &notification)
	return notification, err
}

// gateRequest parses the flags both hooks take and the reference update lines
// both are given, which is everything either of them sends.
//
// The identifier is required rather than defaulted. A hook always writes one,
// and a default would mean a hand-run command acting on a gate nobody named.
func (in *invocation) gateRequest(hook string) (machine.GateRequest, error) {
	var id string
	if err := in.parseFlags("gate "+hook, func(set *flag.FlagSet) {
		set.StringVar(&id, "gate", "", "the identifier of the gate the push is arriving at")
	}); err != nil {
		return machine.GateRequest{}, err
	}
	if strings.TrimSpace(id) == "" {
		return machine.GateRequest{}, usagef("assistant gate %s needs --gate with the identifier of the "+
			"gate the push is arriving at; a gate's hooks write it in", hook)
	}
	updates, err := gate.ParseRefUpdates(in.env.Stdin)
	if err != nil {
		return machine.GateRequest{}, err
	}
	return machine.GateRequest{Gate: id, Updates: updates}, nil
}

// readOutAdmission is what a person pushing sees when the gate admits the
// push. It goes to standard output, which git relays to them prefixed as
// coming from the remote, so it says which gate accepted what rather than only
// that something did.
func readOutAdmission(w io.Writer, a machine.Admission) {
	writef(w, "Admitted to gate %s: %s\n", a.Gate, refList(a.Refs))
}

// readOutNotification is what a person pushing sees once the push is in: the
// runs it started, and every reference it started nothing for with the reason.
func readOutNotification(w io.Writer, n machine.Notification) {
	for _, started := range n.Started {
		writef(w, "Started run %s for %s at %s\n", started.Run, started.Branch, short(started.Head))
		if started.Superseded != "" {
			writef(w, "  superseded run %s, which was validating an earlier push of %s\n",
				started.Superseded, started.Branch)
		}
	}
	for _, ignored := range n.Ignored {
		writef(w, "No run for %s: %s\n", ignored.Ref, ignored.Reason)
	}
	if len(n.Started) == 0 && len(n.Ignored) == 0 {
		writef(w, "The push to gate %s carried no reference updates, so no run was started.\n", n.Gate)
	}
}

// refList reads out the references a push updates, for a message about the
// push as a whole.
func refList(refs []string) string {
	if len(refs) == 0 {
		return "no reference updates"
	}
	return strings.Join(refs, ", ")
}

// gateUsage is the hook subcommands read out, under a heading that says these
// are not commands to type. It is built from gateHooks rather than spelled
// out, so a subcommand added there is a subcommand this describes.
func gateUsage() string {
	var b strings.Builder
	b.WriteString("\nInvoked by a gate's hooks rather than typed. internal/gate writes this binary's\n")
	b.WriteString("absolute path, the gate, and the home into the hook, so a push chooses none of them:\n")
	for _, hook := range gateHooks {
		fmt.Fprintf(&b, "  %s %-7s %s\n", gateVerbName, hook.name, hook.summary)
	}
	return strings.TrimRight(b.String(), "\n")
}
