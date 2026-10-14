package session_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Pipe is a Provider, which is the one assertion the conformance suite below
// rests on: every operation it drives is driven through the interface.
var _ session.Provider = (*session.Pipe)(nil)

// Create starts the worker in the proven copy, Capture sees what it printed,
// Send reaches it and Capture sees the reply, and while all that works the
// probe answers Live through the handle. The reply is read off the real
// adapter's capture of a real process, and the capture is checked not to be
// an echo of the line sent.
func TestCreateCaptureSendAndALiveProbe(t *testing.T) {
	sub := newSubject(t)
	p, agent := provider(t, sub.home, workerScript())
	var provider session.Provider = p

	s := create(t, p, sub.spec("implement the thing"))
	ref := s.Reference()
	if ref.Task != sub.id || ref.Home != session.HomeMarker(sub.home) || ref.PID <= 0 {
		t.Fatalf("the session's reference is %+v, want this home, task %s and a process", ref, sub.id)
	}
	waitCapture(t, provider, s, "worker ready")
	expect(t, provider.Probe(context.Background(), ref.String()), session.ReasonHeld)

	if err := provider.Send(context.Background(), s, "status?"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := waitCapture(t, provider, s, "still working on it")
	if strings.Contains(got.Text, "status?") {
		t.Errorf("the capture carries the line sent, so this would pass on an echo:\n%s", got.Text)
	}
	if got.Digest != session.Digest(got.Text) || got.Dropped != 0 {
		t.Errorf("capture is %+v, want the digest of its text and nothing dropped", got)
	}
	// The worker ran where the proof says, which the stand-in recorded off
	// its own working directory.
	if calls := agent.Calls(); len(calls) != 1 || !calls[0].Worker || calls[0].Dir != sub.isolation.Copy {
		t.Errorf("the stand-in recorded %s, want one worker in %s", calls, sub.isolation.Copy)
	}
}

// A worker that finishes on its own probes Dead through the handle, and a
// provider holding no handle to it finds no such process and answers Dead
// by identity. Neither answer is Live.
func TestAWorkerThatExitedProbesDead(t *testing.T) {
	sub := newSubject(t)
	p, _ := provider(t, sub.home, workerScript())
	s := create(t, p, sub.spec("begin"))
	ref := s.Reference().String()
	waitCapture(t, p, s, "worker ready")

	if err := p.Send(context.Background(), s, "finish"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	waitCapture(t, p, s, "all done")
	expect(t, waitProbe(t, p, ref, session.ReasonExited), session.ReasonExited)

	// The identity path: the process has been reaped, so nothing has its
	// identifier, or something else does and started at another time.
	restarted := session.NewPipe(sub.home, p2runner(t))
	got := restarted.Probe(context.Background(), ref)
	if got.Reason != session.ReasonAbsent && got.Reason != session.ReasonReplaced {
		t.Fatalf("a provider holding no handle answered %+v for an exited process, want absent or replaced", got)
	}
	if got.Liveness != taskstate.Dead {
		t.Fatalf("an exited process read %s, want Dead", got.Liveness)
	}
}

// A simulated restart: a second provider over the same home holds no handle
// to the first provider's live worker. From the identity alone it answers
// Unobserved, never Live, while the process is present, and Dead once it is
// gone.
func TestAfterARestartAnIdentityAloneIsNeverLive(t *testing.T) {
	sub := newSubject(t)
	p, _ := provider(t, sub.home, workerScript())
	s := create(t, p, sub.spec("begin"))
	ref := s.Reference().String()
	waitCapture(t, p, s, "worker ready")

	restarted := session.NewPipe(sub.home, p2runner(t))
	expect(t, restarted.Probe(context.Background(), ref), session.ReasonUnheld)
	if _, err := restarted.Capture(s); !errors.Is(err, session.ErrNotHeld) {
		t.Errorf("the restarted provider captured another provider's session: %v", err)
	}
	if err := restarted.Send(context.Background(), s, "status?"); !errors.Is(err, session.ErrNotHeld) {
		t.Errorf("the restarted provider sent on another provider's session: %v", err)
	}
	if err := restarted.Close(s); !errors.Is(err, session.ErrNotHeld) {
		t.Errorf("the restarted provider closed another provider's session: %v", err)
	}

	if err := p.Close(s); err != nil {
		t.Fatalf("Close: %v", err)
	}
	got := waitProbe(t, restarted, ref, session.ReasonAbsent)
	expect(t, got, session.ReasonAbsent)
	// The provider that held it answers by identity now too, and a second
	// Close is refused rather than repeated.
	expect(t, p.Probe(context.Background(), ref), session.ReasonAbsent)
	if err := p.Close(s); !errors.Is(err, session.ErrNotHeld) {
		t.Errorf("a second Close answered %v, want ErrNotHeld", err)
	}
}

// A reference from another home, one that cannot be decoded, and one that
// recorded no start time all answer Unobserved, with a reason naming which,
// and never Dead: this home never relaunches over a session it cannot
// answer for. The second home has a task with the same identifier.
func TestAForeignOrUnreadableReferenceIsUnobserved(t *testing.T) {
	sub := newSubject(t)
	p, _ := provider(t, sub.home, workerScript())
	s := create(t, p, sub.spec("begin"))
	waitCapture(t, p, s, "worker ready")

	other, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	theirs := s.Reference()
	theirs.Home = session.HomeMarker(other)
	if theirs.Home == s.Reference().Home {
		t.Fatal("two homes share a marker")
	}
	// Same task identifier, same process, other home: the live process is
	// not evidence here, because the reference is not this home's.
	expect(t, p.Probe(context.Background(), theirs.String()), session.ReasonForeignHome)
	elsewhere := session.NewPipe(other, p2runner(t))
	expect(t, elsewhere.Probe(context.Background(), s.Reference().String()), session.ReasonForeignHome)

	expect(t, p.Probe(context.Background(), "not a reference"), session.ReasonMalformed)
	expect(t, p.Probe(context.Background(), ""), session.ReasonMalformed)

	unreadable := s.Reference()
	unreadable.Start = 0
	expect(t, p.Probe(context.Background(), unreadable.String()), session.ReasonUnreadable)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expect(t, p.Probe(cancelled, s.Reference().String()), session.ReasonUnreadable)
}

// Create refuses before anything starts: an unproven Isolation, a task
// identifier the home refuses, and an adapter that has not declared worker
// sessions. The stand-in records no call for any of them.
func TestCreateRefusesBeforeStarting(t *testing.T) {
	sub := newSubject(t)
	p, agent := provider(t, sub.home, workerScript())

	unproven := sub.spec("begin")
	unproven.Isolation = tasks.Isolation{Copy: sub.isolation.Copy, Shape: sub.isolation.Shape, Branch: sub.isolation.Branch}
	if _, err := p.Create(context.Background(), unproven); !errors.Is(err, session.ErrUnproven) {
		t.Errorf("an unproven isolation answered %v, want ErrUnproven", err)
	}
	if _, err := p.Create(context.Background(), session.Spec{Isolation: sub.isolation, Instructions: "begin", TaskID: "../x"}); !errors.Is(err, home.ErrTaskID) {
		t.Errorf("a bad task identifier answered %v, want home.ErrTaskID", err)
	}
	if calls := agent.Calls(); len(calls) != 0 {
		t.Errorf("a refused Create started a worker: %s", calls)
	}

	// The Cursor adapter declares no worker sessions, so OpenWorker refuses
	// it before anything starts and this package passes that refusal on. It
	// is built over this test binary, which it never runs here.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := agents.CursorFactory(agents.WithCursorBinary(self)).New(context.Background(), nil)
	if err != nil {
		t.Fatalf("building the cursor adapter: %v", err)
	}
	undeclared := session.NewPipe(sub.home, cursor)
	var capErr *agents.CapabilityError
	if _, err := undeclared.Create(context.Background(), sub.spec("begin")); !errors.As(err, &capErr) {
		t.Errorf("an adapter without worker sessions answered %v, want a *agents.CapabilityError", err)
	} else if capErr.Capability != agents.CapabilityWorkerSessions {
		t.Errorf("the refusal names %s, want %s", capErr.Capability, agents.CapabilityWorkerSessions)
	}
}

// p2runner is a second worker adapter for a provider that stands in for a
// restarted service: it starts nothing in these tests and exists so the
// provider under test is built the way the real one would be.
func p2runner(t *testing.T) agents.Runner {
	t.Helper()
	return standin.NewWorker(t, standin.Script{}).Runner()
}

// CaptureReference is Capture addressed by the reference the record holds:
// for a session this provider holds it reads the same bytes and digest the
// handle reads, so what a probe answers Live for is what a capture reads;
// a reference held by no provider, the same reference through a restarted
// provider, and a closed session are ErrNotHeld; and a cancelled context is
// refused before the read.
func TestCaptureReferenceReadsTheHeldSession(t *testing.T) {
	sub := newSubject(t)
	p, _ := provider(t, sub.home, workerScript())
	s := create(t, p, sub.spec("begin"))
	ref := s.Reference().String()
	waitCapture(t, p, s, "worker ready")

	byHandle, err := p.Capture(s)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	byReference, err := p.CaptureReference(context.Background(), ref)
	if err != nil || byReference != byHandle {
		t.Fatalf("CaptureReference answered %+v (%v), want the handle's %+v", byReference, err, byHandle)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.CaptureReference(cancelled, ref); !errors.Is(err, context.Canceled) {
		t.Errorf("a capture under a cancelled context answered %v, want the context's error", err)
	}
	other := session.Reference{Home: session.HomeMarker(sub.home), Task: sub.id, PID: 1 << 30, Start: 1}
	if _, err := p.CaptureReference(context.Background(), other.String()); !errors.Is(err, session.ErrNotHeld) {
		t.Errorf("a capture of a reference nobody holds answered %v, want ErrNotHeld", err)
	}
	restarted := session.NewPipe(sub.home, p2runner(t))
	if _, err := restarted.CaptureReference(context.Background(), ref); !errors.Is(err, session.ErrNotHeld) {
		t.Errorf("a restarted provider captured another provider's session by reference: %v", err)
	}
	if err := p.Close(s); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := p.CaptureReference(context.Background(), ref); !errors.Is(err, session.ErrNotHeld) {
		t.Errorf("a closed session was captured by reference: %v", err)
	}
}
