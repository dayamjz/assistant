package ipc

import (
	"fmt"
	"net"
)

// MarkerVar is the environment variable a process running inside a validation
// stage carries. This package owns the name.
//
// What it is for is worth being exact about, because getting it wrong is a
// security hole rather than a bug. It is evidence for a diagnostic: it lets a
// message say "you appear to be running inside a stage" instead of only "this
// call is refused". It is never authorization. A caller sets its own
// environment, so anything derived from it is a claim by the party being
// checked. Authority comes from Credentials, which the kernel attributes to the
// process on the other end of the connection and the process cannot state
// about itself.
const MarkerVar = "ASSISTANT_STAGE"

// Marker is what a peer said about its own environment. It is unauthenticated
// by construction, and the type is separate from Credentials so that a decision
// written against one cannot be fed the other by mistake.
type Marker string

// Credentials is what the kernel says about the process on the other end of a
// connection. It is the authoritative identity, and the only identity a
// decision may rest on.
type Credentials struct {
	// PID is the peer process identifier at the time the connection was
	// accepted. A process identifier is reusable after the process exits, so a
	// decision made on one is a decision about the peer as it was when it
	// connected, which is the only thing a connection can be about.
	PID int
	// UID is the peer's effective user identifier.
	UID int
	// GID is the peer's primary group identifier.
	GID int
}

// String renders the credentials for a diagnostic.
func (c Credentials) String() string {
	return fmt.Sprintf("pid %d uid %d gid %d", c.PID, c.UID, c.GID)
}

// Peer is who is at the other end of a connection.
//
// The identification either succeeded or it did not, and this type makes a
// caller face that: there is no field to read the credentials out of, only
// Credentials, which returns the refusal when the kernel could not be asked.
// A caller reaching for authority therefore has an error to handle rather than
// a zero value that reads like a valid identity.
type Peer struct {
	creds  Credentials
	err    error
	marker Marker
}

// Credentials returns what the kernel attributes to the peer, or a refusal
// wrapping ErrUnidentifiedPeer when it could not be asked: a transport that is
// not a local socket, a platform with no answer for one, or a socket the
// kernel would not answer about.
func (p Peer) Credentials() (Credentials, error) {
	if p.err != nil {
		return Credentials{}, p.err
	}
	return p.creds, nil
}

// Identified reports whether the peer was identified. It is for a diagnostic
// that wants to say so without an error to handle; a decision uses
// Credentials, which cannot be read without handling the refusal.
func (p Peer) Identified() bool { return p.err == nil }

// Marker returns what the peer said about its own environment, which is
// evidence and never authorization. See MarkerVar.
func (p Peer) Marker() Marker { return p.marker }

// withMarker returns the peer with the marker a request carried. A marker is
// per request rather than per connection because it is the claim a caller
// attached to a call, and nothing here treats it as more than that.
func (p Peer) withMarker(m Marker) Peer {
	p.marker = m
	return p
}

// Identify asks the kernel who is on the other end of conn.
//
// It never returns an error separate from the peer: an unidentified peer is a
// Peer that refuses to produce credentials, so a caller that ignores
// identification at accept time still cannot make a decision without handling
// the refusal.
func Identify(conn net.Conn) Peer {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{err: fmt.Errorf("%w: %T is not a local socket", ErrUnidentifiedPeer, conn)}
	}
	creds, err := peerCredentials(uc)
	if err != nil {
		return Peer{err: fmt.Errorf("%w: %w", ErrUnidentifiedPeer, err)}
	}
	return Peer{creds: creds}
}

// LocalMarker reads this process's own marker from the environment, for a
// client to attach to its requests. It is evidence about the caller that the
// caller itself supplies, which is all a marker ever is.
func LocalMarker(lookup func(string) string) Marker {
	if lookup == nil {
		return ""
	}
	return Marker(lookup(MarkerVar))
}

// rawControl runs f on the connection's file descriptor.
func rawControl(uc *net.UnixConn, f func(fd uintptr)) error {
	raw, err := uc.SyscallConn()
	if err != nil {
		return fmt.Errorf("connection has no file descriptor: %w", err)
	}
	if err := raw.Control(f); err != nil {
		return fmt.Errorf("connection file descriptor is unusable: %w", err)
	}
	return nil
}
