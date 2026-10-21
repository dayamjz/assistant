package cli

import (
	"os/exec"
	"testing"
	"time"
)

// ShortenReceiveWait makes a receive command wait d rather than internal/gate's
// bound for the rest of the test, so a test reaches the refusal past the bound
// without waiting the bound out. It reaches only cli.Run in this process: a
// push's receive command is another process and still waits the real bound.
func ShortenReceiveWait(t *testing.T, d time.Duration) {
	t.Helper()
	was := receiveWait
	receiveWait = d
	t.Cleanup(func() { receiveWait = was })
}

// BetweenEjectPreflightAndRemoval runs f inside eject's window between the
// reserved preflight and the gate's removal for the rest of the test. It
// changes a package variable, so a test using it does not run in parallel.
func BetweenEjectPreflightAndRemoval(t *testing.T, f func()) {
	t.Helper()
	was := betweenEjectPreflightAndRemoval
	betweenEjectPreflightAndRemoval = f
	t.Cleanup(func() { betweenEjectPreflightAndRemoval = was })
}

// Subcommands is each verb that takes a subcommand and its table, read from
// the tables the verbs dispatch on, as name and summary pairs in table
// order, so a test holds a verb's help to every row it dispatches rather than
// to a list written beside it.
func Subcommands() map[string][][2]string {
	out := map[string][][2]string{}
	for _, action := range serviceActions {
		out["service"] = append(out["service"], [2]string{action.name, action.summary})
	}
	for _, hook := range gateHooks {
		out[gateVerbName] = append(out[gateVerbName], [2]string{hook.name, hook.summary})
	}
	for _, hook := range hostHooks {
		out[hostVerbName] = append(out[hostVerbName], [2]string{hook.name, hook.summary})
	}
	for _, action := range coordinatorActions {
		out[coordinatorVerbName] = append(out[coordinatorVerbName], [2]string{action.name, action.summary})
	}
	for _, action := range workerActions {
		out[workerVerbName] = append(out[workerVerbName], [2]string{action.name, action.summary})
	}
	return out
}

// WorkerRoute is the route the service start gives a worker for a binary at
// executable serving the home at root, so a test that opens the service
// itself gives its workers the route the product would.
func WorkerRoute(executable, root string) string { return workerRoute(executable, root) }

// CoordinatorRoute is the route the service start gives a domain's
// subordinate coordinator, on WorkerRoute's terms.
func CoordinatorRoute(executable, root string) string { return coordinatorRoute(executable, root) }

// Detach is what launching the service does to the process it starts, so a
// test's stand-in service manager starts a service the way a manager would
// rather than as a child of the test's own process group.
func Detach(cmd *exec.Cmd) { detach(cmd) }

// The labels doctor prints its change's machine text after, so a test outside
// the package can tell those lines from the rows it holds to P13.
const (
	ChangeCommandLabel       = changeCommandLabel
	ChangeDefaultBranchLabel = changeDefaultBranchLabel
)

// ShellWords splits a printed command line on a POSIX shell's quoting rules,
// so a test can run a line doctor printed exactly as a person would copy it.
var ShellWords = shellWords

// InitMachineTextLabels is every label init's human rendering prints machine
// text after, each followed by a space, so a test holds every other line to
// P13's rule and leaves out only the lines carrying a person's own branch
// names, file names, keys and commands.
func InitMachineTextLabels() []string {
	return []string{
		initPreviousBranchLabel + " ", initDefaultBranchLabel + " ",
		commandsFileLabel + " ", commandsProposedLabel + " ", commandsWrittenLabel + " ",
		commandsConfiguredLabel + " ", commandsBranchKeyLabel + " ",
	}
}
