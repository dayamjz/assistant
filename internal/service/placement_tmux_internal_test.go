//go:build linux || darwin

package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// Placement under the tmux provider, read off real processes with the real
// reader. Under the pipe provider a worker is this process's child; under
// tmux it is the child of a tmux server that is nobody's descendant here, so
// the question these tests settle is whether the walk placement.go makes
// still places the worker's commands as the worker's and nothing else as
// the worker's when the worker's parent is that server.
//
// The set is one worker, answered by the pane process's group, which is what
// session.Tmux's Workers answers for a worker it holds; internal/session's
// tests hold the provider to that, and the service tests in
// workers_tmux_unix_test.go reach the same walk through the real set and the
// socket's credentials.

// terminalWorker starts a stand-in worker hosted in a tmux server of its
// own, which runs what follows "run: " on a line it reads, and returns it
// with its terminal and working directory.
func terminalWorker(t *testing.T) (agents.TerminalWorker, agents.Terminal, string) {
	t.Helper()
	term := agents.Terminal{Tmux: standin.Tmux(t), Socket: filepath.Join(standin.SocketDir(t), "w")}
	agent := standin.NewWorker(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "run: ", Run: true}},
	}}, agents.WithTerminationGrace(300*time.Millisecond))
	dir := t.TempDir()
	worker, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, agents.WorkerSpec{Dir: dir, Instructions: "work"})
	if err != nil {
		t.Fatalf("starting the terminal worker: %v", err)
	}
	t.Cleanup(worker.Close)
	return worker, term, dir
}

// tmuxIn runs the tmux client against term's server.
func tmuxIn(t *testing.T, term agents.Terminal, args ...string) string {
	t.Helper()
	out, err := exec.Command(term.Tmux, append([]string{"-S", term.Socket}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// pidIn waits for path to hold a process identifier and returns it, with the
// process killed when the test ends.
func pidIn(t *testing.T, path string) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.Fields(waitFile(t, path))[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// The worker under tmux is not this process's child: its parent is the tmux
// server, whose own parent is no process of this test. A command the worker
// runs in a process group of its own, which is the shape #225 observed of the
// Claude Code CLI's Bash tool, is still the pane process's descendant, so the
// walk places it as the worker's, and the group read alone would not have.
// The server itself, and this test process, are placed as nobody's.
func TestUnderTmuxAWorkersCommandIsPlacedThroughTheServer(t *testing.T) {
	t.Parallel()
	worker, term, dir := terminalWorker(t)
	group := worker.Group()
	member := oneWorker(group)

	pane, err := readLineage(group)
	if err != nil {
		t.Fatal(err)
	}
	if pane.Group != group {
		t.Fatalf("the pane's process is in group %d, want it to lead its own, %d", pane.Group, group)
	}
	if pane.Parent == os.Getpid() {
		t.Fatal("the pane's process is this test's child, so this is the pipe provider's shape and not tmux's")
	}
	server, err := readLineage(pane.Parent)
	if err != nil {
		t.Fatal(err)
	}
	if server.Parent == os.Getpid() {
		t.Fatal("the tmux server is this test's child, so it is not the daemon a real tmux server is")
	}
	if want := tmuxIn(t, term, "display-message", "-p", "#{pid}"); strconv.Itoa(server.PID) != want {
		t.Fatalf("the pane's parent is process %d and the server reports itself as %s", server.PID, want)
	}

	// The shell stays until released, so the chain from the sleep to the
	// worker stands while it is read; a shell that exited would leave the
	// sleep reparented, which is placement.go's named gap and not this test.
	command := `sleep 60 >/dev/null 2>&1 & echo "$!" > child.tmp && mv child.tmp child; ` +
		`while [ ! -e release ]; do sleep 0.02; done`
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600) })
	if err := worker.Send(t.Context(), "run: "+command); err != nil {
		t.Fatalf("Send: %v", err)
	}
	child := pidIn(t, filepath.Join(dir, "child"))
	own, err := processGroup(child)
	if err != nil {
		t.Fatal(err)
	}
	if own == group {
		t.Fatalf("the command ran in the worker's own group %d, so this is not the shape the test is about", group)
	}
	if _, ok := member(own); ok {
		t.Fatal("the group read alone places the command, so the walk is not what placed it")
	}
	if m, ok, err := placeByAncestry(child, readLineage, member); err != nil || !ok || m.Group != group {
		t.Fatalf("the worker's command under tmux was placed %+v, %v, %v; want the worker's", m, ok, err)
	}
	for name, pid := range map[string]int{"the tmux server": server.PID, "this test": os.Getpid()} {
		if _, ok, err := placeByAncestry(pid, readLineage, member); err != nil || ok {
			t.Errorf("%s was placed %v, %v; want nobody's", name, ok, err)
		}
	}
}

// The person's controls, on the same server the worker runs in. A shell a
// person opens in a new window of the worker's server is the server's child
// and the worker's sibling, so it is placed as nobody's. A person outside
// tmux who types into the worker's window with send-keys is placed as
// nobody's, and so is the client a person attaches with. What the person
// types into the window, though, is the worker's input, so a command they
// have the worker run is placed as the worker's: typing into the window is
// authoritative, and what it makes the worker do is the worker's act, read
// off the kernel's chain like every other.
func TestUnderTmuxAPersonsShellOnTheSameServerIsNotTheWorker(t *testing.T) {
	t.Parallel()
	worker, term, dir := terminalWorker(t)
	member := oneWorker(worker.Group())
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600) })

	// A window a person opens in the worker's own server.
	window := filepath.Join(dir, "window")
	tmuxIn(t, term, "new-window", "-d", "--", "/bin/sh", "-c", `echo "$$" > `+window+`.tmp && mv `+window+`.tmp `+window+` && exec sleep 60`)
	if _, ok, err := placeByAncestry(pidIn(t, window), readLineage, member); err != nil || ok {
		t.Errorf("a shell in a new window of the worker's server was placed %v, %v; want nobody's", ok, err)
	}

	// A person's shell outside tmux types a command into the worker's
	// window, and waits so its own identifier can be read.
	typed := filepath.Join(dir, "typed")
	outside := filepath.Join(dir, "outside")
	script := `echo "$$" > ` + outside + `.tmp && mv ` + outside + `.tmp ` + outside + ` && ` +
		`"$0" -S "$1" send-keys -t worker -l "run: sleep 60 >/dev/null 2>&1 & echo \$! > ` + typed + `.tmp && mv ` + typed + `.tmp ` + typed +
		`; while [ ! -e release ]; do sleep 0.02; done" && ` +
		`"$0" -S "$1" send-keys -t worker Enter && exec sleep 60`
	person := exec.Command("/bin/sh", "-c", script, term.Tmux, term.Socket)
	if err := person.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = person.Process.Kill(); _ = person.Wait() })
	if _, ok, err := placeByAncestry(pidIn(t, outside), readLineage, member); err != nil || ok {
		t.Errorf("a person's shell typing into the window from outside was placed %v, %v; want nobody's", ok, err)
	}
	if m, ok, err := placeByAncestry(pidIn(t, typed), readLineage, member); err != nil || !ok || m.Group != worker.Group() {
		t.Errorf("a command the person typed into the worker's window was placed %+v, %v, %v; want the worker's", m, ok, err)
	}

	// A person attached to the worker's server: the attach runs in a
	// terminal of the person's own, a second server, so it has a real one.
	personal := agents.Terminal{Tmux: term.Tmux, Socket: filepath.Join(standin.SocketDir(t), "p")}
	tmuxIn(t, personal, "-f", os.DevNull, "new-session", "-d", "-s", "person", "--",
		"env", "-u", "TMUX", term.Tmux, "-S", term.Socket, "attach-session", "-t", "worker")
	t.Cleanup(func() { _ = exec.Command(personal.Tmux, "-S", personal.Socket, "kill-server").Run() })
	deadline := time.Now().Add(30 * time.Second)
	var client string
	for client == "" {
		client = tmuxIn(t, term, "list-clients", "-F", "#{client_pid}")
		if client == "" && !time.Now().Before(deadline) {
			t.Fatal("the person's attach never became a client of the worker's server")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pid, err := strconv.Atoi(strings.Fields(client)[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := placeByAncestry(pid, readLineage, member); err != nil || ok {
		t.Errorf("the client a person attached with was placed %v, %v; want nobody's", ok, err)
	}
}
