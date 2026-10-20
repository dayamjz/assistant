package session

import (
	"errors"
	"fmt"
	"sync"

	"github.com/dayamjz/assistant/internal/agents"
)

// Survivors is the placer for subordinate coordinators that outlived the
// service that started them: a service that restarts holds no session, so
// Subordinates is empty, while the process a delegation attempt's recorded
// reference names may still be running and still reaching the socket. A
// survivor is placed from that record and the kernel's own identity of the
// process, never from anything the process states: Adopt reads the
// reference the attempt recorded, which only a provider here wrote, and
// looks the process up by identifier and start time through
// agents.Identity.Presence; Member looks it up again at every ask, so the
// placement ends with the process as a held session's does.
//
// It is a set of its own, beside Subordinates and on its terms, because the
// two answer the same question from different evidence: Subordinates from a
// handle this provider holds, Survivors from a record plus a presence read.
// internal/service asks both, after the worker sets, and a chain neither
// answers for is the coordinator's own; a survivor placed here is what keeps
// a restart from handing a subordinate the first coordinator's commands.
//
// What a survivor's placement cannot carry: the identity of a tmux server
// hosting its terminal, which the reference does not record, so a window
// the survivor opens on its server is placed as nobody's until the
// delegation is next started by this service; and, where the platform
// reads no start time, a process later given the survivor's identifier,
// which Member then places as the subordinate until the delegation ends.
// Both err toward the subordinate, the side that claims less.
//
// A Survivors is safe for concurrent use.
type Survivors struct {
	mu      sync.Mutex
	entries map[int]survivor
}

// survivor is one adopted entry: the placement and the identity it is held
// to.
type survivor struct {
	placement Placement
	identity  agents.Identity
}

// NewSurvivors returns an empty set, which places nobody.
func NewSurvivors() *Survivors {
	return &Survivors{entries: map[int]survivor{}}
}

// Adopt derives a subordinate's placement from the reference a delegation
// attempt recorded, as store.DelegationAttempt.SessionRef holds it, for a
// provider whose home marker is marker. A reference ParseReference refuses,
// one of another home, or a worker's is refused with an error wrapping
// ErrReference and nothing is adopted. A process the presence read proves
// absent, or replaced by one with another start time, is not adopted and
// reports false: the subordinate is gone, which is the caller's relaunch to
// decide. A process read present is adopted. A reference with no start
// time, or a presence that could not be read, is adopted too, unverified,
// because a peer in that group is then the subordinate's or nobody's and
// the subordinate claims less; the reported error says which. A domain
// already adopted is replaced.
func (s *Survivors) Adopt(marker, text string) (Placement, bool, error) {
	ref, err := ParseReference(text)
	if err != nil {
		return Placement{}, false, err
	}
	if ref.Home != marker {
		return Placement{}, false, fmt.Errorf("%w: %s belongs to another home", ErrReference, text)
	}
	if !ref.Subordinate() {
		return Placement{}, false, fmt.Errorf("%w: %s is a worker's reference, and a survivor is a subordinate coordinator", ErrReference, text)
	}
	identity := agents.Identity{PID: ref.PID, Start: ref.Start}
	presence, err := identity.Presence()
	switch {
	case err == nil && presence != agents.PresencePresent:
		return Placement{}, false, nil
	case err != nil && !errors.Is(err, agents.ErrIdentityUnreadable):
		err = fmt.Errorf("session: the presence of survivor %s could not be read, so it is placed unverified: %w", text, err)
	case err != nil:
		err = fmt.Errorf("session: survivor %s records no start time this platform reads, so it is placed unverified: %w", text, err)
	}
	p := Placement{Domain: ref.Domain, Group: ref.PID}
	s.mu.Lock()
	s.forgetLocked(ref.Domain)
	s.entries[ref.PID] = survivor{placement: p, identity: identity}
	s.mu.Unlock()
	return p, true, err
}

// Member reports the placement of a process group, and whether the group
// is a survivor adopted here whose process is still present: the presence
// is read again at every ask, an absent or replaced process is forgotten
// and not a member, and one whose presence cannot be read stays a member,
// on Adopt's terms.
func (s *Survivors) Member(group int) (Placement, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[group]
	if !ok {
		return Placement{}, false
	}
	presence, err := entry.identity.Presence()
	if err == nil && presence != agents.PresencePresent {
		delete(s.entries, group)
		return Placement{}, false
	}
	return entry.placement, true
}

// Hosted answers for no identity: a survivor's reference records no host.
func (s *Survivors) Hosted(agents.Identity) (Placement, bool) {
	return Placement{}, false
}

// Forget drops the survivor adopted for a domain and returns the identity
// it was held to, for a caller that ends the process, and false for a
// domain with no survivor.
func (s *Survivors) Forget(domain string) (agents.Identity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.forgetLocked(domain)
}

// forgetLocked is Forget under the lock.
func (s *Survivors) forgetLocked(domain string) (agents.Identity, bool) {
	for group, entry := range s.entries {
		if entry.placement.Domain == domain {
			delete(s.entries, group)
			return entry.identity, true
		}
	}
	return agents.Identity{}, false
}

// Empty reports whether no survivor is adopted at all.
func (s *Survivors) Empty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries) == 0
}
