package ipc

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dayamjz/assistant/internal/store"
)

// seamClaims are the ways a caller can compose a request that reaches
// Request.HoldResolver: the parameter body it wrote, and the marker it attached
// to the call, which the server carries on the request's peer. The marker is
// the one claim here that is not part of the body, and it is why this test sits
// inside the package: Peer's fields and withMarker are unexported, so an
// external test cannot build a request that carries one.
var seamClaims = []struct {
	name   string
	marker Marker
	params string
}{
	{"nothing claimed", "", `{"hold":"k","action":"approve"}`},
	{"a resolver in the body", "", `{"resolver":"person","resolved_by":"person","by":"person"}`},
	{"a person in the marker", "person", `{"hold":"k"}`},
	{"a person in both", "person", `{"resolver":"person","human":true}`},
	{"an empty body", "", `{}`},
}

// seamPeers are the two ends of what a connection can establish about who is
// calling. Both are here because the derivation is meant to be independent of
// the peer as well as of the body, and because the socket tests that drive this
// property over a real connection skip where the kernel names no peer.
func seamPeers() []struct {
	name string
	peer Peer
} {
	return []struct {
		name string
		peer Peer
	}{
		{"an identified peer", Peer{creds: Credentials{PID: 1, UID: 0, GID: 0}}},
		{"an unidentified peer", Peer{err: fmt.Errorf("%w: no answer for this transport", ErrUnidentifiedPeer)}},
	}
}

// The derivation reads nothing out of the request, which is what makes the
// answer independent of anything a caller composes. This is the same property
// the socket tests establish end to end, checked at the seam itself so a future
// field on Request cannot quietly reach it, and it holds on every platform
// including one where no peer can be identified at all.
func TestHoldResolverAnswersTheSameForEveryRequest(t *testing.T) {
	want := store.ResolvedByMachineInterface()
	person := store.ResolvedByPerson()

	for _, spec := range Methods() {
		for _, claim := range seamClaims {
			for _, p := range seamPeers() {
				req := Request{
					Method: spec.Method,
					Params: json.RawMessage(claim.params),
					Peer:   p.peer.withMarker(claim.marker),
				}
				// The marker really is on the request, so this
				// dimension is exercised rather than composed and
				// dropped.
				if got := req.Peer.Marker(); got != claim.marker {
					t.Fatalf("%s with %s: the request carries marker %q, want %q", spec.Method, claim.name, got, claim.marker)
				}
				got := req.HoldResolver()
				if got == person {
					t.Fatalf("%s with %s over %s answered that a person decided", spec.Method, claim.name, p.name)
				}
				if got != want {
					t.Fatalf("%s with %s over %s answered %s, want %s", spec.Method, claim.name, p.name, got, want)
				}
			}
		}
	}
}
