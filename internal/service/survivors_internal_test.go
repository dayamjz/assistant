//go:build linux || darwin

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
)

// A survivor is placed along the chain from the record and the kernel's
// presence read alone (P14): a running process whose reference a
// delegation attempt recorded, adopted into session.Survivors by a service
// holding no session for it, places its own process and a process it
// started as its domain's subordinate, asked after the worker sets; and the
// same walk over the survivors forgotten, which is the production forced
// off, places neither as the subordinate, so a placement that stopped
// asking the survivors would fail this rather than pass it on nothing.
func TestASurvivorIsPlacedAlongTheChainUntilItIsForgotten(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	worker, dir := startedWorker(t)
	group := worker.Group()
	identity, found, err := agents.Identify(group)
	if err != nil || !found {
		t.Fatalf("the worker's identity: %+v, %v, %v", identity, found, err)
	}
	marker := "0123456789abcdef0123456789abcdef"
	text, err := session.Reference{Home: marker, Domain: "alpha", PID: group, Start: identity.Start}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// A process the survivor started, in a group it does not lead, as its
	// commands run. It is in no group the worker's end reaches, so it is a
	// held process, which ends with this test on every path (heldShell).
	child, _ := heldShell(t, worker, dir)

	survivors := session.NewSurvivors()
	if _, adopted, err := survivors.Adopt(marker, text, agents.Identity{}); err != nil || !adopted {
		t.Fatalf("Adopt = %v, %v; want the running survivor adopted", adopted, err)
	}
	for _, pid := range []int{group, child} {
		p, err := placeAlongChain(pid, readLineage, noWorkers{}, survivors)
		if err != nil || p.kind != placedSubordinate || p.subordinate.Domain != "alpha" || p.subordinate.Group != group {
			t.Fatalf("placeAlongChain(%d) = %+v, %v; want the survivor of alpha leading group %d", pid, p, err, group)
		}
	}
	if _, forgotten := survivors.Forget("alpha"); !forgotten {
		t.Fatal("the survivor was not forgotten")
	}
	for _, pid := range []int{group, child} {
		p, err := placeAlongChain(pid, readLineage, noWorkers{}, survivors)
		if err != nil || p.kind == placedSubordinate || p.kind == placedWorker {
			t.Fatalf("with the survivors forgotten placeAlongChain(%d) = %+v, %v; want neither a worker nor a subordinate", pid, p, err)
		}
	}
}

// noWorkers is a worker placer holding nobody, which the zero oneWorkerSet
// is not: on Linux the chain reaches the kernel's processes, whose group
// reads zero, and a set answering for group zero would place them as a
// worker.
type noWorkers struct{}

func (noWorkers) Member(int) (session.Membership, bool) { return session.Membership{}, false }
func (noWorkers) Hosted(agents.Identity) (session.Membership, bool) {
	return session.Membership{}, false
}

// atoiFile is waitFile read as a process identifier.
func atoiFile(t *testing.T, path string) int {
	t.Helper()
	var pid int
	for _, r := range waitFile(t, path) {
		if r < '0' || r > '9' {
			t.Fatalf("%s holds %q, want a process identifier", path, waitFile(t, path))
		}
		pid = pid*10 + int(r-'0')
	}
	return pid
}

// gatedHost is a session.Host whose Delegate waits to be released once it
// has been entered, over a real provider, so a test can land a domain.end
// between the attempt's claim and the session's start; it counts the
// sessions it started and closed.
type gatedHost struct {
	session.Host
	entered, release chan struct{}
	started, closed  int
}

func (g *gatedHost) Delegate(ctx context.Context, spec session.SubordinateSpec) (*session.Session, error) {
	g.started++
	close(g.entered)
	<-g.release
	return g.Host.Delegate(ctx, spec)
}

func (g *gatedHost) Close(s *session.Session) error {
	g.closed++
	return g.Host.Close(s)
}

// A domain.end that lands while the subordinate's session is starting
// leaves no session nobody supervises: the end's record precedes the
// session's, so the record is refused in the recording transaction and the
// launch ends the session it started, holding none and recording none. A
// delegation ended before the launch reads it starts nothing at all. The
// positive control is the same launch over a delegation that stands, which
// holds and records the session, and the end then finds it to end.
func TestALaunchThatLandsAfterTheDelegationEndedIsEndedWithIt(t *testing.T) {
	t.Parallel()
	h, records := loopHomeConfigured(t, `{"supervision_beacon_interval": "1s"}`)
	l := openLoop(t, h, records, newScriptedTerminals(), nil)
	defer func() { _ = l.service.Close() }()
	ctx := t.Context()
	agent := workerStandIn(t)
	pipe := session.NewPipe(h, agent.Runner())
	host := &gatedHost{Host: pipe, entered: make(chan struct{}), release: make(chan struct{})}
	l.service.mu.Lock()
	l.service.built = &driver{sessions: host}
	l.service.mu.Unlock()

	delegate := func(domain string) store.Delegation {
		t.Helper()
		d, err := l.records(t).Delegate(ctx, store.DelegationRequest{Domain: domain, Delivery: config.DeliveryGated})
		if err != nil {
			t.Fatalf("delegating %s: %v", domain, err)
		}
		if _, err := l.records(t).BeginDelegationAttempt(ctx, d.ID, 0); err != nil {
			t.Fatalf("claiming the attempt of %s: %v", domain, err)
		}
		dir, err := h.Domain(domain)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.service.writeDomainInstructions(dir, domain, "Coordinate "+domain+".\n"); err != nil {
			t.Fatal(err)
		}
		return d
	}

	// Ended while starting.
	alpha := delegate("alpha")
	errs := make(chan error, 1)
	go func() { errs <- l.service.launchSubordinate(ctx, alpha, 1) }()
	<-host.entered
	if _, _, err := l.records(t).EndDelegation(ctx, alpha.ID, store.EndedByCoordinator(), store.DelegationEndClosure{}); err != nil {
		t.Fatal(err)
	}
	if l.service.endSubordinate("alpha", alpha.ID) {
		t.Fatal("the end found a session to end before one had started")
	}
	close(host.release)
	if err := <-errs; !errors.Is(err, store.ErrDelegationEnded) {
		t.Fatalf("the launch = %v, want it refused with ErrDelegationEnded", err)
	}
	if _, _, held := l.service.subordinateSessionOf("alpha"); held {
		t.Fatal("the service holds a session for the ended delegation")
	}
	if host.started != 1 || host.closed != 1 {
		t.Fatalf("the host started %d sessions and closed %d, want one and one", host.started, host.closed)
	}
	if attempts, err := l.records(t).DelegationAttempts(ctx, alpha.ID); err != nil || len(attempts) != 1 || attempts[0].SessionRef.IsKnown() {
		t.Fatalf("the attempts are %+v (%v), want one recording no session", attempts, err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for !pipe.Subordinates().Empty() {
		if !time.Now().Before(deadline) {
			t.Fatal("the session the launch ended is still in the subordinate set")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Ended before starting: nothing is started.
	gamma := delegate("gamma")
	if _, _, err := l.records(t).EndDelegation(ctx, gamma.ID, store.EndedByCoordinator(), store.DelegationEndClosure{}); err != nil {
		t.Fatal(err)
	}
	if err := l.service.launchSubordinate(ctx, gamma, 1); !errors.Is(err, store.ErrDelegationEnded) || host.started != 1 {
		t.Fatalf("a launch after the end = %v with %d sessions started, want ErrDelegationEnded and none started", err, host.started)
	}

	// The positive control: a delegation that stands.
	host.entered, host.release = make(chan struct{}), make(chan struct{})
	close(host.release)
	beta := delegate("beta")
	if err := l.service.launchSubordinate(ctx, beta, 1); err != nil {
		t.Fatalf("the launch over a standing delegation: %v", err)
	}
	_, sub, held := l.service.subordinateSessionOf("beta")
	if !held {
		t.Fatal("the service holds no session for the standing delegation")
	}
	ref, err := sub.Reference().Encode()
	if err != nil {
		t.Fatal(err)
	}
	if attempts, err := l.records(t).DelegationAttempts(ctx, beta.ID); err != nil || len(attempts) != 1 || attempts[0].SessionRef.Or("") != ref {
		t.Fatalf("beta's attempts are %+v (%v), want the one recording %s", attempts, err, ref)
	}
	if _, _, err := l.records(t).EndDelegation(ctx, beta.ID, store.EndedByCoordinator(), store.DelegationEndClosure{}); err != nil {
		t.Fatal(err)
	}
	if !l.service.endSubordinate("beta", beta.ID) || host.closed != 2 {
		t.Fatalf("the end after the launch found nothing to end (closed %d)", host.closed)
	}
}

// firstGatedHost is gatedHost for the first Delegate alone: the first call
// waits to be released once it has been entered, and every later one
// starts at once, so a test can hold one launch while another runs.
type firstGatedHost struct {
	session.Host
	entered, release chan struct{}
	mu               sync.Mutex
	calls            int
}

func (g *firstGatedHost) Delegate(ctx context.Context, spec session.SubordinateSpec) (*session.Session, error) {
	g.mu.Lock()
	g.calls++
	first := g.calls == 1
	g.mu.Unlock()
	if first {
		close(g.entered)
		<-g.release
	}
	return g.Host.Delegate(ctx, spec)
}

// A launch paused across its delegation's end and a new delegation of the
// same domain can neither replace nor erase the newer delegation's
// session: the old launch is held inside its start, the delegation is
// ended, the domain is delegated again and the new launch starts and holds
// its session, and the old launch is then released. The old launch is
// refused with ErrDelegationEnded and ends its own session, and the domain
// still holds the new delegation's session, which the new delegation's end
// then finds and ends. A map keyed by the domain alone, written and
// cleared by whichever launch came last, fails this by holding nothing.
func TestAnOldLaunchLeavesANewerDelegationsSessionHeld(t *testing.T) {
	t.Parallel()
	h, records := loopHomeConfigured(t, `{"supervision_beacon_interval": "1s"}`)
	l := openLoop(t, h, records, newScriptedTerminals(), nil)
	defer func() { _ = l.service.Close() }()
	ctx := t.Context()
	agent := workerStandIn(t)
	pipe := session.NewPipe(h, agent.Runner())
	host := &firstGatedHost{Host: pipe, entered: make(chan struct{}), release: make(chan struct{})}
	l.service.mu.Lock()
	l.service.built = &driver{sessions: host}
	l.service.mu.Unlock()
	dir, err := h.Domain("alpha")
	if err != nil {
		t.Fatal(err)
	}
	delegate := func() store.Delegation {
		t.Helper()
		latest, found, err := l.records(t).Delegation(ctx, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		var expected int64
		if found {
			expected = latest.ID
		}
		d, err := l.records(t).Delegate(ctx, store.DelegationRequest{Domain: "alpha", Delivery: config.DeliveryGated, Expected: expected})
		if err != nil {
			t.Fatalf("delegating alpha: %v", err)
		}
		if _, err := l.records(t).BeginDelegationAttempt(ctx, d.ID, 0); err != nil {
			t.Fatalf("claiming the attempt: %v", err)
		}
		if _, err := l.service.writeDomainInstructions(dir, "alpha", "Coordinate alpha.\n"); err != nil {
			t.Fatal(err)
		}
		return d
	}

	old := delegate()
	errs := make(chan error, 1)
	go func() { errs <- l.service.launchSubordinate(ctx, old, 1) }()
	<-host.entered
	if _, _, err := l.records(t).EndDelegation(ctx, old.ID, store.EndedByCoordinator(), store.DelegationEndClosure{}); err != nil {
		t.Fatal(err)
	}
	if l.service.endSubordinate("alpha", old.ID) {
		t.Fatal("the end found a session to end before the old launch had one")
	}
	newer := delegate()
	if err := l.service.launchSubordinate(ctx, newer, 1); err != nil {
		t.Fatalf("the newer delegation's launch: %v", err)
	}
	_, held, ok := l.service.subordinateSessionOf("alpha")
	if !ok {
		t.Fatal("the newer delegation's session is not held")
	}
	close(host.release)
	if err := <-errs; !errors.Is(err, store.ErrDelegationEnded) {
		t.Fatalf("the old launch = %v, want ErrDelegationEnded", err)
	}
	_, still, ok := l.service.subordinateSessionOf("alpha")
	if !ok || still != held {
		t.Fatalf("after the old launch the domain holds %v (%v), want the newer delegation's session %v", still, ok, held)
	}
	ref, err := held.Reference().Encode()
	if err != nil {
		t.Fatal(err)
	}
	if attempts, err := l.records(t).DelegationAttempts(ctx, newer.ID); err != nil || len(attempts) != 1 || attempts[0].SessionRef.Or("") != ref {
		t.Fatalf("the newer delegation's attempts are %+v (%v), want the one recording %s", attempts, err, ref)
	}
	if _, _, err := l.records(t).EndDelegation(ctx, newer.ID, store.EndedByCoordinator(), store.DelegationEndClosure{}); err != nil {
		t.Fatal(err)
	}
	if !l.service.endSubordinate("alpha", newer.ID) {
		t.Fatal("the newer delegation's end found no session to end")
	}
	deadline := time.Now().Add(30 * time.Second)
	for !pipe.Subordinates().Empty() {
		if !time.Now().Before(deadline) {
			t.Fatal("a subordinate session outlived both ends")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
