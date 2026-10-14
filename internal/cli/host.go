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

// HostReentryVar is the environment variable a coordinator's host may set on
// a turn it re-entered after the stop guard blocked the previous one. Set to
// any non-empty value, it is the re-entry signal PRD section 5 has the host
// supply; unset or empty, it supplies none. It is one of the two ways the
// signal arrives, and hostReentryField is the other: the field is what the
// host hostInstall writes the line for documents, and this variable stays for
// a host whose own protocol says nothing this command reads, which bridges
// it by setting the variable. Either one is the signal, and with neither a
// turn with work under way and no live supervisor is blocked.
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
// the host supplies it on; the next turn ends without it and is judged
// afresh. A host that supplied it on every turn would have disabled the
// guard, and nothing here can tell.
const HostReentryVar = "ASSISTANT_HOST_REENTRY"

// hostReentryField is the re-entry signal as the coordinator's host
// hostInstall configures puts it on the hook's standard input: a JSON object
// whose stop_hook_active member is true. The name and its meaning are what
// that host documents about itself, not something this package verified by
// running it: the 2.1.299 build of Claude Code carries, among its own
// messages, "For Stop/SubagentStop hooks, check stop_hook_active in the input
// and return success while it's true", and its documentation's "Hook Input
// (stdin JSON)" heading says where a command hook's input arrives; the Agent
// SDK for Python, 0.2.159, declares StopHookInput with "stop_hook_active:
// bool". The owner's decision on #95 is that this member is re-entry, read
// exactly as HostReentryVar is.
//
// That gives the member a gap the variable does not have. Nothing the host
// documents says the member names which hook blocked, so a turn another
// program's turn-end hook blocked may arrive here with the member true, and
// the guard then allows a turn it did not block itself. Whether the host
// sets it on only the turn after a block or on every turn of a continued
// chain is not documented either, and nothing here can tell the two apart.
//
// readHostInput is the reading, and it is strict in one direction only: the
// member allows a turn when it is there once, as the JSON literal true, in a
// whole object that parses and ends, and in nothing else. Input that is
// absent or empty, or an object without the member, supplies no signal and
// nothing is said about it, because a host that writes nothing, or another
// event, is not a fault. Input that is there and cannot be read that way -
// not JSON, not one object, trailing data, the member twice or as anything
// but a boolean, more than hostInputLimit bytes, a read that fails, or a
// stream still open after hostInputWait - supplies no signal either, and the
// answer says why in machine.HostStop's InputUnread, so unreadable input
// blocks rather than allows and the block is not mistaken for the guard's
// alone. Member names are matched exactly, as the host spells them, and not
// with encoding/json's case folding.
const hostReentryField = "stop_hook_active"

// hostInputLimit and hostInputWait bound the read of the hook's standard
// input, so a hook never holds a turn on its input. The limit is generous
// beside the input's documented members because one of them, the turn's last
// message, is the coordinator's own text and has no stated size; input past
// it is unread, and a re-entered turn whose input is that large is blocked
// again, which is the gap the bound costs. The wait is how long a stream
// still open is waited for; neither figure was measured against a host.
const (
	hostInputLimit = 4 << 20
	hostInputWait  = 3 * time.Second
)

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

// hostHooks are the subcommands under the host verb. The list is the one
// owner of which there are, on gateHooks' terms: it names the subcommand,
// says what it is for, says whether the host invokes it or a person types
// it, and runs the one that was named.
//
// stop is the one the host invokes. install is the one a person types: PRD
// section 6's stop guard row has an installer print the line the host's
// configuration needs and write it only under an explicit flag, and it is
// here because the line it prints is the command beside it, so the two cannot
// name different things. PRD section 9's row for this verb says a person
// never types it, which predates the installer; that is a finding against
// the specification, raised where this row was added, and not settled here.
var hostHooks = []struct {
	name    string
	byHost  bool
	summary string
	run     func(context.Context, *invocation) (any, error)
}{
	{"stop", true, "Decide whether the coordinator's turn may end, from the hook the host fires at turn end.", hostStop},
	{"install", false, "Print the line the host's settings need to run stop; --write adds it to that file.", hostInstall},
}

// hostVerb runs the hook subcommand the command line names, which has to be
// the first thing after the verb, for the reason gateVerb gives: the command
// line is one a host's configuration holds rather than one a person types,
// and requiring the subcommand first removes the ambiguity a scan for it
// would have.
//
// --help where the subcommand belongs is answered with the subcommand table,
// and anything else there, or nothing, is incorrect usage on gateVerb's terms.
func hostVerb(ctx context.Context, in *invocation) (any, error) {
	if len(in.args) > 0 {
		named := in.args[0]
		for _, hook := range hostHooks {
			if named != hook.name {
				continue
			}
			in.args = in.args[1:]
			return hook.run(ctx, in)
		}
		if isHelp(named) {
			return nil, subcommandHelp(hostVerbName, hostUsage())
		}
	}
	return nil, subcommandRefusal(hostVerbName, in.args, hostHookNames())
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
// The re-entry signal is hostReentryField on standard input, read by
// readHostInput under its bounds, or HostReentryVar in the environment the
// host gave this process; either one is the signal, and see those two for
// what each is and what consumes it. Input on standard input that is there
// and unreadable is no signal, and the answer's InputUnread says why. Nothing
// is stored and nothing is written: the guard is a read of the home, and the
// answer is machine.HostStopOf over it.
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
	byInput, unread := readHostInput(ctx, in.env.Stdin)
	byEnvironment := in.env.Getenv != nil && in.env.Getenv(HostReentryVar) != ""
	reEntered := byInput || byEnvironment
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
	answer.InputUnread = unread
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
// and a person running the command by hand reads it there too. It is the
// "host stop" row of the renderings table, listed as phrased because that
// message is two rows of internal/machine's phrase table and nothing else.
func readOutHostStop(io.Writer, machine.HostStop) {}

// hostUsage is the host's subcommands read out, the ones the host invokes
// under a heading that says they are not commands to type and the installer
// under one that says it is. It is built from hostHooks rather than
// spelled out, so a subcommand added there is a subcommand this describes.
func hostUsage() string {
	var b strings.Builder
	b.WriteString("\nInvoked by the coordinator's host at turn end rather than typed. A turn the\n")
	fmt.Fprintf(&b, "host re-entered after a block says so with %s true in the JSON on\n", hostReentryField)
	fmt.Fprintf(&b, "standard input, or with %s set, and no other turn does:\n", HostReentryVar)
	for _, hook := range hostHooks {
		if hook.byHost {
			fmt.Fprintf(&b, "  %s %-7s %s\n", hostVerbName, hook.name, hook.summary)
		}
	}
	b.WriteString("\nTyped by a person to set the host up, and changing nothing without --write:\n")
	for _, hook := range hostHooks {
		if !hook.byHost {
			fmt.Fprintf(&b, "  %s %-7s %s\n", hostVerbName, hook.name, hook.summary)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// readOutHostInstall is the human rendering of the installer's answer: the
// phrase for what became of the request, then the machine text a person
// copies, each on a line of its own after a label. The settings file is
// always named, because every row refers to it; the line is printed whenever
// it was not written, because then the person may want it; and a conflict's
// existing command line follows it, as the file holds it, escaped.
func readOutHostInstall(w io.Writer, answer machine.HostInstall) {
	writeln(w, machine.HostInstallPhrase(answer))
	writef(w, "%s %s\n", hostInstallFileLabel, printable(answer.Settings))
	if answer.Result != machine.HostInstallWritten && answer.Result != machine.HostInstallPresent {
		writef(w, "%s %s\n", hostInstallLineLabel, printable(answer.Line))
	}
	if answer.Conflict != "" {
		writef(w, "%s %s\n", hostInstallConflictLabel, printable(answer.Conflict))
	}
}

// The labels readOutHostInstall prints its machine text after, named so a
// test can tell the labelled lines from the phrase.
const (
	hostInstallFileLabel     = "Settings file:"
	hostInstallLineLabel     = "Line:"
	hostInstallConflictLabel = "Already there:"
)
