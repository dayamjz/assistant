package session_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// Both providers are what a service holds, so both are asserted to be one.
var (
	_ session.Host = (*session.Pipe)(nil)
	_ session.Host = (*session.Tmux)(nil)
)

// tmuxProvider is the tmux provider over a stand-in worker adapter, on the
// tmux binary standin.Tmux resolved or skipped on.
func tmuxProvider(t *testing.T, h *home.Home, script standin.Script) (*session.Tmux, *standin.Agent) {
	t.Helper()
	agent := standin.NewWorker(t, script, agents.WithTerminationGrace(2*time.Second))
	return session.NewTmux(h, agent.Runner(), standin.Tmux(t)), agent
}

// createIn is Create on any provider, with the session closed at cleanup.
func createIn(t *testing.T, p session.Provider, spec session.Spec) *session.Session {
	t.Helper()
	s, err := p.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = p.Close(s) })
	return s
}

// tmuxRun runs the tmux client against socket, failing the test on an error.
func tmuxRun(t *testing.T, socket string, args ...string) string {
	t.Helper()
	out, err := exec.Command(standin.Tmux(t), append([]string{"-S", socket}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// socketOf is the worker's socket, read off the attach command the provider
// answers, which is the one place it is stated.
func socketOf(t *testing.T, attach []string) string {
	t.Helper()
	i := slices.Index(attach, "-S")
	if i < 0 || i+1 >= len(attach) {
		t.Fatalf("the attach command %q names no socket", attach)
	}
	return attach[i+1]
}

// awaitRead waits until the stand-in worker has read a line containing want.
func awaitRead(t *testing.T, agent *standin.Agent, want string) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		for _, call := range agent.Calls() {
			for _, line := range call.Lines {
				if strings.Contains(line, want) {
					return
				}
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker never read a line carrying %q", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A worker the tmux provider launched is captured and typed to through the
// provider, and a person can attach to it: the attach command the provider
// answers, run in a terminal of the person's own (a second tmux server
// standing in for it, so the client has a real terminal), attaches a client
// to the worker's server, and what the person types there reaches the worker
// as a line it reads and answers, which the provider's capture then shows.
// That is the sense in which typing into the window is authoritative: it is
// the worker's input, the same input Send types into.
func TestTmuxCaptureSendAndAPersonAttaches(t *testing.T) {
	sub := newSubject(t)
	p, agent := tmuxProvider(t, sub.home, workerScript())
	s := createIn(t, p, sub.spec("implement the thing"))
	ref := s.Reference().String()

	waitCapture(t, p, s, "worker ready")
	expect(t, p.Probe(context.Background(), ref), session.ReasonHeld)
	if err := p.Send(context.Background(), s, "status?"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := waitCapture(t, p, s, "still working on it")
	if got.Digest != session.Digest(got.Text) {
		t.Errorf("the capture's digest is not its text's")
	}
	if calls := agent.Calls(); len(calls) != 1 || !calls[0].Worker || calls[0].Dir != sub.isolation.Copy {
		t.Errorf("the stand-in recorded %s, want one worker in %s", calls, sub.isolation.Copy)
	}

	attach, err := p.AttachReference(ref)
	if err != nil {
		t.Fatalf("AttachReference: %v", err)
	}
	worker := socketOf(t, attach)
	person := filepath.Join(standin.SocketDir(t), "person")
	// The person's terminal is a server of its own, and the attach runs in
	// its pane with that server's TMUX removed, as a person's shell outside
	// tmux would have none.
	tmuxRun(t, person, append([]string{"-f", "/dev/null", "new-session", "-d", "-s", "person", "-x", "200", "-y", "50",
		"--", "env", "-u", "TMUX"}, attach...)...)
	t.Cleanup(func() { _ = exec.Command(standin.Tmux(t), "-S", person, "kill-server").Run() })
	deadline := time.Now().Add(waitFor)
	for strings.TrimSpace(tmuxRun(t, worker, "list-clients", "-F", "#{client_pid}")) == "" {
		if !time.Now().Before(deadline) {
			t.Fatal("the attach command never attached a client to the worker's server")
		}
		time.Sleep(20 * time.Millisecond)
	}

	tmuxRun(t, person, "send-keys", "-t", "person", "-l", "status from a person")
	tmuxRun(t, person, "send-keys", "-t", "person", "Enter")
	awaitRead(t, agent, "status from a person")
	waitCapture(t, p, s, "status from a person")
}

// The tmux provider's liveness is Pipe's: Live through the handle while the
// pane's process runs, and, once a person kills the window from outside, Dead
// through the handle and Dead by identity from a provider holding none.
// Nothing between reads Live after the process has gone, which is P9's rule
// that only a live process signal absorbs.
func TestTmuxProbesLiveThenDeadOnceTheWindowIsKilled(t *testing.T) {
	principles.Cite(t, principles.P9)
	sub := newSubject(t)
	p, _ := tmuxProvider(t, sub.home, workerScript())
	s := createIn(t, p, sub.spec("begin"))
	ref := s.Reference().String()
	waitCapture(t, p, s, "worker ready")
	expect(t, p.Probe(context.Background(), ref), session.ReasonHeld)
	if _, ok := p.Workers().Member(s.Reference().PID); !ok {
		t.Fatal("the pane's process group is not a member of the worker set while it runs")
	}

	attach, err := p.AttachReference(ref)
	if err != nil {
		t.Fatalf("AttachReference: %v", err)
	}
	tmuxRun(t, socketOf(t, attach), "kill-window", "-t", "worker")

	expect(t, waitProbe(t, p, ref, session.ReasonExited), session.ReasonExited)
	if _, ok := p.Workers().Member(s.Reference().PID); ok {
		t.Error("the killed worker's group is still a member of the worker set")
	}
	other, _ := tmuxProvider(t, sub.home, standin.Script{})
	got := other.Probe(context.Background(), ref)
	if got.Liveness != taskstate.Dead {
		t.Errorf("a provider holding no handle reads the killed worker as %+v, want Dead", got)
	}
}

// A simulated restart: a provider over the same home holds no handle to the
// first provider's live tmux worker, so it answers Unobserved while the
// process is present and refuses to capture, send, attach or close; and
// once the worker is gone it answers Dead. The pipe provider answers the
// same reference the same way, which is what lets reconciliation treat a
// tmux-hosted survivor as it treats a pipe-hosted one: the answer is the
// shared rule's, not the provider's.
func TestTmuxAfterARestartAnIdentityAloneIsNeverLive(t *testing.T) {
	principles.Cite(t, principles.P9)
	sub := newSubject(t)
	p, _ := tmuxProvider(t, sub.home, workerScript())
	s := createIn(t, p, sub.spec("begin"))
	ref := s.Reference().String()
	waitCapture(t, p, s, "worker ready")

	restarted, _ := tmuxProvider(t, sub.home, standin.Script{})
	pipe := session.NewPipe(sub.home, p2runner(t))
	for _, after := range []session.Host{restarted, pipe} {
		expect(t, after.Probe(context.Background(), ref), session.ReasonUnheld)
		if _, err := after.CaptureReference(context.Background(), ref); !errors.Is(err, session.ErrNotHeld) {
			t.Errorf("a provider after the restart captured the survivor: %v", err)
		}
		if _, err := after.AttachReference(ref); !errors.Is(err, session.ErrNotHeld) {
			t.Errorf("a provider after the restart offered to attach to the survivor: %v", err)
		}
		if err := after.Send(context.Background(), s, "status?"); !errors.Is(err, session.ErrNotHeld) {
			t.Errorf("a provider after the restart typed into the survivor: %v", err)
		}
		if err := after.Close(s); !errors.Is(err, session.ErrNotHeld) {
			t.Errorf("a provider after the restart closed the survivor: %v", err)
		}
	}

	if err := p.Close(s); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, after := range []session.Host{restarted, pipe} {
		got := waitProbe(t, after, ref, session.ReasonAbsent)
		if got.Liveness != taskstate.Dead {
			t.Errorf("the closed worker reads %+v after the restart, want Dead", got)
		}
	}
}

// The tmux provider refuses what Pipe refuses, before anything starts: an
// unproven copy (P11), an adapter that has not declared terminal workers,
// and a provider built with no tmux binary, which is what a service whose
// operator chose tmux on a machine without one builds. A Pipe session is
// not attachable, which is the answer the peek surface reports for it.
func TestTmuxRefusesBeforeAnythingStarts(t *testing.T) {
	principles.Cite(t, principles.P11)
	sub := newSubject(t)
	p, agent := tmuxProvider(t, sub.home, workerScript())
	_, err := p.Create(context.Background(), session.Spec{TaskID: sub.id, Isolation: tasks.Isolation{}, Instructions: "x"})
	if !errors.Is(err, session.ErrUnproven) {
		t.Fatalf("an unproven copy answered %v, want ErrUnproven", err)
	}
	if calls := agent.Calls(); len(calls) != 0 {
		t.Errorf("an unproven copy started %d stand-in processes", len(calls))
	}

	cursor, err := agents.CursorFactory(agents.WithCursorBinary(standinBinary(t))).New(context.Background(), nil)
	if err != nil {
		t.Fatalf("building the cursor adapter: %v", err)
	}
	undeclared := session.NewTmux(sub.home, cursor, standin.Tmux(t))
	_, err = undeclared.Create(context.Background(), sub.spec("x"))
	var refusal *agents.CapabilityError
	if !errors.As(err, &refusal) || refusal.Capability != agents.CapabilityTerminalWorkers {
		t.Errorf("an adapter without terminal workers answered %v, want a *CapabilityError naming %s",
			err, agents.CapabilityTerminalWorkers)
	}

	if _, err := session.NewTmux(sub.home, agent.Runner(), "").Create(context.Background(), sub.spec("x")); !errors.Is(err, session.ErrNoTmux) {
		t.Errorf("a provider with no tmux binary answered %v, want ErrNoTmux", err)
	}

	pipe, _ := provider(t, sub.home, workerScript())
	ps := create(t, pipe, sub.spec("begin"))
	if _, err := pipe.AttachReference(ps.Reference().String()); !errors.Is(err, session.ErrNotAttachable) {
		t.Errorf("a pipe session answered %v to an attach, want ErrNotAttachable", err)
	}
}

// standinBinary is this test binary, which a factory is pointed at where the
// adapter is never run.
func standinBinary(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	return self
}

// A Claude Code worker under the tmux provider reaches its conversation
// with its instructions, and runs under the grant a pipe worker gets, route
// and all: the provider hands the adapter the spec's route, which the
// adapter writes into the worker's tool list.
func TestTmuxStartsAClaudeCodeWorkerWithItsRoute(t *testing.T) {
	sub := newSubject(t)
	const route = "/opt/assistant/bin/assistant worker"
	claude := standin.New(t, standin.Script{}, agents.WithTerminationGrace(2*time.Second),
		agents.WithBaseEnvironment([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
			agents.ClaudeKeyVariable + "=standin-placeholder-key-0123456789abcdefghij"}))
	p := session.NewTmux(sub.home, claude.Runner(), standin.Tmux(t))
	spec := sub.spec("begin the task")
	spec.Route = route
	createIn(t, p, spec)
	awaitRead(t, claude, "begin the task")
	for _, call := range claude.Calls() {
		if !call.Interactive {
			continue
		}
		i := slices.Index(call.Args, "--allowedTools")
		if i < 0 || i+1 >= len(call.Args) || call.Args[i+1] != "Bash(git:*),Bash("+route+":*)" {
			t.Errorf("the worker was started with %q, want its tool list to carry the route", call.Args)
		}
		return
	}
	t.Fatal("no interactive worker was recorded")
}
