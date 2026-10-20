package session

import (
	"sync"

	"github.com/dayamjz/assistant/internal/agents"
)

// Placement is one live subordinate coordinator: the process group its host
// session leads, the domain it was started for, and, for a session with a
// terminal, the process hosting it. It is Membership's shape for the second
// held set, and like Membership it carries nothing else, because the domain
// is the whole of what a subordinate's identity buys.
type Placement struct {
	// Domain is the domain the subordinate coordinator was started for, as
	// the SubordinateSpec named it.
	Domain string
	// Group is the process group the subordinate's host session leads,
	// which agents.Worker.Group reports and the one a peer's process group
	// is compared against.
	Group int
	// Host is the identity of the process hosting the subordinate's
	// terminal, on Membership.Host's terms, and zero for one with no
	// terminal.
	Host agents.Identity
}

// Subordinates is the set of live subordinate coordinator sessions a
// provider has created and still holds, keyed by process group, beside
// Workers and on its terms: it is the authority input PRD section 9's
// placement derives a domain from, produced only by the providers' Delegate
// for exactly each session's process lifetime, with the join unexported so
// a placement cannot be stated from outside this package.
//
// It is a set of its own rather than a second meaning of Workers, for the
// reason Workers is not the containment registry: one map answering "which
// task's worker" and "which domain's subordinate" with one lookup would be
// two facts under one key, which P14 forbids, and the two answers turn
// opposite methods on and off. internal/service asks both sets and errs
// toward the side that claims less, which its workers.go owns.
//
// A Subordinates is safe for concurrent use.
type Subordinates struct {
	mu     sync.Mutex
	groups map[int]placed
	hosts  map[agents.Identity]placed
}

// placed is one entry: the placement and the signal that the session's
// process has ended.
type placed struct {
	placement Placement
	done      <-chan struct{}
}

// NewSubordinates returns an empty set, which is a provider that has started
// no subordinate coordinator and so places nobody.
func NewSubordinates() *Subordinates {
	return &Subordinates{groups: map[int]placed{}, hosts: map[agents.Identity]placed{}}
}

// Member reports the placement of a process group, and whether the group
// leads a subordinate coordinator this set holds whose done signal has not
// closed, on Workers.Member's terms.
func (s *Subordinates) Member(group int) (Placement, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return livePlaced(s.groups[group])
}

// Hosted looks a subordinate coordinator up by the identity of the tmux
// server hosting its terminal, on Workers.Hosted's terms: found only for
// the session's lifetime, and never for the zero identity.
func (s *Subordinates) Hosted(host agents.Identity) (Placement, bool) {
	if host == (agents.Identity{}) {
		return Placement{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return livePlaced(s.hosts[host])
}

// livePlaced is p's placement while its session's done signal is open.
func livePlaced(p placed) (Placement, bool) {
	if p.done == nil {
		return Placement{}, false
	}
	select {
	case <-p.done:
		return Placement{}, false
	default:
		return p.placement, true
	}
}

// Empty reports whether the set holds no subordinate coordinator at all,
// ended or not, on Workers.Empty's terms.
func (s *Subordinates) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.groups) == 0
}

// join records a subordinate's group, and its host when it has one, and
// returns the leave, on Workers.join's terms for an identifier already
// present.
func (s *Subordinates) join(p Placement, done <-chan struct{}) func() {
	entry := placed{placement: p, done: done}
	s.mu.Lock()
	s.groups[p.Group] = entry
	if p.Host != (agents.Identity{}) {
		s.hosts[p.Host] = entry
	}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if held, ok := s.groups[p.Group]; ok && held.done == done {
			delete(s.groups, p.Group)
		}
		if held, ok := s.hosts[p.Host]; ok && held.done == done {
			delete(s.hosts, p.Host)
		}
	}
}

// SubordinateSpec is what a subordinate coordinator's session is created
// with: PRD section 9's domain.delegate, which starts the session the way a
// worker's is started, through the same adapter under
// agents.WorkerEnvironment, in a directory of the home's rather than an
// isolated copy, because a subordinate coordinates and edits nothing.
type SubordinateSpec struct {
	// Domain is the domain the session coordinates, which the reference
	// embeds. It must satisfy home.ValidDomain.
	Domain string
	// Dir is the absolute directory the session runs in, the domain's own
	// under the home as home.Domain composes it. Nothing here asserts
	// anything about it: it is not a copy and holds no repository.
	Dir string
	// Instructions is what the subordinate is told first, on the terms
	// agents.WorkerSpec.Instructions states.
	Instructions string
	// Route is the command line prefix the subordinate reaches the program
	// through, on agents.WorkerSpec.Route's terms: the coordinator client,
	// so the grant allows exactly the commands the service then decides.
	Route string
	// Model and Effort are the adapter's, on Spec's terms.
	Model  string
	Effort string
}
