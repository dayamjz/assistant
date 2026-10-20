package session

import (
	"context"
	"fmt"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
)

// Pipe is the reference provider: a pipe-backed terminal over the worker
// internal/agents starts. It holds the sessions it created, keyed by their
// reference, and answers Probe for those from the handle and for every other
// reference from the process identity alone, on the terms the package
// documentation states. It is also a producer of the worker group set: every
// session it creates joins Workers for exactly the worker process's
// lifetime.
//
// A Pipe is safe for concurrent use.
type Pipe struct {
	runner agents.Runner
	*held
}

// NewPipe returns the reference provider for the home h, starting workers
// through r. r is asked for a worker at Create, through agents.OpenWorker,
// so an adapter that has not declared worker sessions is refused there and
// not here. The worker group set is the provider's own, read through
// Workers.
func NewPipe(h *home.Home, r agents.Runner) *Pipe {
	p := &Pipe{runner: r}
	p.held = newHeld(HomeMarker(h), p)
	return p
}

// Workers is the set of live worker groups this provider has created, on
// the terms Workers states. It is the same set for the provider's lifetime,
// so a caller may hold it before any session exists.
func (p *Pipe) Workers() *Workers { return p.workers }

// Create starts the worker on the terms Provider.Create states.
func (p *Pipe) Create(ctx context.Context, spec Spec) (*Session, error) {
	if !spec.Isolation.Proven() {
		return nil, ErrUnproven
	}
	if err := home.ValidTaskID(spec.TaskID); err != nil {
		return nil, err
	}
	worker, err := agents.OpenWorker(ctx, p.runner, agents.WorkerSpec{
		Dir:          spec.Isolation.Copy,
		Instructions: spec.Instructions,
		Route:        spec.Route,
		Model:        spec.Model,
		Effort:       spec.Effort,
	})
	if err != nil {
		return nil, fmt.Errorf("session: starting the worker: %w", err)
	}
	return p.adopt(spec.TaskID, worker, nil, agents.Identity{})
}

// Subordinates is the set of live subordinate coordinators this provider
// has started, on the terms Subordinates states.
func (p *Pipe) Subordinates() *Subordinates { return p.subordinates }

// Delegate starts a subordinate coordinator on the terms Host.Delegate
// states, through agents.OpenWorker as Create does.
func (p *Pipe) Delegate(ctx context.Context, spec SubordinateSpec) (*Session, error) {
	if err := home.ValidDomain(spec.Domain); err != nil {
		return nil, err
	}
	worker, err := agents.OpenSubordinate(ctx, p.runner, agents.WorkerSpec{
		Dir: spec.Dir, Instructions: spec.Instructions, Route: spec.Route, Model: spec.Model, Effort: spec.Effort,
	})
	if err != nil {
		return nil, fmt.Errorf("session: starting the subordinate coordinator of domain %s: %w", spec.Domain, err)
	}
	return p.adoptSubordinate(spec.Domain, worker, nil, agents.Identity{})
}

// Capture returns the retained output and its digest, on the terms
// Provider.Capture states.
func (p *Pipe) Capture(s *Session) (Capture, error) { return p.capture(s) }

// CaptureReference is Capture addressed by a session's encoded reference
// rather than its handle, for a caller that reads references out of the
// record and holds no handle, which internal/service's terminal hookup is.
// It is answered out of the same held set Probe answers Live from, so a
// reference Probe answers Live for is one this captures, and every other
// reference, held by no provider or by another one, is ErrNotHeld, which is
// that terminal's failure and never the provider's. The read is of retained
// output in memory and does not block, so ctx is honoured before it and
// there is nothing for it to end during it.
func (p *Pipe) CaptureReference(ctx context.Context, ref string) (Capture, error) {
	return p.captureReference(ctx, ref)
}

// AttachReference answers ErrNotAttachable for a session this provider
// holds, because a pipe has no terminal anyone can attach to, and ErrNotHeld
// for any other reference.
func (p *Pipe) AttachReference(ref string) ([]string, error) { return p.attachReference(ref) }

// Send delivers text on the terms Provider.Send states.
func (p *Pipe) Send(ctx context.Context, s *Session, text string) error { return p.send(ctx, s, text) }

// Probe answers on the terms Provider.Probe states.
func (p *Pipe) Probe(ctx context.Context, ref string) Observation { return p.probe(ctx, ref) }

// Close ends the session on the terms Provider.Close states.
func (p *Pipe) Close(s *Session) error { return p.release(s) }
