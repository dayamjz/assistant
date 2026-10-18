package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dayamjz/assistant/internal/agents"
)

// held is the part of a provider that is the same whichever way its workers
// are hosted: the sessions it created and still holds, keyed by reference,
// the worker group set it produces, and the one Probe rule. Pipe and Tmux
// each embed one, so the liveness rule the package documentation states has
// one implementation and a second provider cannot answer Live on terms of
// its own.
type held struct {
	marker  string
	workers *Workers
	// owner is the provider the sessions belong to, compared by identity so
	// a session one provider created is never another's.
	owner any

	mu       sync.Mutex
	sessions map[string]*Session
}

func newHeld(marker string, owner any) *held {
	return &held{marker: marker, workers: NewWorkers(), owner: owner, sessions: map[string]*Session{}}
}

// adopt makes worker a held session of task: it reads the worker's identity,
// joins its group to the set for the process's lifetime, and records the
// session under its reference. A worker whose identity cannot be read is
// ended and refused with ErrIdentity, as Provider.Create states.
func (h *held) adopt(task string, worker agents.Worker, attach []string) (*Session, error) {
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
		ref:    Reference{Home: h.marker, Task: task, PID: pid, Start: identity.Start},
		owner:  h.owner,
		worker: worker,
		attach: attach,
	}
	// The group is a member from here until the process ends: Member reads
	// the done signal itself, and the goroutine takes the entry out once it
	// has closed, so the set does not grow with workers that have gone.
	s.leave = h.workers.join(Membership{Task: task, Group: pid}, worker.Done())
	go func() {
		<-worker.Done()
		s.leave()
	}()
	h.mu.Lock()
	h.sessions[s.ref.String()] = s
	h.mu.Unlock()
	return s, nil
}

// holds reports whether s is a session this provider created and still
// holds.
func (h *held) holds(s *Session) bool {
	if s == nil || s.owner != h.owner {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[s.ref.String()] == s
}

// lookup is the held session a reference names, if any.
func (h *held) lookup(ref string) (*Session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[ref]
	return s, ok
}

// release ends s and forgets it, on the terms Provider.Close states.
func (h *held) release(s *Session) error {
	if s == nil || s.owner != h.owner {
		return ErrNotHeld
	}
	key := s.ref.String()
	h.mu.Lock()
	current, ok := h.sessions[key]
	if ok && current == s {
		delete(h.sessions, key)
	}
	h.mu.Unlock()
	if !ok || current != s {
		return ErrNotHeld
	}
	s.worker.Close()
	s.leave()
	return nil
}

// capture is Capture of a held session.
func (h *held) capture(s *Session) (Capture, error) {
	if !h.holds(s) {
		return Capture{}, ErrNotHeld
	}
	got := s.worker.Capture()
	return Capture{Failure: got.Failure, Text: got.Text, Dropped: got.Dropped, Uncounted: got.Uncounted, Digest: Digest(got.Text), Format: got.Format}, nil
}

// captureReference is capture addressed by reference, on the terms
// Pipe.CaptureReference states.
func (h *held) captureReference(ctx context.Context, ref string) (Capture, error) {
	if err := ctx.Err(); err != nil {
		return Capture{}, err
	}
	s, ok := h.lookup(ref)
	if !ok {
		return Capture{}, ErrNotHeld
	}
	return h.capture(s)
}

// attachReference is the attach command of a held session, ErrNotHeld for a
// reference no session here has, and ErrNotAttachable for a held session
// whose worker has no terminal.
func (h *held) attachReference(ref string) ([]string, error) {
	s, ok := h.lookup(ref)
	if !ok {
		return nil, ErrNotHeld
	}
	if len(s.attach) == 0 {
		return nil, ErrNotAttachable
	}
	return append([]string(nil), s.attach...), nil
}

// send is Send of a held session.
func (h *held) send(ctx context.Context, s *Session, text string) error {
	if !h.holds(s) {
		return ErrNotHeld
	}
	return s.worker.Send(ctx, text)
}

// probe answers on the terms Provider.Probe states. A held session is
// answered from its handle; any other reference from the identity, which can
// prove absence and never presence of a reachable worker. It is the one
// implementation of that rule, whichever provider asks.
func (h *held) probe(ctx context.Context, ref string) Observation {
	if err := ctx.Err(); err != nil {
		return observe(ReasonUnreadable, "the probe was not made: %v", err)
	}
	if s, ok := h.lookup(ref); ok {
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
	if decoded.Home != h.marker {
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
