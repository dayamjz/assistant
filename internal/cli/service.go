package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// readyTimeout bounds how long starting the service waits for it to answer a
// readiness check. PRD section 8 makes launch and readiness different states,
// so this waits for the second rather than reporting the first.
const readyTimeout = 10 * time.Second

// readyPoll is how often the wait re-asks.
const readyPoll = 25 * time.Millisecond

// serviceActions are the four subcommands of the service verb, which PRD
// section 9's table gives one row. The list is the one owner of which four
// there are: it names the subcommand a command line holds and it runs the one
// that was named.
var serviceActions = []struct {
	name    string
	summary string
	run     func(context.Context, *invocation) (any, error)
}{
	{"start", "Launch the service in the background and wait until it answers a readiness check; --install hands it to this computer's service manager.", startService},
	{"stop", "Stop the service; refused while runs are active unless --force is given; --uninstall stops handing it to the service manager.",
		func(ctx context.Context, in *invocation) (any, error) {
			return stopService(ctx, in, ipc.MethodServiceStop)
		}},
	{"restart", "Stop the service and wait for its replacement to answer; refused while runs are active unless --force is given.",
		func(ctx context.Context, in *invocation) (any, error) {
			return stopService(ctx, in, ipc.MethodServiceRestart)
		}},
	{"status", "Report whether the service answers a readiness check.", serviceStatus},
}

// serviceVerb runs the subcommand the command line names.
//
// The subcommand is taken from wherever it stands rather than from the first
// argument, so a global flag written between the verb and it is a flag and not
// a subcommand nobody recognizes. What is left is the subcommand's to parse,
// with the flags around it still in the order they were written, so --help
// written beside a subcommand is that subcommand's and lists its flags.
//
// With no subcommand, --help anywhere after the verb is answered with the
// subcommand table, and anything else is incorrect usage phrased as what the
// verb needs and the subcommands to choose from.
func serviceVerb(ctx context.Context, in *invocation) (any, error) {
	for _, action := range serviceActions {
		for i, arg := range in.args {
			if arg != action.name {
				continue
			}
			in.args = append(append([]string{}, in.args[:i]...), in.args[i+1:]...)
			return action.run(ctx, in)
		}
	}
	if slices.ContainsFunc(in.args, isHelp) {
		return nil, subcommandHelp("service", serviceUsage())
	}
	return nil, subcommandRefusal("service", in.args, serviceActionNames())
}

// serviceUsage is the four read out one line each, for the verb's help.
func serviceUsage() string {
	var b strings.Builder
	b.WriteString("\nSubcommands:\n")
	for _, action := range serviceActions {
		fmt.Fprintf(&b, "  %-8s %s\n", action.name, action.summary)
	}
	return strings.TrimRight(b.String(), "\n")
}

// serviceActionNames is the four read out, for a refusal that says what was
// expected.
func serviceActionNames() []string {
	names := make([]string, 0, len(serviceActions))
	for _, action := range serviceActions {
		names = append(names, action.name)
	}
	return names
}

// serviceStatus reports whether the service is running, which is a real answer
// to the readiness method rather than the presence of a socket or a process.
func serviceStatus(ctx context.Context, in *invocation) (any, error) {
	if err := in.parseFlags("service status", func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	state := in.serviceState(ctx)
	state.Managed = in.managedState()
	return state, nil
}

// startService launches the background service and waits until it answers.
//
// --foreground runs it in this process instead, which is what the launched
// copy is invoked with. It is a flag rather than a verb because the process
// that serves and the command that launches it are one binary, and PRD
// section 9's table has one row for the service. --managed beside it is what
// a definition internal/managed wrote invokes it with, and changes one thing:
// see serveHere.
//
// --install hands the service to this computer's service manager instead of
// launching it, which managed.go owns. A home whose service is already handed
// over is started through the manager rather than launched beside it, so the
// service the manager would restart and the one this brings up are one.
func startService(ctx context.Context, in *invocation) (any, error) {
	var foreground, underManager, install, granted bool
	if err := in.parseFlags("service start", func(set *flag.FlagSet) {
		set.BoolVar(&foreground, "foreground", false, "run the service in this process rather than launching one")
		set.BoolVar(&underManager, "managed", false, "with --foreground: this process was started by the service manager, so a restart ends it for the manager to bring up again rather than launching a successor")
		set.BoolVar(&install, "install", false, "hand the service to this computer's service manager, launchd on macOS or systemd on Linux, so it comes back after a login or restart, and start it there")
		set.BoolVar(&granted, "full-disk-access-granted", false, "with --install on macOS: you granted this program Full Disk Access, so a home, program or checkout in Documents, Desktop or Downloads is not refused; nothing checks it")
	}); err != nil {
		return nil, err
	}
	switch {
	case underManager && !foreground:
		return nil, usagef("--managed is given with --foreground, which is how a service manager starts the service")
	case install && foreground:
		return nil, usagef("--install hands the service to the service manager, which starts it, so it is not given with --foreground")
	case granted && !install:
		return nil, usagef("--full-disk-access-granted is given with --install")
	}
	if foreground {
		return nil, in.serveHere(ctx, underManager)
	}
	if install {
		return installManaged(ctx, in, granted)
	}
	if state := in.serviceState(ctx); state.Running {
		state.Managed = in.managedState()
		return state, nil
	}
	if m, err := in.manager(); err == nil {
		if installed, err := m.Installed(); err == nil && installed {
			return startManaged(ctx, in, m)
		}
	}
	if err := in.launch(); err != nil {
		return nil, err
	}
	in.progressf("waiting for the service to answer a readiness check")
	state, err := in.waitReady(ctx, "")
	if err != nil {
		return nil, err
	}
	return state, nil
}

// launch starts a copy of this binary serving in the background. It is
// detached from this process's own lifetime, because the service outlives the
// command that started it.
func (in *invocation) launch() error {
	cmd := exec.Command(in.env.Executable, "--home", in.home.Root(), "service", "start", "--foreground")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching the service: %w", err)
	}
	// The child is not waited for: it outlives this command, and a wait here
	// would either block for the life of the service or reap it early.
	return cmd.Process.Release()
}

// waitReady polls the readiness method until the service answers or the wait
// runs out.
//
// replacing is the instance a readiness answer may not come from, which is how
// a restart waits for the successor rather than for the service on its way
// out: the outgoing service is still bound to the socket when it accepts a
// restart, so the first answer would otherwise be its. It is empty when
// nothing is being replaced.
func (in *invocation) waitReady(ctx context.Context, replacing string) (machine.Service, error) {
	deadline := time.Now().Add(readyTimeout)
	for {
		health, err := in.serviceHealth(ctx)
		switch {
		case err != nil:
		case replacing == "" || health.Instance != replacing:
			return in.serviceStateFrom(health), nil
		}
		if !time.Now().Before(deadline) {
			return machine.Service{}, fmt.Errorf("the service did not become ready within %s; its log is at %s", readyTimeout, in.home.ServiceLog())
		}
		select {
		case <-ctx.Done():
			return machine.Service{}, ctx.Err()
		case <-time.After(readyPoll):
		}
	}
}

// stopService stops or restarts the running service.
//
// PRD section 9 makes both refuse while runs are active, list the affected
// runs, and require an explicit flag for this act rather than a general
// yes-to-everything one, which is why the flag is named for what it forces.
func stopService(ctx context.Context, in *invocation, method ipc.Method) (any, error) {
	verb := "stop"
	if method == ipc.MethodServiceRestart {
		verb = "restart"
	}
	var force, uninstall bool
	if err := in.parseFlags("service "+verb, func(set *flag.FlagSet) {
		set.BoolVar(&force, "force", false, "carry out the "+verb+" even while runs are active")
		if method == ipc.MethodServiceStop {
			set.BoolVar(&uninstall, "uninstall", false, "stop handing the service to this computer's service manager: stop it, then remove what --install set up")
		}
	}); err != nil {
		return nil, err
	}
	if uninstall {
		return uninstallManaged(ctx, in, force)
	}
	// Which service is answering now is read before the request, because after
	// it there is no way to ask: the outgoing service answers the socket until
	// it lets go of it, and the successor answers the same socket afterwards.
	outgoing, err := in.serviceHealth(ctx)
	if err != nil {
		return nil, err
	}
	var answer machine.Lifecycle
	if err := in.callService(ctx, method, machine.LifecycleRequest{Force: force}, &answer); err != nil {
		return nil, err
	}
	if !answer.Accepted {
		return answer, nil
	}
	if method == ipc.MethodServiceRestart {
		in.progressf("waiting for the replacement service to answer a readiness check")
		if _, err := in.waitReady(ctx, outgoing.Instance); err != nil {
			return nil, err
		}
	}
	return answer, nil
}

// serveHere runs the service in this process until it is told to stop or the
// operating system asks it to.
//
// A service that stopped in order to be replaced launches its successor before
// this returns, which is what makes a restart one command rather than two: the
// lock is released here first, so the successor's bounded wait for it is short
// rather than a race it can lose.
//
// Under a service manager, underManager, it launches nothing and returns
// managedRestart instead, which exits with a failure status: the definition
// internal/managed wrote asks the manager to start the service again after a
// failure exit and not after the success exit a requested stop ends with, so
// the replacement is the manager's and is one the manager tracks. Whether the
// manager does so is its own; internal/managed's doc says what was verified.
func (in *invocation) serveHere(parent context.Context, underManager bool) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	build, err := store.CurrentBuild()
	if err != nil {
		return err
	}
	running, err := service.Open(ctx, service.Options{
		Home:        in.home,
		NewStages:   stages.All,
		NewFixer:    stages.Fix,
		Build:       build,
		WorkerRoute: workerRoute(in.env.Executable, in.home.Root()),
	})
	if err != nil {
		return err
	}
	serveErr := running.Serve(ctx)
	restarting := running.Restarting()
	closeErr := running.Close()
	if err := errors.Join(serveErr, closeErr); err != nil {
		return err
	}
	if restarting {
		if underManager {
			return managedRestart{}
		}
		return in.launch()
	}
	return nil
}
