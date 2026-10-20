package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/route"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
)

// delegate is Delegate on a host, or a fatal failure, with the session
// closed at cleanup.
func delegate(t *testing.T, p session.Host, spec session.SubordinateSpec) *session.Session {
	t.Helper()
	s, err := p.Delegate(context.Background(), spec)
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	t.Cleanup(func() { _ = p.Close(s) })
	return s
}

// subordinateSpec is a Delegate spec for a domain, in a directory of the
// home's that the test makes, as the service makes one.
func subordinateSpec(t *testing.T, h *home.Home, domain string) session.SubordinateSpec {
	t.Helper()
	dir, err := h.Domain(domain)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return session.SubordinateSpec{Domain: domain, Dir: dir, Instructions: "coordinate the domain"}
}

// P14 at the second held set: delegating a domain joins the session's group
// to the provider's Subordinates, naming the domain, and nothing else, for
// exactly the process's lifetime; Workers is untouched by it, so one lookup
// never answers both questions. The reference names the domain and no task,
// round-trips through its own format, and is what Probe answers Live for
// while the process runs and exited once it has ended. A domain the home
// refuses is refused before anything starts.
func TestDelegatingJoinsTheSubordinateSetAndNotTheWorkerSet(t *testing.T) {
	principles.Cite(t, principles.P14)
	sub := newSubject(t)
	p, _ := provider(t, sub.home, workerScript())
	if !p.Subordinates().Empty() {
		t.Fatal("a provider that delegated nothing holds a subordinate")
	}
	if _, err := p.Delegate(context.Background(), session.SubordinateSpec{Domain: "No", Dir: t.TempDir(), Instructions: "x"}); !errors.Is(err, home.ErrDomain) {
		t.Fatalf("a domain the home refuses = %v, want ErrDomain", err)
	}

	s := delegate(t, p, subordinateSpec(t, sub.home, "alpha"))
	ref := s.Reference()
	if !ref.Subordinate() || ref.Domain != "alpha" || ref.Task != "" {
		t.Fatalf("the reference is %+v, want one naming domain alpha and no task", ref)
	}
	text, err := ref.Encode()
	if err != nil || !strings.HasPrefix(text, "subordinate1:") {
		t.Fatalf("the reference encodes as %q (%v), want the subordinate format", text, err)
	}
	if parsed, err := session.ParseReference(text); err != nil || parsed != ref {
		t.Fatalf("ParseReference(%q) = %+v, %v; want %+v", text, parsed, err, ref)
	}
	waitCapture(t, p, s, "worker ready")
	group := ref.PID
	placed, ok := p.Subordinates().Member(group)
	if !ok || placed != (session.Placement{Domain: "alpha", Group: group}) {
		t.Fatalf("Subordinates().Member(%d) = %+v, %v; want domain alpha and no host", group, placed, ok)
	}
	if _, ok := p.Workers().Member(group); ok || !p.Workers().Empty() {
		t.Fatal("the subordinate's group is a member of the worker set")
	}
	if _, ok := p.Subordinates().Hosted(agents.Identity{}); ok {
		t.Fatal("the zero identity hosts a subordinate")
	}
	expect(t, p.Probe(context.Background(), text), session.ReasonHeld)
	if err := p.Send(context.Background(), s, "finish"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitProbe(t, p, text, session.ReasonExited)
	deadline := time.Now().Add(waitFor)
	for {
		if _, ok := p.Subordinates().Member(group); !ok {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("group %d is still a subordinate after its process ended", group)
		}
		time.Sleep(5 * time.Millisecond)
	}

	closed := delegate(t, p, subordinateSpec(t, sub.home, "beta"))
	if _, ok := p.Subordinates().Member(closed.Reference().PID); !ok {
		t.Fatal("the second session's group is not a subordinate")
	}
	if err := p.Close(closed); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, ok := p.Subordinates().Member(closed.Reference().PID); ok {
		t.Fatal("the group of a session Close ended is still a subordinate")
	}
}

// The subordinate's reference format is as closed as the worker's: a
// domain the home refuses in its place, and the two prefixes with a third
// field only the other's rule accepts, are refused rather than read as a
// nearby one; and where the two rules overlap the prefix alone decides the
// owner, which is all the package comment claims.
func TestASubordinateReferenceIsItsOwnClosedFormat(t *testing.T) {
	h, err := home.Open(filepath.Join(t.TempDir(), home.DirName))
	if err != nil {
		t.Fatal(err)
	}
	ref := session.Reference{Home: session.HomeMarker(h), Domain: "web-api", PID: 4242, Start: 77}
	text, err := ref.Encode()
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(text, ":")
	if len(fields) != 5 || fields[0] != "subordinate1" || fields[2] != "web-api" {
		t.Fatalf("encoded as %q", text)
	}
	for name, text := range map[string]string{
		"a domain the home refuses":  strings.Join([]string{fields[0], fields[1], "Web", fields[3], fields[4]}, ":"),
		"a task in a domain's place": strings.Join([]string{"terminal1", fields[1], "web-api", fields[3], fields[4]}, ":"),
		"another format":             strings.Join([]string{"subordinate2", fields[1], fields[2], fields[3], fields[4]}, ":"),
	} {
		if _, err := session.ParseReference(text); !errors.Is(err, session.ErrReference) {
			t.Errorf("%s (%q) answered %v, want ErrReference", name, text, err)
		}
	}

	// The prefix decides the owner where the grammars overlap: a name both
	// home.ValidDomain and home.ValidTaskID accept is a domain under the
	// subordinate prefix and a task under the worker's, never the other.
	const both = "abcdefghij23"
	if home.ValidDomain(both) != nil || home.ValidTaskID(both) != nil {
		t.Fatalf("%q is not accepted by both grammars, so it shows no overlap", both)
	}
	sub, err := session.ParseReference(strings.Join([]string{"subordinate1", fields[1], both, "4242", "77"}, ":"))
	if err != nil || sub.Domain != both || sub.Task != "" || !sub.Subordinate() {
		t.Fatalf("the overlapping name under the subordinate prefix = %+v, %v; want domain %s and no task", sub, err, both)
	}
	worker, err := session.ParseReference(strings.Join([]string{"terminal1", fields[1], both, "4242", "77"}, ":"))
	if err != nil || worker.Task != both || worker.Domain != "" || worker.Subordinate() {
		t.Fatalf("the overlapping name under the worker prefix = %+v, %v; want task %s and no domain", worker, err, both)
	}
}

// A caller holding the subordinate set or a placement reaches no fixer
// session, on route_test.go's terms.
func TestNoRouteFromTheSubordinateSetToAFixerSession(t *testing.T) {
	for _, root := range []reflect.Type{
		reflect.TypeOf(&session.Subordinates{}),
		reflect.TypeOf(session.Placement{}),
		reflect.TypeOf(session.SubordinateSpec{}),
	} {
		if found := route.ToFixerSession(root); len(found) != 0 {
			t.Errorf("%s reaches a fixer session:\n%v", root, found)
		}
	}
	if found := route.ToFixerSession(reflect.TypeOf(agents.Resolution{})); len(found) == 0 {
		t.Fatal("the walk reports no route out of agents.Resolution, so it is not inspecting anything")
	}
}
