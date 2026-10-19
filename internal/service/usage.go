package service

import (
	"context"
	"fmt"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
)

// usage answers usage.read, PRD section 9's usage view: the agent invocation
// history the default catalog's recorder feeds, read over the requested
// window and totalled by machine.UsageOf. A window the request names and
// machine.ParseUsageWindow cannot read is refused as an invalid request
// rather than read as the default.
//
// The answer carries the operator's usage ceiling compared against the same
// rows, so the view and the refusal ceiling.go returns report one total; the
// rows are read back to the earlier of the two windows' starts for that.
//
// It reads the history as it stands at the read and keeps nothing between
// reads. What the history holds is what invocation.go feeds it, so the view
// carries that recorder's gaps rather than closing them: a row the store
// refused was dropped there and is in no total here, and a caller-supplied
// catalog records only what its own adapters arranged.
func (s *Service) usage(ctx context.Context, req machine.UsageRequest) (machine.Usage, error) {
	window, err := machine.ParseUsageWindow(req.Window)
	if err != nil {
		return machine.Usage{}, fmt.Errorf("%w: %w", ipc.ErrInvalidRequest, err)
	}
	readAt := time.Now().UTC()
	rows, err := s.store.AgentInvocationsSince(ctx, machine.UsageViewSince(window, s.cfg.UsageCeiling, readAt))
	if err != nil {
		return machine.Usage{}, err
	}
	return machine.UsageViewOf(window, s.cfg.UsageCeiling, readAt, rows), nil
}
