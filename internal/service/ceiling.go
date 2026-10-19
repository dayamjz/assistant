package service

import (
	"context"
	"errors"
	"time"

	"github.com/dayamjz/assistant/internal/machine"
)

// errNoInvocationHistory is the read error admitInvocation reports when a
// positive ceiling has no store to read. A ceiling of zero never asks.
var errNoInvocationHistory = errors.New("the invocation history is not open")

// admitInvocation refuses to start another agent invocation when the
// operator's usage ceiling says the recorded input tokens in its window are
// over the count, or when that total is not established. A ceiling of zero,
// the schema default, returns nil and reads nothing, so a home that never
// set the key starts what it started before.
//
// The input is the invocation history usage.go reads for the usage view,
// totalled by machine.UsageOf and decided by machine.CeilingOn. The ceiling
// is the operator's resolved configuration, s.cfg, which is the global layer
// alone: the key is global-only, and a repository copy is not consulted here
// even so. No provider is asked what remains of a quota.
//
// A refusal is machine.UsageCeilingError. It does not cancel a start that
// already passed this check: the caller holds that invocation, and nothing
// here signals it. Two starts that both read a total under the ceiling can
// both run, because a call is absent from the history until its row is
// accepted. A row the recorder dropped is absent too, so the total can be
// under what was spent; invocation.go owns that drop.
//
// What the history does not hold is not covered. machine.UsageExcludes names
// a worker's turns and any price, and the comparison is input tokens alone,
// so output tokens and cache tokens are not part of the count either.
func (s *Service) admitInvocation(ctx context.Context) error {
	ceiling := s.cfg.UsageCeiling.InputTokens
	if ceiling <= 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	windowText := s.cfg.UsageCeiling.Window
	window, err := machine.ParseUsageWindow(windowText)
	if err != nil {
		return &machine.UsageCeilingError{Window: windowText, Ceiling: ceiling, Read: err}
	}
	if s.store == nil {
		return &machine.UsageCeilingError{Window: window.String(), Ceiling: ceiling, Read: errNoInvocationHistory}
	}
	readAt := time.Now().UTC()
	rows, err := s.store.AgentInvocationsSince(ctx, window.Since(readAt))
	if err != nil {
		return &machine.UsageCeilingError{Window: window.String(), Ceiling: ceiling, Read: err}
	}
	return machine.CeilingOn(machine.UsageOf(window, readAt, rows), ceiling)
}
