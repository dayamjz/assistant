package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
)

// Pipe is the reference provider: a pipe-backed terminal over the worker
// internal/agents starts. It holds the sessions it created, keyed by their
// reference, and answers Probe for those from the handle and for every other
// reference from the process identity alone, on the terms the package
// documentation states. It is also the one producer of the worker group
// set: every session it creates joins Workers for exactly the worker
// process's lifetime.
//
// A Pipe is safe for concurrent use.
type Pipe struct {
	home    string
	runner  agents.Runner
	workers *Workers

	mu   sync.Mutex
	held map[string]*Session
}

// NewPipe returns the reference provider for the home h, starting workers
// through r. r is asked for a worker at Create, through agents.OpenWorker,
// so an adapter that has not declared worker sessions is refused there and
// not here. The worker group set is the provider's own, read through
// Workers.
func NewPipe(h *home.Home, r agents.Runner) *Pipe {
	return &Pipe{home: HomeMarker(h), runner: r, workers: NewWorkers(), held: map[string]*Session{}}
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
	})
	if err != nil {
		return nil, fmt.Errorf("session: starting the worker: %w", err)
	}
	pid := worker.Group()
	// The identity is internal/agents' PID-reuse check, read once here and
	// carried in the reference; a platform with no read records zero.
	identity, found, err := agents.Identify(pid)
	switch {
	case errors.Is(err, agents.ErrIdentityUnreadable):
		identity = agents.Identity{}
	case err != nil:
		worker.Close()
		return nil, fmt.Errorf("%w: process %d: %w", ErrIdentity, pid, err)
	case !found:
		worker.Close()
		return nil, fmt.Errorf("%w: process %d was not running when looked for", ErrIdentity, pid)
	}
	s := &Session{
		ref:    Reference{Home: p.home, Task: spec.TaskID, PID: pid, Start: identity.Start},
		owner:  p,
		worker: worker,
	}
	// The group is a member from here until the process ends: Member reads
	// the done signal itself, and the goroutine takes the entry out once it
	// has closed, so the set does not grow with workers that have gone.
	s.leave = p.workers.join(Membership{Task: spec.TaskID, Group: pid}, worker.Done())
	go func() {
		<-worker.Done()
		s.leave()
	}()
	p.mu.Lock()
	p.held[s.ref.String()] = s
	p.mu.Unlock()
	return s, nil
}

// Capture returns the retained output and its digest, on the terms
// Provider.Capture states.
func (p *Pipe) Capture(s *Session) (Capture, error) {
	if !p.holds(s) {
		return Capture{}, ErrNotHeld
	}
	got := s.worker.Capture()
	return Capture{Text: got.Text, Dropped: got.Dropped, Digest: Digest(got.Text)}, nil
}

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
	if err := ctx.Err(); err != nil {
		return Capture{}, err
	}
	p.mu.Lock()
	s, held := p.held[ref]
	p.mu.Unlock()
	if !held {
		return Capture{}, ErrNotHeld
	}
	return p.Capture(s)
}

// Send delivers text on the terms Provider.Send states.
func (p *Pipe) Send(ctx context.Context, s *Session, text string) error {
	if !p.holds(s) {
		return ErrNotHeld
	}
	return s.worker.Send(ctx, text)
}

// Probe answers on the terms Provider.Probe states. A held session is
// answered from its handle; any other reference from the identity, which can
// prove absence and never presence of a reachable worker.
func (p *Pipe) Probe(ctx context.Context, ref string) Observation {
	if err := ctx.Err(); err != nil {
		return observe(ReasonUnreadable, "the probe was not made: %v", err)
	}
	p.mu.Lock()
	s, held := p.held[ref]
	p.mu.Unlock()
	if held {
		select {
		case <-s.worker.Done():
			return observe(ReasonExited, "the agent process %d this provider started has exited", s.ref.PID)
		default:
			return observe(ReasonHeld, "the agent process %d is running and this provider holds its session", s.ref.PID)
		}
	}
	decoded, err := ParseReference(ref)
	if err != nil {
		return observe(ReasonMalformed, "%v", err)
	}
	if decoded.Home != p.home {
		return observe(ReasonForeignHome, "the reference belongs to another home")
	}
	if decoded.Start == 0 {
		return observe(ReasonUnreadable, "the reference records no start time for process %d, so its presence proves nothing", decoded.PID)
	}
	presence, err := agents.Identity{PID: decoded.PID, Start: decoded.Start}.Presence()
	switch {
	case err != nil:
		return observe(ReasonUnreadable, "reading the start time of process %d: %v", decoded.PID, err)
	case presence == agents.PresenceAbsent:
		return observe(ReasonAbsent, "no running process has identifier %d", decoded.PID)
	case presence == agents.PresenceReplaced:
		return observe(ReasonReplaced, "process %d is running with another start time, and the reference names one started at %d", decoded.PID, decoded.Start)
	default:
		return observe(ReasonUnheld, "process %d is present and this provider holds no session for it", decoded.PID)
	}
}

// Close ends the session on the terms Provider.Close states.
func (p *Pipe) Close(s *Session) error {
	if s == nil || s.owner != p {
		return ErrNotHeld
	}
	key := s.ref.String()
	p.mu.Lock()
	held, ok := p.held[key]
	if ok && held == s {
		delete(p.held, key)
	}
	p.mu.Unlock()
	if !ok || held != s {
		return ErrNotHeld
	}
	s.worker.Close()
	s.leave()
	return nil
}

// holds reports whether s is a session this provider created and still
// holds.
func (p *Pipe) holds(s *Session) bool {
	if s == nil || s.owner != p {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held[s.ref.String()] == s
}
