package store

import "fmt"

// Sender says who sent a message to a task's worker, PRD section 8's "who
// sent it, derived from the surface". It is a closed set on Acknowledger's
// terms, and for Resolver's reason: a struct with one unexported field and no
// conversion into it, so outside this package the only values are the one
// the function below returns and the zero, and nothing decoded from a wire,
// a file or an agent's output is one.
//
// There is no value meaning a person sent it, and that is the design: no
// surface in this repository witnesses a person, and sending is the
// coordinator's, PRD section 9's coordinator-only service method. The record
// understates who sent rather than overstating it.
//
// Nothing reads a Sender to decide anything. AppendMessage records it and
// treats every member identically.
type Sender struct{ name string }

// SentByCoordinator is a message the coordinator sent. Its one producer is
// internal/ipc's Request.MessageSender, which derives it from the surface the
// way Request.HoldResolver derives a resolver; no frame carries it, and the
// method that records it refuses a task's worker.
func SentByCoordinator() Sender { return Sender{name: "coordinator"} }

// String renders who sent, or "nobody" for the zero Sender.
func (s Sender) String() string {
	if s.name == "" {
		return "nobody"
	}
	return s.name
}

// senders is the closed set, and the only place it is written down.
func senders() []Sender { return []Sender{SentByCoordinator()} }

// senderText returns what the column holds for s: ErrNoSender for the zero
// Sender, and ErrUnknownSender, unreachable from outside this package, for
// anything else outside the set.
func senderText(s Sender) (string, error) {
	if s == (Sender{}) {
		return "", ErrNoSender
	}
	for _, known := range senders() {
		if s == known {
			return s.name, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownSender, s.name)
}

// senderValue turns a stored column back into a Sender, refusing text
// outside the set rather than fabricating one.
func senderValue(text, column string) (Sender, error) {
	for _, known := range senders() {
		if text == known.name {
			return known, nil
		}
	}
	return Sender{}, fmt.Errorf("store: column %s: %w: %q", column, ErrUnknownSender, text)
}
