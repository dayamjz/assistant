//go:build unix

package service

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
)

// restartSubject is an opened service whose workers run in tmux, with one
// task planted after open so the launch a test makes is the only one.
type restartSubject struct {
	service *Service
	task    string
	d       dispatched
}

// openRestartSubject opens a service over a home whose global layer puts
// workers in tmux under stop_grace grace, and dispatches one planted task.
func openRestartSubject(t *testing.T, grace time.Duration) restartSubject {
	t.Helper()
	standin.Tmux(t)
	document := strings.TrimSuffix(strings.TrimSpace(closeGraceConfig(grace)), "}") + `, "session_provider": "tmux"}` + "\n"
	h, records := loopHomeConfigured(t, document)
	agent := workerStandIn(t)
	l := openLoopWith(t, h, records, nil, nil, agents.NewCatalog(fixedRunner{runner: agent.Runner()}, cursorFactory{runner: agent.Runner()}))
	id, _ := plantLaunchable(t, h, l.service.store)
	d, err := l.service.dispatchTask(t.Context(), id)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	return restartSubject{service: l.service, task: id, d: d}
}

// endServerAtCleanup ends the tmux server ref names by its socket when the
// test ends, so a failure below leaks nothing.
func endServerAtCleanup(t *testing.T, s *Service, ref string) session.Reference {
	t.Helper()
	parsed, err := session.ParseReference(ref)
	if err != nil || parsed.Terminal == "" {
		t.Fatalf("the worker's reference %q names no terminal (%v)", ref, err)
	}
	socket := filepath.Join(s.home.TerminalSockets(), parsed.Terminal)
	tmux := standin.Tmux(t)
	t.Cleanup(func() { _ = exec.Command(tmux, "-S", socket, "kill-server").Run() })
	return parsed
}

// P15: a restart's forced stop does not end a worker the restart let go,
// even one let go after the stop began. The launch is held at the record of
// its session when Shutdown begins, so the worker is a session still being
// recorded, which the stop's first reading of its groups names and the
// first release passes over. The record then finishes, Close's second
// release lets the worker go, and Close stalls at the next step, the way
// ending a subordinate coordinator can, until the stop is forced. The
// forced stop must not signal the released worker: it reads Present after
// Shutdown returns, and the log says it goes on across the restart. A
// worker the force kills here is one the next service finds dead and
// relaunches, losing its context, which is what the restart was for.
func TestAForcedRestartLeavesAWorkerReleasedAfterTheStopBegan(t *testing.T) {
	principles.Cite(t, principles.P15)
	sub := openRestartSubject(t, time.Minute)
	s := sub.service
	reached := make(chan string, 1)
	proceed := make(chan struct{})
	s.beforeSessionRecord = func(task, ref string) {
		if task != sub.task {
			return
		}
		reached <- ref
		<-proceed
	}
	launched := make(chan error, 1)
	go func() {
		_, err := s.launchTask(t.Context(), sub.d)
		launched <- err
	}()
	var ref string
	select {
	case ref = <-reached:
	case <-time.After(loopWait):
		close(proceed)
		t.Fatal("the launch never reached the record of its session")
	}
	pane := endServerAtCleanup(t, s, ref)

	force := make(chan struct{})
	unstall := make(chan struct{})
	stalled := false
	s.atCloseStep = func(step string) {
		switch {
		case strings.HasPrefix(step, "ending the workers it launched"):
			close(proceed)
			if err := <-launched; err != nil {
				t.Errorf("the held launch failed once let go: %v", err)
			}
		case strings.HasPrefix(step, "ending the subordinate coordinators"):
			stalled = true
			close(force)
			<-unstall
		}
	}
	t.Cleanup(func() {
		close(unstall)
		_ = s.Close()
	})
	s.Stop(true)
	err := s.Shutdown(force)
	if !stalled {
		t.Fatalf("Close never reached the step this test stalls it at; Shutdown answered %v", err)
	}
	var forced *StopForcedError
	if !errors.As(err, &forced) || !forced.Early {
		t.Fatalf("the stop answered %v, want a *StopForcedError for a stop forced early", err)
	}
	if presence, err := (agents.Identity{PID: pane.PID, Start: pane.Start}).Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the worker the restart let go reads %q (%v) after the forced stop, want it still running", presence, err)
	}
	if !strings.Contains(serviceLogOf(t, s.home), "goes on in terminal session "+ref+" across the restart") {
		t.Fatalf("the service log does not say the worker goes on across the restart:\n%s", serviceLogOf(t, s.home))
	}
}

// A restart lets go of no worker while the protocol server can still serve
// it: a worker let go is no longer in any set this service places a peer
// by, so a request it had in flight, or sent then, would be placed by its
// terminal's foreground instead, a person's, and pass the coordinator's
// guard. Close is held as it enters the protocol server's close, which
// waits for every request in flight, and the worker must still be placed
// as its task's worker there. Once Close is done the worker is let go: in
// no set, and still running.
func TestARestartPlacesItsWorkerAsAWorkerUntilTheProtocolServerIsClosed(t *testing.T) {
	sub := openRestartSubject(t, time.Minute)
	s := sub.service
	held, err := s.launchTask(t.Context(), sub.d)
	if err != nil {
		t.Fatalf("launching: %v", err)
	}
	ref := held.Reference()
	endServerAtCleanup(t, s, ref.String())
	peer := ipc.Credentials{PID: ref.PID}
	if m, ok, err := s.workerOf(peer); err != nil || !ok || m.Task != sub.task {
		t.Fatalf("the launched worker is placed as %+v, %v (%v), want task %s's worker", m, ok, err, sub.task)
	}
	type placed struct {
		member session.Membership
		ok     bool
		err    error
	}
	atServer := make(chan placed, 1)
	s.atCloseStep = func(step string) {
		if strings.HasPrefix(step, "closing the protocol server") {
			m, ok, err := s.workerOf(peer)
			atServer <- placed{member: m, ok: ok, err: err}
		}
	}
	s.Stop(true)
	if err := s.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	got := <-atServer
	if got.err != nil || !got.ok || got.member.Task != sub.task {
		t.Fatalf("while the protocol server was closing, the worker was placed as %+v, %v (%v), want task %s's worker",
			got.member, got.ok, got.err, sub.task)
	}
	if _, ok, err := s.workerOf(peer); err != nil || ok {
		t.Fatalf("after the restart closed the service, the worker is still placed as a worker (%v, %v), want it let go", ok, err)
	}
	if presence, err := (agents.Identity{PID: ref.PID, Start: ref.Start}).Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the worker reads %q (%v) after the restart, want it still running", presence, err)
	}
}

// P15: once a forced stop has claimed the groups it signals, a restart lets
// no more workers go, so none is let go after the force has read what to
// end: the worker stays held, which closeSessions then ends, and nothing
// says it goes on across the restart.
func TestAReleaseAfterAForcedStopClaimedItsGroupsLetsNothingGo(t *testing.T) {
	principles.Cite(t, principles.P15)
	sub := openRestartSubject(t, time.Minute)
	s := sub.service
	defer func() { _ = s.Close() }()
	held, err := s.launchTask(t.Context(), sub.d)
	if err != nil {
		t.Fatalf("launching: %v", err)
	}
	endServerAtCleanup(t, s, held.Reference().String())
	s.Stop(true)
	if left := s.claimForce(sessionLeaders(held)); len(left) != 2 {
		t.Fatalf("the force claimed %+v of the held worker's pane and server, want both", left)
	}
	s.releaseForRestart()
	if _, still, ok := s.sessionOf(sub.task); !ok || still != held {
		t.Fatalf("after the force claimed its groups, the restart let the worker go (held %v)", ok)
	}
	if strings.Contains(serviceLogOf(t, s.home), "across the restart") {
		t.Fatalf("the log says a worker goes on across the restart after the force claimed its groups:\n%s", serviceLogOf(t, s.home))
	}
}
