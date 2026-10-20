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
// A survivor hosted in a tmux server is placed by the server too: the
// delegation attempt records the server's identity beside the reference,
// as Placement.Host carries it for a held session, Adopt is given it, and
// Hosted answers for it while the server is present, so a window the
// survivor opens on its own server places as that subordinate exactly as it
// does under a held session. What the placement cannot tell, where the
// platform reads no start time, is a process later given the survivor's or
// its server's identifier, which Member and Hosted then place as the
// subordinate until the delegation ends; that errs toward the subordinate,
// the side that claims less.
//
// A Survivors is safe for concurrent use.
type Survivors struct {
	mu      sync.Mutex
	entries map[int]survivor
}

// survivor is one adopted entry: the placement, the identity it is held
// to, and the identity of the server hosting its terminal, zero for a
// survivor with none.
type survivor struct {
	placement Placement
	identity  agents.Identity
	host      agents.Identity
}

// NewSurvivors returns an empty set, which places nobody.
func NewSurvivors() *Survivors {
	return &Survivors{entries: map[int]survivor{}}
}

// Adopt derives a subordinate's placement from the reference a delegation
// attempt recorded, as store.DelegationAttempt.SessionRef holds it, and the
// identity of the server hosting its terminal as the attempt recorded it
// beside the reference, zero for a session with none, for a provider whose
// home marker is marker. A reference ParseReference refuses,
// one of another home, or a worker's is refused with an error wrapping
// ErrReference and nothing is adopted. A process the presence read proves
// absent, or replaced by one with another start time, is not adopted and
// reports false: the subordinate is gone, which is the caller's relaunch to
// decide. A process read present is adopted. A reference with no start
// time, or a presence that could not be read, is adopted too, unverified,
// because a peer in that group is then the subordinate's or nobody's and
// the subordinate claims less; the reported error says which. A domain
// already adopted is replaced.
func (s *Survivors) Adopt(marker, text string, host agents.Identity) (Placement, bool, error) {
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
	p := Placement{Domain: ref.Domain, Group: ref.PID, Host: host}
	s.mu.Lock()
	s.forgetLocked(ref.Domain)
	s.entries[ref.PID] = survivor{placement: p, identity: identity, host: host}
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

// Hosted reports the placement of the survivor whose terminal the identity
// hosts, on Subordinates.Hosted's terms: never for the zero identity, and
// read again at every ask like Member, so a server proven absent or
// replaced is forgotten with its survivor and one whose presence cannot be
// read stays a host.
func (s *Survivors) Hosted(host agents.Identity) (Placement, bool) {
	if host == (agents.Identity{}) {
		return Placement{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for group, entry := range s.entries {
		if entry.host != host {
			continue
		}
		presence, err := entry.host.Presence()
		if err == nil && presence != agents.PresencePresent {
			delete(s.entries, group)
			return Placement{}, false
		}
		return entry.placement, true
	}
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
