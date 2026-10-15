package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/managed"
)

// The managed service: the home's service handed to the operating system's
// own service manager, so it comes back after a login or a restart. It is
// flags on the service verb PRD section 9's table already has rather than a
// verb of its own - --install on start, --uninstall on stop - and
// internal/managed owns the definition and the manager command lines; this
// file decides when they may be asked for and reads out what came of it.
//
// One service per home is the home's lock, and this file keeps installing
// from starting a second: an install is refused while a service answers the
// readiness check, before anything is written. Removal goes through the
// service's own stop first, which refuses while runs are active unless forced
// as PRD section 9 has every stop refuse, and the definition is unloaded only
// once the service has stopped answering, so the manager's own stop, which
// asks nothing, never ends a service that runs were under.

// managedRefused is a refusal internal/managed made, read out in
// internal/machine's row for its reason with what was found after it: the
// paths in a protected folder, the value that could not be written, or the
// manager's own output.
type managedRefused struct {
	refusal *managed.Refusal
}

// Error is the row, then what was found.
func (e managedRefused) Error() string {
	row := machine.ManagedRefusalPhrase(e.refusal.Reason).String()
	if e.refusal.Detail == "" {
		return row
	}
	return row + " Found: " + e.refusal.Detail
}

// Unwrap resolves to internal/managed's refusal.
func (e managedRefused) Unwrap() error { return e.refusal }

// asManagedRefusal reads a refusal internal/managed returned as the one a
// person reads, and returns any other error as it came.
func asManagedRefusal(err error) error {
	var refusal *managed.Refusal
	if errors.As(err, &refusal) {
		return managedRefused{refusal: refusal}
	}
	return err
}

// managedServiceUp is the refusal of an install while a service answers for
// the home, which would make the service the manager starts a second one.
type managedServiceUp struct{}

// Error is the row a person reads, which says all there is.
func (managedServiceUp) Error() string { return machine.PhraseManagedServiceUp.String() }

// managedRestart is what a service the manager started returns when it ends
// for a restart: a failure, so that the manager's restart policy brings up
// its replacement, read out as the row that says nothing went wrong.
type managedRestart struct{}

// Error is the row the manager's output holds.
func (managedRestart) Error() string { return machine.PhraseManagedRestart.String() }

// manager is this home's managed service on this platform, from the
// environment the command was run in.
func (in *invocation) manager() (*managed.Manager, error) {
	m, err := managed.ForThisPlatform(in.env.Getenv, in.home.Root())
	if err != nil {
		return nil, asManagedRefusal(err)
	}
	return m, nil
}

// managedState is the home's managed service as an answer reports it, and
// nil when none is set up or none can be on this platform. A definition that
// could not be read is reported as none rather than failing the answer it is
// a field of.
func (in *invocation) managedState() *machine.Managed {
	m, err := in.manager()
	if err != nil {
		return nil
	}
	installed, err := m.Installed()
	if err != nil || !installed {
		return nil
	}
	return &machine.Managed{Manager: string(m.Kind()), Unit: m.Unit()}
}

// installManaged writes the home's service definition, has the manager load
// and start it, and waits for the service to answer, as a start does.
//
// It refuses while a service answers for the home, and it hands
// internal/managed every checkout the home's records name, which a launchd
// install holds to macOS's protected folders along with the home and this
// program. granted is --full-disk-access-granted, the operator's statement
// that access was granted, which nothing here checks.
func installManaged(ctx context.Context, in *invocation, granted bool) (any, error) {
	m, err := in.manager()
	if err != nil {
		return nil, err
	}
	if in.serviceState(ctx).Running {
		return nil, managedServiceUp{}
	}
	if err := in.home.Create(); err != nil {
		return nil, err
	}
	records, err := in.openStore(ctx)
	if err != nil {
		return nil, err
	}
	repositories, err := records.Repositories(ctx)
	_ = records.Close()
	if err != nil {
		return nil, fmt.Errorf("reading the checkouts this home serves: %w", err)
	}
	checkouts := make([]string, 0, len(repositories))
	for _, r := range repositories {
		checkouts = append(checkouts, r.WorkingPath)
	}
	if err := m.Install(ctx, managed.Spec{
		Executable:    in.env.Executable,
		Root:          in.home.Root(),
		Output:        in.home.ManagedServiceOutput(),
		SearchPath:    in.env.Getenv("PATH"),
		Repositories:  checkouts,
		AccessGranted: granted,
	}); err != nil {
		return nil, asManagedRefusal(err)
	}
	in.progressf("waiting for the service the manager started to answer a readiness check")
	state, err := in.waitReady(ctx, "", nil)
	if err != nil {
		return nil, err
	}
	state.Managed = &machine.Managed{Manager: string(m.Kind()), Unit: m.Unit()}
	return state, nil
}

// startManaged has the manager start the home's installed service and waits
// for it to answer. It is what assistant service start does when a
// definition is set up, so the service the manager restarts and the one a
// start brings up are the same and not two.
func startManaged(ctx context.Context, in *invocation, m *managed.Manager) (any, error) {
	if err := m.Start(ctx); err != nil {
		return nil, asManagedRefusal(err)
	}
	in.progressf("waiting for the service the manager started to answer a readiness check")
	state, err := in.waitReady(ctx, "", nil)
	if err != nil {
		return nil, err
	}
	state.Managed = &machine.Managed{Manager: string(m.Kind()), Unit: m.Unit()}
	return state, nil
}

// uninstallManaged stops the home's service through its own stop, waits for
// it to stop answering, and then has the manager unload the definition and
// removes it.
//
// It refuses with nothing set up before stopping anything, so the flag is not
// a second way to stop a service. A stop the service refuses, because runs
// are active and force was not given, is returned as that answer and the
// definition is left as it was. A stop whose connection ended with no answer
// is decided by whether the service then stops answering within the wait,
// because the definition may go only once no service is up to be ended by
// the manager's own stop. A service that does not answer is not asked to
// stop, and the definition is removed.
func uninstallManaged(ctx context.Context, in *invocation, force bool) (any, error) {
	m, err := in.manager()
	if err != nil {
		return nil, err
	}
	installed, err := m.Installed()
	if err != nil {
		return nil, err
	}
	if !installed {
		return nil, managedRefused{refusal: &managed.Refusal{Reason: managed.ReasonNotInstalled, Detail: m.Unit()}}
	}
	stopped := false
	if _, err := in.serviceHealth(ctx); err == nil {
		var answer machine.Lifecycle
		stopErr := in.callService(ctx, ipc.MethodServiceStop, machine.LifecycleRequest{Force: force}, &answer)
		var refused *ipc.Error
		switch {
		case errors.As(stopErr, &refused):
			// The service answered, with a refusal of the request.
			return nil, stopErr
		case stopErr == nil && !answer.Accepted:
			return answer, nil
		}
		// An accepted stop, or a connection that ended before an answer
		// arrived, which a stop that was accepted can produce: either way
		// what decides is whether the service stops answering, and one that
		// does not leaves the definition in place and reports what happened.
		in.progressf("waiting for the service to stop answering")
		if err := in.waitGone(ctx); err != nil {
			if stopErr != nil {
				return nil, stopErr
			}
			return nil, err
		}
		stopped = true
	}
	if err := m.Remove(ctx); err != nil {
		return nil, asManagedRefusal(err)
	}
	return machine.ManagedRemoval{
		Managed: machine.Managed{Manager: string(m.Kind()), Unit: m.Unit()},
		Stopped: stopped,
	}, nil
}

// waitGone polls the readiness method until the service stops answering or
// the wait runs out, which is a failure that leaves everything as it was.
func (in *invocation) waitGone(ctx context.Context) error {
	gone, err := in.stoppedAnswering(ctx)
	switch {
	case err != nil:
		return err
	case !gone:
		return errors.New(machine.PhraseManagedStillUp.String() + " Its log: " + in.home.ServiceLog())
	}
	return nil
}

// stoppedAnswering polls the readiness method until nothing answers it, and
// reports whether that happened before the wait ran out. Not answering is
// any failure to get a readiness answer, a refused connection and a missing
// socket among them, which is what a service that has let go of the home
// leaves.
func (in *invocation) stoppedAnswering(ctx context.Context) (bool, error) {
	deadline := time.Now().Add(readyTimeout)
	for {
		if _, err := in.serviceHealth(ctx); err != nil {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(readyPoll):
		}
	}
}
