package agents_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// terminalFor is a Terminal on a fresh socket in a short directory, over the
// tmux binary standin.Tmux resolved or skipped on.
func terminalFor(t *testing.T) agents.Terminal {
	t.Helper()
	return agents.Terminal{Tmux: standin.Tmux(t), Socket: filepath.Join(standin.SocketDir(t), "s")}
}

// openTerminal starts a terminal worker over the scripted stand-in on a fresh
// terminal and closes it when the test ends.
func openTerminal(t *testing.T, script standin.Script, spec agents.WorkerSpec,
	opts ...agents.ClaudeOption) (agents.TerminalWorker, agents.Terminal, *standin.Agent) {
	t.Helper()
	term := terminalFor(t)
	agent := standin.NewWorker(t, script, append([]agents.ClaudeOption{
		agents.WithTerminationGrace(300 * time.Millisecond),
	}, opts...)...)
	worker, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, spec)
	if err != nil {
		t.Fatalf("starting a terminal worker: %v", err)
	}
	t.Cleanup(worker.Close)
	return worker, term, agent
}

// awaitLines waits until the stand-in has recorded at least n lines read, and
// returns them.
func awaitLines(t *testing.T, agent *standin.Agent, n int) []string {
	t.Helper()
	lines, ok := linesWithin(agent, n, 30*time.Second)
	if !ok {
		t.Fatalf("the worker never recorded %d lines read", n)
	}
	return lines
}

// linesWithin is the lines the stand-in worker recorded once it has read n,
// and false when it has not within wait.
func linesWithin(agent *standin.Agent, n int, wait time.Duration) ([]string, bool) {
	deadline := time.Now().Add(wait)
	for {
		for _, call := range agent.Calls() {
			if call.Worker && len(call.Lines) >= n {
				return call.Lines, true
			}
		}
		if !time.Now().Before(deadline) {
			return nil, false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A terminal worker is typed to: the instructions and a send reach the
// stand-in as the lines they were, its reply is on the pane's screen, the
// handle names the pane's process, and Close ends the process, the server,
// and the socket.
func TestATerminalWorkerIsTypedToAndEndedWithItsServer(t *testing.T) {
	script := standin.Script{Worker: &standin.WorkerScript{
		Greeting:  "terminal stand-in ready",
		Exchanges: []standin.Exchange{{Contains: "ping", Reply: "pong from the pane"}},
	}}
	worker, term, agent := openTerminal(t, script, workerSpec(t))
	// Read before anything else gives the process time: the group the
	// handle names is led by the pane's process as soon as the handle is
	// returned, which tmux's own answer at creation does not ensure (#273).
	if lineage, found, err := agents.ReadLineage(worker.Group()); err != nil || !found || lineage.Group != worker.Group() {
		t.Fatalf("on return the pane's process is in group %d (found %v, %v), want it leading its own, %d",
			lineage.Group, found, err, worker.Group())
	}

	awaitWorkerOutput(t, worker, "terminal stand-in ready")
	identity, found, err := agents.Identify(worker.Group())
	if err != nil || !found {
		t.Fatalf("reading the pane process's identity while it runs: found %v, %v", found, err)
	}
	if err := worker.Send(t.Context(), "ping one"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	awaitWorkerOutput(t, worker, "pong from the pane")
	if lines := awaitLines(t, agent, 2); lines[0] != "work on the task" || lines[1] != "ping one" {
		t.Errorf("the worker read %q, want the instructions then the send", lines)
	}
	if worker.Group() <= 0 {
		t.Errorf("Group() = %d, want the pane's process", worker.Group())
	}
	if got := worker.Attach(); len(got) < 3 || got[0] != term.Tmux || !strings.Contains(strings.Join(got, " "), term.Socket) {
		t.Errorf("Attach() = %q, want the tmux binary addressing the worker's socket", got)
	}

	worker.Close()
	select {
	case <-worker.Done():
	default:
		t.Fatal("the worker is not done after Close returned")
	}
	if _, err := os.Lstat(term.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket is still there after Close: %v", err)
	}
	if err := exec.Command(term.Tmux, "-S", term.Socket, "has-session").Run(); err == nil {
		t.Error("a tmux server still answers on the worker's socket after Close")
	}
	if presence, err := identity.Presence(); err != nil || presence == agents.PresencePresent {
		t.Errorf("the pane's process reads %q (%v) after Close, want it gone", presence, err)
	}
	if got := worker.Capture().Text; !strings.Contains(got, "pong from the pane") {
		t.Errorf("Capture after Close answers %q, want the last screen the handle read", got)
	}
	if err := worker.Send(t.Context(), "too late"); !errors.Is(err, agents.ErrWorkerEnded) {
		t.Errorf("Send after Close answered %v, want ErrWorkerEnded", err)
	}
}

// A worker that exits on its own is Done, and its server and socket go with
// it, without anyone calling Close.
func TestATerminalWorkerThatExitsTakesItsServerWithIt(t *testing.T) {
	script := standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "finish", Reply: "finishing", Ends: true}},
	}}
	worker, term, _ := openTerminal(t, script, workerSpec(t))
	if err := worker.Send(t.Context(), "finish now"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case <-worker.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the worker exited on its own and Done never closed")
	}
	if _, err := os.Lstat(term.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket outlived the worker: %v", err)
	}
}

// The pane runs under the one environment builder's output: a variable that
// would point git elsewhere, set in the runner's base environment, is not in
// the environment of a command the worker runs, while the variable tmux sets
// for a pane is, which is what shows the command ran in the pane.
func TestATerminalWorkerRunsUnderTheWorkerEnvironment(t *testing.T) {
	spec := workerSpec(t)
	script := standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "run:", Run: true}},
	}}
	base := []string{"PATH=" + os.Getenv("PATH"), "GIT_DIR=/somewhere/else", "TMUX=/not/this/one,1,0"}
	worker, _, _ := openTerminal(t, script, spec, agents.WithBaseEnvironment(base))
	if err := worker.Send(t.Context(), "run: env > env.txt"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	path := filepath.Join(spec.Dir, "env.txt")
	deadline := time.Now().Add(30 * time.Second)
	var env string
	for {
		raw, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(raw), "\n") {
			env = string(raw)
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker's command never wrote its environment: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if strings.Contains(env, "GIT_DIR=") {
		t.Errorf("the worker's environment carries GIT_DIR:\n%s", env)
	}
	if strings.Contains(env, "/not/this/one") {
		t.Errorf("the worker's environment carries the service's own TMUX:\n%s", env)
	}
	if !strings.Contains(env, "TMUX_PANE=") {
		t.Errorf("the command did not run in a tmux pane:\n%s", env)
	}
}

// The scripted adapter's terminal framing refuses a line over
// TerminalLineLimit before anything is typed, at the instructions and at a
// send.
func TestTheTerminalFramingRefusesALongLine(t *testing.T) {
	long := strings.Repeat("x", agents.TerminalLineLimit+1)
	agent := standin.NewWorker(t, standin.Script{})
	term := terminalFor(t)
	_, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term,
		agents.WorkerSpec{Dir: t.TempDir(), Instructions: "first line\n" + long})
	if !errors.Is(err, agents.ErrInvalidInvocation) {
		t.Fatalf("instructions with a %d-byte line answered %v, want ErrInvalidInvocation", len(long), err)
	}
	if _, err := os.Lstat(term.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a server was started for instructions the framing refused: %v", err)
	}

	worker, _, agent := openTerminal(t, standin.Script{}, workerSpec(t))
	if err := worker.Send(t.Context(), long); !errors.Is(err, agents.ErrInvalidInvocation) {
		t.Fatalf("a send with a %d-byte line answered %v, want ErrInvalidInvocation", len(long), err)
	}
	if err := worker.Send(t.Context(), strings.Repeat("y", agents.TerminalLineLimit)); err != nil {
		t.Fatalf("a send at the limit answered %v", err)
	}
	if lines := awaitLines(t, agent, 2); len(lines[1]) != agents.TerminalLineLimit {
		t.Errorf("the line at the limit arrived as %d bytes, want %d", len(lines[1]), agents.TerminalLineLimit)
	}
}

// The reason the framing has a limit at all: a line typed into a pane whose
// program reads in the terminal's line-editing mode does not arrive whole.
// The typing here goes around the framing, straight through tmux, so it is a
// person's keystrokes and not the adapter's; what the stand-in recorded is
// what reached it. What the terminal does with the line is the platform's:
// on Linux it arrives cut, and the test asserts the cut is still longer than
// the framing's limit and records where it fell; on a macOS runner the line
// never completed within the wait, which is accepted there and nowhere else.
// Either way the line did not arrive whole, which is what the limit is for.
func TestATerminalCutsALongLineTheFramingRefuses(t *testing.T) {
	worker, term, agent := openTerminal(t, standin.Script{}, workerSpec(t))
	awaitLines(t, agent, 1)
	typed := strings.Repeat("z", 10000)
	for _, args := range [][]string{
		{"send-keys", "-t", "worker", "-l", typed},
		{"send-keys", "-t", "worker", "Enter"},
	} {
		if out, err := exec.Command(term.Tmux, append([]string{"-S", term.Socket}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("typing through tmux: %v: %s", err, out)
		}
	}
	lines, arrived := linesWithin(agent, 2, 10*time.Second)
	if !arrived {
		if runtime.GOOS != "darwin" {
			t.Fatalf("the %d-byte line never reached the worker", len(typed))
		}
		t.Logf("a %d-byte line typed into the pane never completed on %s", len(typed), runtime.GOOS)
		worker.Close()
		return
	}
	got := len(lines[1])
	if got >= len(typed) {
		t.Fatalf("a %d-byte line arrived whole, so the framing's limit guards nothing here", len(typed))
	}
	if got <= agents.TerminalLineLimit {
		t.Errorf("the line was cut to %d bytes, at or under the framing's limit of %d, so the limit does not keep a line whole",
			got, agents.TerminalLineLimit)
	}
	t.Logf("a %d-byte line typed into the pane arrived as %d bytes", len(typed), got)
	worker.Close()
}

// A Terminal that cannot be started as written is refused before anything
// starts, and so is a socket that already exists.
func TestATerminalIsRefusedBeforeAnythingStarts(t *testing.T) {
	agent := standin.NewWorker(t, standin.Script{})
	tmux := standin.Tmux(t)
	dir := standin.SocketDir(t)
	cases := map[string]agents.Terminal{
		"a relative tmux":   {Tmux: "tmux", Socket: filepath.Join(dir, "a")},
		"a relative socket": {Tmux: tmux, Socket: "s"},
		"a socket too long": {Tmux: tmux, Socket: filepath.Join(dir, strings.Repeat("l", 120))},
	}
	for name, term := range cases {
		_, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, workerSpec(t))
		if !errors.Is(err, agents.ErrInvalidInvocation) {
			t.Errorf("%s answered %v, want ErrInvalidInvocation", name, err)
		}
	}
	occupied := filepath.Join(dir, "taken")
	if err := os.WriteFile(occupied, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), agents.Terminal{Tmux: tmux, Socket: occupied}, workerSpec(t))
	if !errors.Is(err, agents.ErrTerminalOccupied) {
		t.Errorf("an existing socket answered %v, want ErrTerminalOccupied", err)
	}
}

// OpenTerminalWorker reads the declaration before the type: a shipped
// adapter that does not declare terminal workers, the Cursor one, is refused
// by name, and a stub carrying the mechanism without declaring it is refused
// the same way.
func TestOpenTerminalWorkerConsultsTheDeclarationBeforeTheMechanism(t *testing.T) {
	term := agents.Terminal{Tmux: "/nonexistent/tmux", Socket: "/nonexistent/s"}
	cursor, err := agents.CursorFactory(agents.WithCursorBinary(helperBinary(t))).New(t.Context(), nil)
	if err != nil {
		t.Fatalf("building the cursor adapter: %v", err)
	}
	_, err = agents.OpenTerminalWorker(t.Context(), cursor, term, workerSpec(t))
	var refusal *agents.CapabilityError
	if !errors.As(err, &refusal) || refusal.Capability != agents.CapabilityTerminalWorkers || refusal.Agent != agents.CursorName {
		t.Fatalf("the cursor adapter answered %v, want a *CapabilityError naming %s", err, agents.CapabilityTerminalWorkers)
	}
	undeclared := &terminalStubRunner{stubRunner: &stubRunner{name: "undeclared"}}
	if _, err := agents.OpenTerminalWorker(t.Context(), undeclared, term, workerSpec(t)); !errors.Is(err, agents.ErrUndeclaredCapability) {
		t.Errorf("an undeclared stub answered %v, want ErrUndeclaredCapability", err)
	}
	declared := (&stubFactory{
		name: "declared", available: true, terminals: true,
		declares: []agents.Capability{agents.CapabilityTerminalWorkers},
	}).runner()
	if _, err := agents.OpenTerminalWorker(t.Context(), declared, term, workerSpec(t)); !errors.Is(err, errStubTerminalReached) {
		t.Errorf("a declared stub answered %v, want its own mechanism", err)
	}
	boaster := &stubRunner{name: "boaster", declared: agents.Declare(agents.CapabilityTerminalWorkers)}
	if _, err := agents.OpenTerminalWorker(t.Context(), boaster, term, workerSpec(t)); !errors.Is(err, agents.ErrAdapterDeclaration) {
		t.Errorf("a boasting stub answered %v, want ErrAdapterDeclaration", err)
	}
}
