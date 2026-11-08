package agents

import "context"

// InterruptibleWorker is a Worker whose running turn can be ended over the
// pipe while the process stays up, the mechanism CapabilityPipeInterrupt
// names. An adapter declares that capability exactly when Start's workers
// implement this; Resolve refuses a declaration that disagrees with the
// Runner carrying pipeInterrupter.
type InterruptibleWorker interface {
	Worker

	// Interrupt ends the running turn and keeps the worker. It answers
	// true once the protocol has read that a turn was running and then
	// ended, and false when nothing was under way so nothing was sent.
	// ctx bounds the wait for a turn in flight to settle after the cancel.
	Interrupt(ctx context.Context) (bool, error)
}

// pipeInterrupter is the Runner half of CapabilityPipeInterrupt.
type pipeInterrupter interface {
	pipeInterrupt()
}

// InterruptPipeWorker is w.Interrupt for a worker r started, refused with a
// *CapabilityError and nothing sent when r has not declared
// CapabilityPipeInterrupt. An adapter that declared it and whose worker does
// not implement InterruptibleWorker is refused with an *AdapterError.
func InterruptPipeWorker(ctx context.Context, r Runner, w Worker) (bool, error) {
	if !r.Capabilities().Has(CapabilityPipeInterrupt) {
		return false, &CapabilityError{
			Agent: r.Name(), Capability: CapabilityPipeInterrupt,
			Path: "interrupting a pipe worker's turn",
		}
	}
	worker, ok := w.(InterruptibleWorker)
	if !ok {
		return false, declaredWithoutMechanism(r.Name(), CapabilityPipeInterrupt)
	}
	return worker.Interrupt(ctx)
}
