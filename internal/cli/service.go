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
	"sync"
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
		if err := in.startableConfiguration(); err != nil {
			return nil, err
		}
		return installManaged(ctx, in, granted)
	}
	if state := in.serviceState(ctx); state.Running {
		state.Managed = in.managedState()
		return state, nil
	}
	if err := in.startableConfiguration(); err != nil {
		return nil, err
	}
	if m, err := in.manager(); err == nil {
		if installed, err := m.Installed(); err == nil && installed {
			return startManaged(ctx, in, m)
		}
	}
	exited, err := in.launch()
	if err != nil {
		return nil, err
	}
	in.progressf("waiting for the service to answer a readiness check")
	state, err := in.waitReady(ctx, "", exited)
	if err != nil {
		return nil, err
	}
	return state, nil
}

// startConfigurationRefused is a start refused before anything was launched,
// because the service reads the home's configuration file as it starts and
// would refuse to start on it. It reads out as internal/machine's row and
// then the file's own refusal, which names the file, the key and the value.
type startConfigurationRefused struct{ err error }

// Error is the row, then the refusal.
func (e startConfigurationRefused) Error() string {
	return machine.PhraseStartConfigurationRefused.String() + " " + e.err.Error()
}

// Unwrap resolves to the refusal of the file.
func (e startConfigurationRefused) Unwrap() error { return e.err }

// startableConfiguration reads the home's configuration file the way a
// starting service resolves it, and refuses when that read would refuse, so
// a start that cannot succeed says why here rather than as a readiness wait
// that runs out. It answers for that file when it is read here and for
// nothing else a start can fail on; the service reads the file again as it
// starts.
func (in *invocation) startableConfiguration() error {
	if _, err := service.GlobalConfig(in.home.ConfigFile()); err != nil {
		return startConfigurationRefused{err: err}
	}
	return nil
}

// launch starts a copy of this binary serving in the background. It is
// detached from this process's own lifetime, because the service outlives the
// command that started it.
//
// It is not a short-lived program, so it does not run through procgroup and
// carries no invocation deadline: what it starts is the service itself,
// which runs until it is stopped, and is the exception the owner's decision
// on #430 makes of a long-lived process, on that decision's terms. On unix
// the copy leads a session, and so a process group, of its own (detach).
// Its recorded owner is the service itself, which holds the home's lock
// for as long as it serves (PRD section 8). What ends it is its stop, a
// stop request or SIGTERM, which PRD section 8 bounds by stop_grace, or
// the operating system ending it outright. The launching command bounds
// only its own wait for readiness, readyTimeout, and ends nothing when that
// wait runs out. A restart's successor, which the outgoing service launches
// through this same path, is the same exception.
//
// The channel it returns is closed once that copy has exited. It is waited for
// on a goroutine of its own, so nothing here blocks for the life of the
// service; a command that ends first simply stops watching, and the service,
// detached, goes on.
func (in *invocation) launch() (<-chan struct{}, error) {
	cmd := exec.Command(in.env.Executable, "--home", in.home.Root(), "service", "start", "--foreground")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("launching the service: %w", err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	return exited, nil
}

// waitReady polls the readiness method until the service answers or the wait
// runs out.
//
// replacing is the instance a readiness answer may not come from, which is how
// a restart waits for the successor rather than for the service on its way
// out: the outgoing service is still bound to the socket when it accepts a
// restart, so the first answer would otherwise be its. It is empty when
// nothing is being replaced.
//
// exited is closed when the process this command launched to answer has
// exited, and is nil when this command launched nothing it can watch. A
// launched process that exited while nothing answers is reported then rather
// than when the wait runs out, and both failures point at the service log,
// where a service that held the home's lock and could not start writes why.
func (in *invocation) waitReady(ctx context.Context, replacing string, exited <-chan struct{}) (machine.Service, error) {
	deadline := time.Now().Add(readyTimeout)
	for {
		health, err := in.serviceHealth(ctx)
		switch {
		case err != nil:
		case replacing == "" || health.Instance != replacing:
			return in.serviceStateFrom(health), nil
		}
		if gone(exited) {
			return machine.Service{}, fmt.Errorf("the service exited before it answered a readiness check; why is in its log at %s", in.home.ServiceLog())
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

// gone reports whether a channel launch returned has been closed. A nil
// channel is a launch nobody is watching and is never gone.
func gone(exited <-chan struct{}) bool {
	if exited == nil {
		return false
	}
	select {
	case <-exited:
		return true
	default:
		return false
	}
}

// stopService stops or restarts the running service.
//
// PRD section 9 makes both refuse while runs are active, list the affected
// runs, and require an explicit flag for this act rather than a general
// yes-to-everything one, which is why the flag is named for what it forces.
//
// A request whose connection ended before any answer arrived is decided by
// what the service then does rather than reported as a failed request,
// because a service ending as it acts on a stop is one way that connection
// ends. A stop is answered as accepted once nothing answers a readiness
// check within the wait, and a restart once a service other than the one
// asked answers one; either wait running out returns the ended connection as
// the failure. What that observation does not establish is that this request,
// rather than something else acting at the same moment, is what ended the
// service.
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
		if !endedUnanswered(err) {
			return nil, err
		}
		return decideByObservation(ctx, in, method, outgoing.Instance, err)
	}
	if !answer.Accepted {
		return answer, nil
	}
	if method == ipc.MethodServiceRestart {
		in.progressf("waiting for the replacement service to answer a readiness check")
		if _, err := in.waitReady(ctx, outgoing.Instance, nil); err != nil {
			return nil, err
		}
	}
	return answer, nil
}

// endedUnanswered reports whether a request failed because its connection
// ended before the service answered it, as opposed to the service answering
// with a refusal, which is decided on what it said.
func endedUnanswered(err error) bool {
	var answered *ipc.Error
	return errors.Is(err, ipc.ErrClientClosed) && !errors.As(err, &answered)
}

// decideByObservation settles a stop or a restart whose connection ended with
// no answer, on stopService's terms: by whether the service stopped answering,
// or for a restart by whether a different service answers, within the wait.
// ended is the request's own failure, returned when neither is seen.
func decideByObservation(ctx context.Context, in *invocation, method ipc.Method, outgoing string, ended error) (any, error) {
	if method == ipc.MethodServiceRestart {
		in.progressf("the restart got no answer; waiting for a replacement service to answer a readiness check")
		if _, err := in.waitReady(ctx, outgoing, nil); err != nil {
			return nil, fmt.Errorf("%w; no replacement answered: %w", ended, err)
		}
		return machine.Lifecycle{Accepted: true, Restarting: true}, nil
	}
	in.progressf("the stop got no answer; waiting for the service to stop answering")
	gone, err := in.stoppedAnswering(ctx)
	switch {
	case err != nil:
		return nil, errors.Join(ended, err)
	case !gone:
		return nil, fmt.Errorf("%w; the service was still answering %s later", ended, readyTimeout)
	}
	return machine.Lifecycle{Accepted: true}, nil
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
//
// SIGINT and SIGTERM are taken on stopSignals' terms: the first begins the
// stop, which service.Shutdown bounds by stop_grace, and none is swallowed
// while the stop runs.
func (in *invocation) serveHere(parent context.Context, underManager bool) error {
	serving, begin := context.WithCancel(parent)
	defer begin()
	ctx, force, stop := stopSignals(serving)
	defer stop()

	build, err := store.CurrentBuild()
	if err != nil {
		return err
	}
	running, err := service.Open(ctx, service.Options{
		Home:             in.home,
		NewStages:        stages.All,
		NewFixer:         stages.Fix,
		Build:            build,
		WorkerRoute:      workerRoute(in.env.Executable, in.home.Root()),
		CoordinatorRoute: coordinatorRoute(in.env.Executable, in.home.Root()),
	})
	if err != nil {
		return err
	}
	serveErr := running.Serve(ctx)
	// The stop has begun, whether a signal or the protocol asked for it, so
	// a signal from here on forces it.
	begin()
	restarting := running.Restarting()
	closeErr := running.Shutdown(force)
	if errors.Is(closeErr, service.ErrStopForced) {
		// What the stop was still waiting on is abandoned with this process,
		// so no successor is launched over it; the next start recovers.
		return errors.Join(serveErr, closeErr)
	}
	if err := errors.Join(serveErr, closeErr); err != nil {
		return err
	}
	if restarting {
		if underManager {
			return managedRestart{}
		}
		_, err := in.launch()
		return err
	}
	return nil
}

// stopSignals is how a foreground service hears SIGINT and SIGTERM. The
// returned context ends at the first one or when parent ends, either of which
// is the stop beginning; serveHere ends parent once Serve returns, so a stop
// asked for through the protocol begins it too. A signal arriving after the
// stop has begun closes force, which service.Shutdown reads as a stop to be forced
// at once rather than waited out; once force is closed the default handling
// is restored, so a third signal ends the process the way the operating
// system ends one. No signal is swallowed at any point, which is what #407
// found signal.NotifyContext did once its first signal had been taken: its
// handler stayed installed for the whole shutdown, and every later signal
// was caught and did nothing.
//
// stop releases the handler, and is the caller's to defer.
func stopSignals(parent context.Context) (context.Context, <-chan struct{}, func()) {
	ctx, cancel := context.WithCancel(parent)
	force := make(chan struct{})
	heard := make(chan os.Signal, 2)
	signal.Notify(heard, os.Interrupt, syscall.SIGTERM)
	released := make(chan struct{})
	go func() {
		select {
		case <-heard:
			cancel()
		case <-ctx.Done():
		case <-released:
			return
		}
		select {
		case <-heard:
			close(force)
			signal.Reset(os.Interrupt, syscall.SIGTERM)
		case <-released:
		}
	}()
	var once sync.Once
	return ctx, force, func() {
		once.Do(func() {
			signal.Stop(heard)
			close(released)
			cancel()
		})
	}
}
