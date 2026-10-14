package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// hostVerbName is the second command this binary answers to that PRD section
// 9's table does not name, and with gateVerbName the last: section 9 says
// the two are not a precedent for a third, because each is the interface a
// hook this program installed or asked for requires.
//
// This one is the interface the coordinator's host requires. PRD section 5's
// stop guard acts through a hook the host fires at turn end, which runs
// "<this binary> host stop", and the host reads that command's exit status
// to decide whether the turn may end. A host configured to run a command that
// is not served runs a hook that fails, and whether a failed hook blocks or
// allows is the host's convention, not this program's, so the command has to
// exist and answer.
//
// It is dispatched beside the table rather than added to it, for gateVerbName's
// reason, and it is in the usage text under a heading that says what it is
// for, for the same reason.
const hostVerbName = "host"

// HostReentryVar is the environment variable the coordinator's host sets on
// a turn it re-entered after the stop guard blocked the previous one. Set to
// any non-empty value, it is the re-entry signal PRD section 5 has the host
// supply; unset or empty, no signal was supplied, and a turn with work under
// way and no live supervisor is blocked.
//
// It is an environment variable rather than a flag on the hook's command
// line, for two reasons. The command line is configuration the host owns and
// writes once, so a flag there would be present on every invocation or on
// none and could not say which turn is the re-entered one; a value the host
// sets per invocation can. And a person who types the command at a shell
// must not pass the signal by accident, which an argument they could find in
// the usage text invites and a variable the usage text names as the host's
// does not.
//
// Nothing here consumes the signal: this process stores nothing, so it
// cannot tell the first re-entered turn from a second. The signal is
// consumed by the host's own re-entered turn, which is the one invocation
// the host sets the variable on; the next turn ends with the variable unset
// and is judged afresh. A host whose own protocol carries the signal another
// way, as a field in what it writes to the hook, bridges it by setting this
// variable, which is the installer's to arrange and is not built here.
const HostReentryVar = "ASSISTANT_HOST_REENTRY"

// hostBlockCode is the exit status that tells the coordinator's host the turn
// may not end, with the reason on standard error. It is the host's convention
// and this adapter's mapping of machine.HostStop onto it, which is why it is
// here and not in internal/machine: that package owns the three exit codes
// PRD section 9 gives the machine interface and the verdict's shape, and the
// host's protocol is neither.
//
// The value coincides with machine.ExitUsage, and that is a gap worth
// knowing: a host that invokes this command with a command line it refuses
// reads the usage failure as a block. That is the safe direction, since a
// misconfigured hook then blocks rather than allows, and the usage failure's
// text on standard error says what was wrong.
const hostBlockCode machine.Code = 2

// hostHooks are the subcommands the coordinator's host invokes. The list is
// the one owner of which there are, on gateHooks' terms: it names the
// subcommand, says what it is for, and runs the one that was named.
var hostHooks = []struct {
	name    string
	summary string
	run     func(context.Context, *invocation) (any, error)
}{
	{"stop", "Decide whether the coordinator's turn may end, from the hook the host fires at turn end.", hostStop},
}

// hostVerb runs the hook subcommand the command line names, which has to be
// the first thing after the verb, for the reason gateVerb gives: the command
// line is one a host's configuration holds rather than one a person types,
// and requiring the subcommand first removes the ambiguity a scan for it
// would have.
func hostVerb(ctx context.Context, in *invocation) (any, error) {
	if len(in.args) == 0 {
		return nil, usagef("assistant host needs one of: %s", strings.Join(hostHookNames(), ", "))
	}
	named := in.args[0]
	for _, hook := range hostHooks {
		if named != hook.name {
			continue
		}
		in.args = in.args[1:]
		return hook.run(ctx, in)
	}
	return nil, usagef("assistant host %s names none of: %s, which come first after the verb",
		named, strings.Join(hostHookNames(), ", "))
}

// hostHookNames is the subcommands read out, for a refusal that says what was
// expected.
func hostHookNames() []string {
	names := make([]string, 0, len(hostHooks))
	for _, hook := range hostHooks {
		names = append(names, hook.name)
	}
	return names
}

// hostStop answers the hook the coordinator's host fires at turn end: PRD
// section 5's stop guard, computed with no service.
//
// It reads what internal/supervisor/feed reads, the beacon file, the instance
// file, the lock probe and the in-flight set through the read-only open, and
// judges them with the guard that package and internal/supervisor own; the
// freshness is the operator's own supervision_beacon_interval, read the way
// the status command's pull guard reads it. It consults no socket, because
// the service it is asking about is exactly what may be dead, and a hook
// that needed the service to say the service is gone would answer nothing
// when it mattered. Everything that is not a definite answer blocks: a dead
// service, a file that cannot be read, a database that cannot be opened, and
// an operator file that does not parse, which leaves the guard uncomputed
// rather than judged by a default the operator did not set.
//
// The re-entry signal is HostReentryVar, read from the environment the host
// gave this process; see that constant for why it is a variable and what
// consumes it. Nothing is stored and nothing is written: the guard is a read
// of the home, and the answer is machine.HostStopOf over it.
//
// Two things it cannot do. It cannot place its caller: a process inside a
// registered worker group ending the coordinator's turn would be a worker
// deciding for the coordinator, but the worker set is the service's, reached
// over the socket by peer credentials, and this command has no socket and no
// peer, so it answers the same whoever invokes it. And it cannot tell a first
// re-entered turn from a second, for HostReentryVar's reason.
func hostStop(ctx context.Context, in *invocation) (any, error) {
	if err := in.parseFlags("host stop", func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	reEntered := in.env.Getenv != nil && in.env.Getenv(HostReentryVar) != ""
	var verdict supervisor.StopVerdict
	detail := ""
	cfg, err := service.GlobalConfig(in.home.ConfigFile())
	if err != nil {
		detail = "whether work is under way unwatched was not decided, because the configuration did not resolve: " + err.Error()
	} else {
		verdict = feed.Guard(ctx, in.home, supervisor.FreshnessFor(cfg.SupervisionBeaconInterval), time.Now())
	}
	answer := machine.HostStopOf(verdict, reEntered)
	answer.Detail = detail
	// The message is the host's to relay, and the host's convention reads it
	// off standard error beside the exit status; it is written whether or
	// not the answer is a document, because under --json standard output
	// carries the document and nothing else.
	if answer.Message != "" {
		in.progressf("%s", answer.Message)
	}
	return answer, nil
}

// hostExit maps the stop guard's answer onto the host's convention: success
// when the turn may end, hostBlockCode when it may not. Every answer maps to
// exactly one of the two, and nothing else here reads the answer.
func hostExit(answer machine.HostStop) machine.Code {
	if answer.Allowed {
		return machine.ExitOK
	}
	return hostBlockCode
}

// readOutHostStop is the human rendering of the stop guard's answer, which
// is nothing: the message went to standard error, where the host reads it,
// and a person running the command by hand reads it there too.
func readOutHostStop(io.Writer, machine.HostStop) {}

// hostUsage is the host's subcommands read out, under a heading that says
// these are not commands to type. It is built from hostHooks rather than
// spelled out, so a subcommand added there is a subcommand this describes.
func hostUsage() string {
	var b strings.Builder
	b.WriteString("\nInvoked by the coordinator's host at turn end rather than typed. The host sets\n")
	fmt.Fprintf(&b, "%s on a turn it re-entered after a block, and on no other:\n", HostReentryVar)
	for _, hook := range hostHooks {
		fmt.Fprintf(&b, "  %s %-7s %s\n", hostVerbName, hook.name, hook.summary)
	}
	return strings.TrimRight(b.String(), "\n")
}
