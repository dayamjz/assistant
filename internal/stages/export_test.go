package stages

import (
	"context"
	"time"
)

// WithChecksClock returns these dependencies with the clock the checks body
// tells the time and waits by replaced, so a test can drive a wait of days in
// no time. now answers what time it is, and pause is called in place of
// sleeping for d. It exists only in this package's tests: the product waits on
// the wall clock.
func WithChecksClock(d StageDeps, now func() time.Time, pause func(context.Context, time.Duration) error) StageDeps {
	d.clock = checksClock{nowFunc: now, pauseFunc: pause}
	return d
}

// CommandSlots is a set of command slots a test can share between the
// dependencies of two runs.
type CommandSlots = commandSlots

// NewCommandSlots returns n slots on newCommandSlots' terms.
func NewCommandSlots(n int) CommandSlots { return newCommandSlots(n) }

// WithCommandSlots returns these dependencies with their stage commands
// drawing from s rather than hostCommandSlots. It exists only in this
// package's tests: the product shares the one set the host is given.
func WithCommandSlots(d StageDeps, s CommandSlots) StageDeps {
	d.slots = s
	return d
}
