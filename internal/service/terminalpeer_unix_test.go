//go:build unix

package service_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
)

// Peers placed by the foreground relation. The service decides whether a
// peer is in the foreground of its controlling terminal from the peer's own
// lineage, so a test of that rule has to make its request from a process
// whose terminal the kernel attributes and the test could not state. Two
// shapes are made here. A command in the foreground of a terminal: a shell
// in a tmux session of the test's own, which tmux gives the foreground of
// the pseudo-terminal it allocated, the shape a person's shell gives a job.
// And a command with no controlling terminal, started in a session of its
// own, the shape foreground.go observed of a host agent's tool. Neither
// walks through the worker's process group: a request from the worker is
// workers_unix_test.go's callAs.

// ran is what a command run from a placed process came to: its streams and
// its exit status.
type ran struct {
	stdout, stderr string
	status         int
}

// needsTerminal skips t when this host cannot place a peer at a terminal,
// the skip runAtTerminal would otherwise take, and a test that places one
// calls it first, before it builds anything. Skipping is runtime.Goexit,
// and a Goexit or panic that unwinds through a frame holding a dead
// address-taken stack object over 128 bytes, such as a fixture body's
// launched argument, while a deferred call allocates, can make the
// collector mark an object it already freed (golang/go#82394, which lists
// the versions and platforms; seen here on go1.26.9 darwin/amd64). As #425
// it crashed the whole race-built binary from the skip inside
// TestALocalApprovalRefusesAPeerOffATerminalAndTheWorker's body on a host
// without tmux. Skipping from the test's own frame keeps a skip off that
// path; a t.Fatal inside such a body can still reach it, which this does
// not cover.
func needsTerminal(t *testing.T) {
	t.Helper()
	standin.Tmux(t)
}

// runAtTerminal runs argv from a shell in the foreground of a terminal and
// returns what it came to. The terminal is a tmux server of the test's own,
// started for this one command and killed after it; the shell's output goes
// to files, because the pane's screen is not a stream.
func runAtTerminal(t *testing.T, argv ...string) ran {
	t.Helper()
	return startAtTerminal(t, argv...)()
}

// startAtTerminal is runAtTerminal in two halves: it starts argv from a
// shell in the foreground of a terminal and returns what waits for it, so
// a test can read the command's processes off the kernel, or release
// something they wait on, while they run.
func startAtTerminal(t *testing.T, argv ...string) func() ran {
	t.Helper()
	tmux := standin.Tmux(t)
	dir, err := os.MkdirTemp("", "t")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	out, errOut, code := filepath.Join(dir, "out"), filepath.Join(dir, "err"), filepath.Join(dir, "code")
	script := shellQuote(argv...) + " > " + shellQuote(out) + " 2> " + shellQuote(errOut) +
		"; echo $? > " + shellQuote(code+".tmp") + " && mv " + shellQuote(code+".tmp") + " " + shellQuote(code)
	if raw, err := exec.Command(tmux, "-S", socket, "new-session", "-d", "-x", "120", "-y", "24", "--",
		"/bin/sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("starting a terminal for the command: %v: %s", err, raw)
	}
	t.Cleanup(func() { _ = exec.Command(tmux, "-S", socket, "kill-server").Run() })
	return func() ran {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for {
			raw, err := os.ReadFile(code)
			if err == nil {
				status, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
				stdout, _ := os.ReadFile(out)
				stderr, _ := os.ReadFile(errOut)
				return ran{stdout: string(stdout), stderr: string(stderr), status: status}
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("the command at the terminal never finished: %q", argv)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// runDetached runs argv from a process with no controlling terminal, in a
// session of its own, and returns what it came to.
func runDetached(t *testing.T, argv ...string) ran {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return ran{stdout: stdout.String(), stderr: stderr.String()}
	case errors.As(err, &exit):
		return ran{stdout: stdout.String(), stderr: stderr.String(), status: exit.ExitCode()}
	}
	t.Fatalf("running %q detached: %v", argv, err)
	return ran{}
}

// callArgs is the command line that makes this binary one call on the
// service's socket, on callMain's terms.
func callArgs(t *testing.T, socket string, method ipc.Method, params any) []string {
	t.Helper()
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return []string{testCommand(t), callVerb, socket, string(method), string(body)}
}

// callOutcome reads callMain's protocol out of what a call came to: the
// answer on success, the wire code on a refusal, read by refusedWith, and a
// failure for anything else.
func callOutcome(t *testing.T, method ipc.Method, how string, got ran) (json.RawMessage, ipc.Code) {
	t.Helper()
	switch got.status {
	case 0:
		return json.RawMessage(got.stdout), ""
	case 1:
		return nil, refusedWith(t, method, got.stderr)
	}
	t.Fatalf("calling %s %s exited %d: %s", method, how, got.status, got.stderr)
	return nil, ""
}

// callAtTerminal makes one call on the service's socket from a process in
// the foreground of a terminal, and returns the answer or the wire code it
// was refused with.
func callAtTerminal(t *testing.T, running serviceUnderTest, method ipc.Method, params any) (json.RawMessage, ipc.Code) {
	t.Helper()
	return callOutcome(t, method, "at a terminal", runAtTerminal(t, callArgs(t, running.service.Socket(), method, params)...))
}

// callDetached makes one call on the service's socket from a process with
// no controlling terminal, and returns the answer or the wire code it was
// refused with.
func callDetached(t *testing.T, running serviceUnderTest, method ipc.Method, params any) (json.RawMessage, ipc.Code) {
	t.Helper()
	return callOutcome(t, method, "detached", runDetached(t, callArgs(t, running.service.Socket(), method, params)...))
}

// answerAtTerminal answers a task's hold the way a person does, with the
// product's own command line run in the foreground of a terminal:
// "--task ID --answer ANSWER" through cli.Run, with --json so the answer
// is read back as the structured document. A command that failed is an
// error carrying what it printed.
func answerAtTerminal(t *testing.T, h *home.Home, task, answer string) (machine.TaskAnswered, error) {
	t.Helper()
	got := runAtTerminal(t, testCommand(t), cliVerb, "--home", h.Root(), "--json", "--task", task, "--answer", answer)
	if got.status != 0 {
		return machine.TaskAnswered{}, fmt.Errorf("the command exited %d:\n%s%s", got.status, got.stdout, got.stderr)
	}
	var answered machine.TaskAnswered
	if err := json.Unmarshal([]byte(got.stdout), &answered); err != nil {
		return machine.TaskAnswered{}, fmt.Errorf("the answer does not decode: %w\n%s", err, got.stdout)
	}
	return answered, nil
}
