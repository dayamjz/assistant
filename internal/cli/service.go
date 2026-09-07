package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
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
	name string
	run  func(context.Context, *invocation) (any, error)
}{
	{"start", startService},
	{"stop", func(ctx context.Context, in *invocation) (any, error) {
		return stopService(ctx, in, ipc.MethodServiceStop)
	}},
	{"restart", func(ctx context.Context, in *invocation) (any, error) {
		return stopService(ctx, in, ipc.MethodServiceRestart)
	}},
	{"status", serviceStatus},
}

// serviceVerb runs the subcommand the command line names.
//
// The subcommand is taken from wherever it stands rather than from the first
// argument, so a global flag written between the verb and it is a flag and not
// a subcommand nobody recognizes. What is left is the subcommand's to parse,
// with the flags around it still in the order they were written.
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
	if len(in.args) == 0 {
		return nil, usagef("assistant service needs one of: %s", strings.Join(serviceActionNames(), ", "))
	}
	return nil, usagef("assistant service names none of: %s", strings.Join(serviceActionNames(), ", "))
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
	return in.serviceState(ctx), nil
}

// startService launches the background service and waits until it answers.
//
// --foreground runs it in this process instead, which is what the launched
// copy is invoked with. It is a flag rather than a verb because the process
// that serves and the command that launches it are one binary, and PRD
// section 9's table has one row for the service.
func startService(ctx context.Context, in *invocation) (any, error) {
	var foreground bool
	if err := in.parseFlags("service start", func(set *flag.FlagSet) {
		set.BoolVar(&foreground, "foreground", false, "run the service in this process rather than launching one")
	}); err != nil {
		return nil, err
	}
	if foreground {
		return nil, in.serveHere(ctx)
	}
	if state := in.serviceState(ctx); state.Running {
		return state, nil
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
	var force bool
	if err := in.parseFlags("service "+verb, func(set *flag.FlagSet) {
		set.BoolVar(&force, "force", false, "carry out the "+verb+" even while runs are active")
	}); err != nil {
		return nil, err
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
func (in *invocation) serveHere(parent context.Context) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	build, err := store.CurrentBuild()
	if err != nil {
		return err
	}
	running, err := service.Open(ctx, service.Options{
		Home:     in.home,
		Stages:   stages.All(),
		NewFixer: stages.PendingFixer,
		Build:    build,
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
		return in.launch()
	}
	return nil
}
