package session_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
)

// A survivor is placed from the reference a delegation attempt recorded
// and the kernel's presence read, and from nothing the process states
// (P14): a live subordinate's reference, adopted by a set holding no
// handle, places its group as its domain's subordinate; a worker's
// reference and another home's are refused; a process that has ended is
// not adopted, and one adopted while it ran stops being a member once it
// has ended, read at the ask; and Forget hands back the identity the
// survivor was held to. The counterfeit is the same group asked of a set
// that forgot it, which is the production forced off, so a walk that
// stopped asking the survivors would place the survivor as nobody.
func TestASurvivorIsPlacedFromItsRecordedReferenceWhileItsProcessIsPresent(t *testing.T) {
	principles.Cite(t, principles.P14)
	sub := newSubject(t)
	p, _ := provider(t, sub.home, workerScript())
	s := delegate(t, p, subordinateSpec(t, sub.home, "alpha"))
	waitCapture(t, p, s, "worker ready")
	ref := s.Reference()
	text, err := ref.Encode()
	if err != nil {
		t.Fatal(err)
	}
	marker := session.HomeMarker(sub.home)

	survivors := session.NewSurvivors()
	if !survivors.Empty() {
		t.Fatal("a new set holds a survivor")
	}
	if _, ok := survivors.Member(ref.PID); ok {
		t.Fatal("a set that adopted nothing places the subordinate's group")
	}
	placed, adopted, err := survivors.Adopt(marker, text, agents.Identity{})
	if err != nil || !adopted || placed != (session.Placement{Domain: "alpha", Group: ref.PID}) {
		t.Fatalf("Adopt(%q) = %+v, %v, %v; want the domain's placement adopted with no error", text, placed, adopted, err)
	}
	if got, ok := survivors.Member(ref.PID); !ok || got != placed {
		t.Fatalf("Member(%d) = %+v, %v after the adoption; want the placement", ref.PID, got, ok)
	}
	if _, ok := survivors.Hosted(agents.Identity{PID: ref.PID, Start: ref.Start}); ok {
		t.Fatal("a survivor's own identity hosts it: it was adopted with no host")
	}
	if _, ok := survivors.Hosted(agents.Identity{}); ok {
		t.Fatal("the zero identity hosts a survivor")
	}
	// A survivor adopted with the identity of the server hosting its
	// terminal, as the attempt records one: this process stands in for the
	// server, since what Hosted reads of it is its presence alone.
	server, found, err := agents.Identify(os.Getpid())
	if err != nil || !found {
		t.Fatalf("this process's identity: %+v, %v, %v", server, found, err)
	}
	if _, adopted, err := survivors.Adopt(marker, text, server); err != nil || !adopted {
		t.Fatalf("adopting with a host = %v, %v", adopted, err)
	}
	if got, ok := survivors.Hosted(server); !ok || got != (session.Placement{Domain: "alpha", Group: ref.PID, Host: server}) {
		t.Fatalf("Hosted(the server) = %+v, %v; want the survivor's placement carrying the host", got, ok)
	}
	if _, ok := survivors.Hosted(agents.Identity{PID: server.PID, Start: server.Start + 1}); ok {
		t.Fatal("an identity with another start time hosts the survivor")
	}
	if _, ok := survivors.Member(ref.PID); !ok {
		t.Fatal("the survivor adopted with a host is not placed by its group")
	}
	if survivors.Empty() {
		t.Fatal("the set reads empty while it holds the survivor")
	}

	worker := session.Reference{Home: marker, Task: "abcdefghij23", PID: ref.PID, Start: ref.Start}
	workerText, err := worker.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, adopted, err := survivors.Adopt(marker, workerText, agents.Identity{}); !errors.Is(err, session.ErrReference) || adopted {
		t.Fatalf("adopting a worker's reference = %v, %v; want ErrReference and nothing adopted", adopted, err)
	}
	if _, adopted, err := survivors.Adopt("0123456789abcdef0123456789abcdef", text, agents.Identity{}); !errors.Is(err, session.ErrReference) || adopted {
		t.Fatalf("adopting another home's reference = %v, %v; want ErrReference and nothing adopted", adopted, err)
	}
	if _, adopted, err := survivors.Adopt(marker, "not a reference", agents.Identity{}); !errors.Is(err, session.ErrReference) || adopted {
		t.Fatalf("adopting a malformed reference = %v, %v; want ErrReference and nothing adopted", adopted, err)
	}

	// The production forced off: the group and the host asked of a set
	// that forgot it.
	identity, forgotten := survivors.Forget("alpha")
	if !forgotten || identity != (agents.Identity{PID: ref.PID, Start: ref.Start}) {
		t.Fatalf("Forget(alpha) = %+v, %v; want the identity the survivor was held to", identity, forgotten)
	}
	if _, ok := survivors.Member(ref.PID); ok || !survivors.Empty() {
		t.Fatal("a forgotten survivor is still placed")
	}
	if _, ok := survivors.Hosted(server); ok {
		t.Fatal("a forgotten survivor is still placed by its host")
	}
	if _, forgotten := survivors.Forget("alpha"); forgotten {
		t.Fatal("a domain with no survivor was forgotten twice")
	}

	// Adopted again while it runs, then ended: the membership ends with
	// the process, read at the ask.
	if _, adopted, err := survivors.Adopt(marker, text, agents.Identity{}); err != nil || !adopted {
		t.Fatalf("adopting the live survivor again = %v, %v", adopted, err)
	}
	if err := p.Send(context.Background(), s, "finish"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitProbe(t, p, text, session.ReasonExited)
	deadline := time.Now().Add(waitFor)
	for {
		if _, ok := survivors.Member(ref.PID); !ok {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("group %d is still a survivor after its process ended", ref.PID)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, adopted, err := survivors.Adopt(marker, text, agents.Identity{}); err != nil || adopted {
		t.Fatalf("adopting the ended subordinate's reference = %v, %v; want not adopted and no error", adopted, err)
	}
}

// Encode refuses a reference naming both a task and a domain and one naming
// neither, rather than choosing an owner, and String renders such a
// reference as a text ParseReference refuses, so neither can reach a record
// as the identity of a session.
func TestAReferenceWithTwoOwnersOrNoneIsRefusedRatherThanEncoded(t *testing.T) {
	marker := "0123456789abcdef0123456789abcdef"
	for name, ref := range map[string]session.Reference{
		"both a task and a domain": {Home: marker, Task: "abcdefghij23", Domain: "alpha", PID: 7, Start: 1},
		"neither":                  {Home: marker, PID: 7, Start: 1},
	} {
		text, err := ref.Encode()
		if !errors.Is(err, session.ErrReference) || text != "" {
			t.Errorf("%s: Encode = %q, %v; want ErrReference and no text", name, text, err)
		}
		if _, err := session.ParseReference(ref.String()); !errors.Is(err, session.ErrReference) {
			t.Errorf("%s: String renders %q, which ParseReference reads", name, ref.String())
		}
	}
	for name, ref := range map[string]session.Reference{
		"a task":   {Home: marker, Task: "abcdefghij23", PID: 7, Start: 1},
		"a domain": {Home: marker, Domain: "alpha", PID: 7, Start: 1},
	} {
		text, err := ref.Encode()
		if err != nil || ref.String() != text {
			t.Errorf("%s: Encode = %q, %v and String = %q; want one text and no error", name, text, err, ref.String())
		}
		if parsed, err := session.ParseReference(text); err != nil || parsed != ref {
			t.Errorf("%s: ParseReference(%q) = %+v, %v; want the reference back", name, text, parsed, err)
		}
	}
}
