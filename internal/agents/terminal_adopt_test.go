package agents_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// serverConfigDir is the configuration directory the server on term names in
// its environment, as a Claude Code terminal worker's start put it there.
func serverConfigDir(t *testing.T, term agents.Terminal) string {
	t.Helper()
	out, err := exec.Command(term.Tmux, "-S", term.Socket, "show-environment", "-g", "CLAUDE_CONFIG_DIR").Output()
	if err != nil {
		t.Fatalf("reading the server's configuration directory: %v", err)
	}
	dir, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "CLAUDE_CONFIG_DIR=")
	if !ok || dir == "" {
		t.Fatalf("the server names no configuration directory: %q", out)
	}
	return dir
}

// identityOf is the identity of the worker's pane process.
func identityOf(t *testing.T, w agents.TerminalWorker) agents.Identity {
	t.Helper()
	id, found, err := agents.Identify(w.Group())
	if err != nil || !found {
		t.Fatalf("reading the worker's identity: found %v, %v", found, err)
	}
	return id
}

// presentWithin reports whether id is still present after wait, polling so a
// process that ends inside it is seen.
func presentWithin(id agents.Identity, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if p, err := id.Presence(); err == nil && p != agents.PresencePresent {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// Finding 1 of #778's review: what an adopted Claude Code worker removes
// when it ends is not taken from anything the worker's server says. A
// released worker whose server's environment was pointed at another
// worker's configuration directory is adopted and closed, and that other
// directory is still there.
func TestAnAdoptedClaudeWorkerRemovesNoDirectoryItsServerNames(t *testing.T) {
	released, term, agent := openClaudeTerminal(t, standin.Script{}, workerSpec(t))
	_, otherTerm, _ := openClaudeTerminal(t, standin.Script{}, workerSpec(t))
	victim := serverConfigDir(t, otherTerm)
	own := serverConfigDir(t, term)
	pane := identityOf(t, released)
	if err := released.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if out, err := exec.Command(term.Tmux, "-S", term.Socket, "set-environment", "-g", "CLAUDE_CONFIG_DIR", victim).CombinedOutput(); err != nil {
		t.Fatalf("pointing the released worker's server at the other directory: %v: %s", err, out)
	}
	held, err := agents.AdoptTerminalWorker(t.Context(), agent.Runner(), term, pane)
	if err != nil {
		t.Fatalf("adopting: %v", err)
	}
	held.Close()
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the other worker's configuration directory after the adopted worker ended: %v, want it still there", err)
	}
	if _, err := os.Stat(own); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the adopted worker's own configuration directory after it ended: %v, want it removed", err)
	}
}

// Finding 2: a hurry closed as Release runs ends the worker only when it
// wins, and then Release is refused with ErrWorkerEnding; a Release that
// returns nil leaves the worker running whatever the hurry does after. The
// first case is the control, with the hurry closed only once Release has
// returned; the loop races the two.
func TestAHurryRacingReleaseNeverEndsAWorkerReleaseLetGo(t *testing.T) {
	script := standin.Script{Worker: &standin.WorkerScript{Greeting: "ready"}}
	adopt := func(t *testing.T) (agents.TerminalWorker, agents.Identity, chan struct{}) {
		t.Helper()
		started, term, agent := openTerminal(t, script, workerSpec(t))
		pane := identityOf(t, started)
		if err := started.Release(); err != nil {
			t.Fatalf("Release of the started handle: %v", err)
		}
		hurry := make(chan struct{})
		held, err := agents.AdoptTerminalWorker(agents.WithHurry(context.Background(), hurry), agent.Runner(), term, pane)
		if err != nil {
			t.Fatalf("adopting: %v", err)
		}
		t.Cleanup(held.Close)
		return held, pane, hurry
	}

	held, pane, hurry := adopt(t)
	if err := held.Release(); err != nil {
		t.Fatalf("Release before any hurry: %v", err)
	}
	close(hurry)
	if !presentWithin(pane, 500*time.Millisecond) {
		t.Fatal("a hurry closed after Release returned ended the worker")
	}
	held.Close()

	for i := range 10 {
		held, pane, hurry := adopt(t)
		close(hurry)
		err := held.Release()
		switch {
		case err == nil && !presentWithin(pane, 500*time.Millisecond):
			t.Fatalf("iteration %d: Release returned nil and the hurry ended the worker", i)
		case err != nil && !errors.Is(err, agents.ErrWorkerEnding):
			t.Fatalf("iteration %d: Release answered %v, want nil or ErrWorkerEnding", i, err)
		}
		held.Close()
	}
}

// Finding 3: a stale released handle's Close ends nothing that is not its
// worker. Its worker is adopted and closed, a new worker starts on the same
// socket, and the stale handle's Close leaves the new worker and its socket.
func TestAStaleReleasedHandleLeavesAReplacementOnItsSocket(t *testing.T) {
	script := standin.Script{Worker: &standin.WorkerScript{Greeting: "ready"}}
	stale, term, agent := openTerminal(t, script, workerSpec(t))
	pane := identityOf(t, stale)
	if err := stale.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	held, err := agents.AdoptTerminalWorker(t.Context(), agent.Runner(), term, pane)
	if err != nil {
		t.Fatalf("adopting: %v", err)
	}
	held.Close()
	replacement, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, workerSpec(t))
	if err != nil {
		t.Fatalf("starting the replacement on the same socket: %v", err)
	}
	t.Cleanup(replacement.Close)
	next := identityOf(t, replacement)
	if err := stale.Send(t.Context(), "hello"); !errors.Is(err, agents.ErrWorkerReleased) {
		t.Fatalf("Send on the stale handle answered %v, want ErrWorkerReleased", err)
	}
	stale.Close()
	if !presentWithin(next, 500*time.Millisecond) {
		t.Fatal("the stale handle's Close ended the replacement worker")
	}
	if _, err := os.Lstat(term.Socket); err != nil {
		t.Fatalf("the replacement's socket after the stale Close: %v, want it still there", err)
	}
}
