package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// errNoService reports that the background service did not answer. It is a
// local sentinel rather than something that crossed the wire, because nothing
// crossed it.
var errNoService = errors.New("the background service is not running")

// connect dials the home's socket. A home whose service is not running gives
// errNoService, which every verb that needs one reports with the command that
// starts it.
func (in *invocation) connect(ctx context.Context) (*ipc.Client, error) {
	client, err := ipc.Dial(ctx, in.home.Socket(), ipc.ClientConfig{Marker: ipc.LocalMarker(in.env.Getenv)})
	if err != nil {
		return nil, fmt.Errorf("%w at %s: %w", errNoService, in.home.Socket(), err)
	}
	return client, nil
}

// callService makes one call and decodes the answer.
//
// It holds the connection open for exactly the call, which matters for the
// calls that block: starting and responding do not answer until the run
// reaches its next decision point or a terminal outcome, per PRD section 9, so
// this waits as long as the run takes and the caller's context is what ends
// that wait.
func (in *invocation) callService(ctx context.Context, method ipc.Method, params, out any) error {
	client, err := in.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	return client.Call(ctx, method, params, out)
}

// serviceHealth asks whether the service is ready. PRD section 8 makes launch
// and readiness different states, so this is a real answer to the readiness
// method and never the presence of a socket file or a process.
func (in *invocation) serviceHealth(ctx context.Context) (machine.Health, error) {
	var health machine.Health
	if err := in.callService(ctx, ipc.MethodHealth, nil, &health); err != nil {
		return machine.Health{}, err
	}
	return health, nil
}

// serviceState reports what is known about the service, which is a field of an
// answer rather than a failure: a status that cannot reach the service still
// reports everything else, and reporting that it is down is most of what a
// person asked for.
func (in *invocation) serviceState(ctx context.Context) machine.Service {
	health, err := in.serviceHealth(ctx)
	if err != nil {
		return machine.Service{Socket: in.home.Socket(), Detail: err.Error()}
	}
	return in.serviceStateFrom(health)
}

// serviceStateFrom is what a readiness answer says about the service, so the
// two callers that have one report it the same way.
func (in *invocation) serviceStateFrom(health machine.Health) machine.Service {
	build := health.Build
	return machine.Service{Running: true, Socket: in.home.Socket(), Build: &build}
}

// openRecords opens a home's database with the credential remover PRD
// section 8 gives that job to. Every caller here goes through this, so no
// command opens the store without one.
func openRecords(ctx context.Context, h *home.Home) (*store.Store, error) {
	return store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
}

// failureCode is the machine-readable category of a failure, which is
// internal/ipc's vocabulary when the failure crossed the wire and empty when
// it did not.
func failureCode(err error) string {
	var wire *ipc.Error
	if errors.As(err, &wire) {
		return string(wire.Code)
	}
	return ""
}

// nextActions says what to do about a failure a caller can act on. PRD
// section 9 refuses silence after a failure, and these are the ones where the
// next step is the same every time.
//
// It is keyed on what survives the wire. A refusal the service raised arrives
// as an *ipc.Error carrying a code and a message, and the sentinel it was
// built from does not travel, so anything more specific than a code has to be
// read out of the message and is deliberately not attempted here.
var nextActions = []struct {
	// when reports whether this row answers for the failure.
	when func(error) bool
	// action is what to do about it.
	action string
}{
	{func(err error) bool { return errors.Is(err, errNoService) },
		"Start it with: assistant service start"},
	{func(err error) bool { return errors.Is(err, ipc.ErrContained) },
		"This process is inside a validation stage. It may inspect, fix, and return its own stage, and nothing else."},
	{func(err error) bool { return errors.Is(err, ipc.ErrUnidentifiedPeer) },
		"This platform does not let the service identify who is calling, so it refuses every request that drives a run."},
	{func(err error) bool { return errors.Is(err, ipc.ErrUnavailable) },
		"The service could not establish a fact this request depends on, so it refused rather than guessing."},
}

// nextAction is what to do about a failure, empty when there is nothing
// general to say.
func nextAction(err error) string {
	for _, row := range nextActions {
		if row.when(err) {
			return row.action
		}
	}
	return ""
}
