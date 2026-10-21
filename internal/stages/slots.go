package stages

import (
	"context"
	"runtime"
)

// commandSlots bounds how many configured stage commands this process runs
// at once. A command waits for a slot before it starts, so a run whose stage
// finds every slot taken waits rather than starting another process tree on
// a host already running as many as it was given; its runtime bound
// (config.KeyCommandTimeout) starts once it holds one, and ending its run's
// context ends the wait.
//
// It bounds how many commands run together and nothing else: one command
// may still use every core, which is what the full-repository commands #419
// observed do, and nothing here lowers a command's scheduling priority.
// Nothing reports that a run is waiting for a slot either; the stage reads
// as running for the whole wait. An operator's command (RunOperatorCommand)
// takes no slot, because its caller bounds it on terms of its own.
type commandSlots chan struct{}

// newCommandSlots returns n slots, and one when n is less than one, so no
// value leaves commands unbounded or lets none run.
func newCommandSlots(n int) commandSlots {
	return make(commandSlots, max(1, n))
}

// hostCommandSlots is the bound every stage body shares unless a test gave
// it another, so every run of every service in this process draws from one
// set. It is a quarter of the host's logical CPUs, and at least one.
//
// Chosen, not measured. The commands it bounds (go test, golangci-lint, a
// build) each parallelize across every core themselves, so running many of
// them at once buys little throughput and multiplies contention, while
// running only one would serialize every run on a host behind its slowest
// check. It is not a configuration key because the cost of a bound set too
// low is waiting rather than a failed run, so an operator has no remedy to
// reach for until a host shows otherwise.
var hostCommandSlots = newCommandSlots(runtime.NumCPU() / 4)

// acquire takes a slot, or returns ctx's error when ctx ends first.
func (s commandSlots) acquire(ctx context.Context) error {
	select {
	case s <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release returns a slot acquire took.
func (s commandSlots) release() { <-s }
