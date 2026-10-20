package store

import (
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/home"
)

// Sender says who sent a message to a task's worker, PRD section 8's "who
// sent it, derived from the surface". It is a closed set on Acknowledger's
// terms, and for Resolver's reason: a struct with unexported fields and no
// conversion into it, so outside this package the only values are the ones
// the functions below return and the zero, and nothing decoded from a wire,
// a file or an agent's output is one.
//
// There is no value meaning a person sent it, and that is the design: no
// surface in this repository witnesses a person, and sending is a
// coordinator's, PRD section 9's coordinator-only service method, which a
// subordinate coordinator holds over its own domain. The record understates
// who sent rather than overstating it.
//
// Nothing reads a Sender to decide anything. AppendMessage records it and
// treats every member identically.
type Sender struct {
	name string
	// domain is set for the subordinate member alone.
	domain string
}

// SentByCoordinator is a message the coordinator sent. Its one producer is
// internal/ipc's Request.MessageSender, which derives it from the surface the
// way Request.HoldResolver derives a resolver; no frame carries it, and the
// method that records it refuses a task's worker.
func SentByCoordinator() Sender { return Sender{name: "coordinator"} }

// SentBySubordinate is a message the subordinate coordinator of the named
// domain sent. Its one producer is internal/service, which substitutes it
// for the coordinator's member when the kernel's credentials place the
// caller as that domain's subordinate, on AcknowledgedBySubordinate's terms.
func SentBySubordinate(domain string) Sender { return Sender{name: subordinateMember, domain: domain} }

// Domain is the domain of the subordinate member, and empty for every other
// member.
func (s Sender) Domain() string { return s.domain }

// String renders who sent, or "nobody" for the zero Sender.
func (s Sender) String() string {
	switch {
	case s.name == "":
		return "nobody"
	case s.domain != "":
		return s.name + " of domain " + s.domain
	}
	return s.name
}

// senderText returns what the column holds for s: ErrNoSender for the zero
// Sender, and ErrUnknownSender for anything else outside the set, which from
// outside this package is only a subordinate member naming a domain
// home.ValidDomain refuses.
func senderText(s Sender) (string, error) {
	if s == (Sender{}) {
		return "", ErrNoSender
	}
	if s.name == subordinateMember {
		if err := home.ValidDomain(s.domain); err != nil {
			return "", fmt.Errorf("%w: %w", ErrUnknownSender, err)
		}
		return subordinateMember + ":" + s.domain, nil
	}
	if s == SentByCoordinator() {
		return s.name, nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownSender, s.name)
}

// senderValue turns a stored column back into a Sender, refusing text
// outside the set rather than fabricating one.
func senderValue(text, column string) (Sender, error) {
	if domain, found := strings.CutPrefix(text, subordinateMember+":"); found {
		if err := home.ValidDomain(domain); err != nil {
			return Sender{}, fmt.Errorf("store: column %s: %w: %w", column, ErrUnknownSender, err)
		}
		return SentBySubordinate(domain), nil
	}
	if text == SentByCoordinator().name {
		return SentByCoordinator(), nil
	}
	return Sender{}, fmt.Errorf("store: column %s: %w: %q", column, ErrUnknownSender, text)
}
