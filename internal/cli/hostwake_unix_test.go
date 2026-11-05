//go:build unix

package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// paneHost is a coordinator's host in a tmux pane of the test's own: a
// server on a socket in a short directory, whose one pane runs cat appending
// every line typed into it to a file, which is how the test reads what
// reached the host's input. cat reads its terminal line by line, so a line
// is in the file once Enter was typed after it.
type paneHost struct {
	tmux, socket, pane, pid, typed string
}

// startPaneHost starts the pane, and ends its server when the test ends.
func startPaneHost(t *testing.T) paneHost {
	t.Helper()
	tmux := standin.Tmux(t)
	dir := standin.SocketDir(t)
	p := paneHost{tmux: tmux, socket: filepath.Join(dir, "s"), typed: filepath.Join(dir, "typed")}
	if raw, err := exec.Command(tmux, "-S", p.socket, "new-session", "-d", "-x", "200", "-y", "40", "--",
		"/bin/sh", "-c", "exec cat >> "+p.typed).CombinedOutput(); err != nil {
		t.Fatalf("starting the host's pane: %v: %s", err, raw)
	}
	t.Cleanup(func() { _ = exec.Command(tmux, "-S", p.socket, "kill-server").Run() })
	raw, err := exec.Command(tmux, "-S", p.socket, "display-message", "-p", "#{pid} #{pane_id}").Output()
	fields := strings.Fields(string(raw))
	if err != nil || len(fields) != 2 {
		t.Fatalf("reading the host's pane: %v: %q", err, raw)
	}
	p.pid, p.pane = fields[0], fields[1]
	return p
}

// environment is what a command run by the host from inside the pane sees:
// tmux's two variables as tmux sets them, a PATH that finds tmux, and HOME.
func (p paneHost) environment(userHome string) func(string) string {
	vars := map[string]string{
		"TMUX":      p.socket + "," + p.pid + ",0",
		"TMUX_PANE": p.pane,
		"PATH":      filepath.Dir(p.tmux) + ":/usr/bin:/bin",
		"HOME":      userHome,
	}
	return func(name string) string { return vars[name] }
}

// installFromPane runs assistant host install as the host would from inside
// the pane, through cli.Run with the pane's environment, and returns its
// document decoded loosely, so a member it does not carry reads empty.
func installFromPane(t *testing.T, h *home.Home, p paneHost) map[string]any {
	t.Helper()
	var answer map[string]any
	installWith(t, h, p.environment(filepath.Dir(p.socket)), &answer)
	return answer
}

// installWith runs assistant --json host install through cli.Run with the
// environment getenv answers, and decodes its document into out.
func installWith(t *testing.T, h *home.Home, getenv func(string) string, out any) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := cli.Run(t.Context(), cli.Environment{
		Args:       []string{"--home", h.Root(), "--json", "host", "install"},
		Stdout:     &stdout,
		Stderr:     &stderr,
		Getenv:     getenv,
		WorkingDir: dir,
		Executable: executable,
		Version:    "assistant (test)",
	})
	if code != machine.ExitOK {
		t.Fatalf("assistant host install exited %s:\n%s%s", code, stdout.String(), stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		t.Fatalf("decoding assistant host install: %v\n%s", err, stdout.String())
	}
}

// typedLines is what reached the host's input so far, one entry per line.
func (p paneHost) typedLines() []string {
	raw, err := os.ReadFile(p.typed)
	if err != nil {
		return nil
	}
	return strings.FieldsFunc(string(raw), func(r rune) bool { return r == '\n' })
}

// A worker's exit wakes a coordinator whose host runs in a tmux pane, with
// no listen held and no wait armed: PRD section 8's host wake, which keeps
// P10's supervision cycle alive when the coordinator's own wait is gone.
//
// The host is a pane running cat, so what reaches its input is read back
// from a file rather than stated. assistant host install, run with the
// pane's environment as a command the host started there would be, prints
// the notify.command line; the home is served with that line configured and
// a one-second supervision interval, and nothing else: no listen, no await.
// A delivery task's worker is launched and ended through the client's own
// message. Nothing has been typed into the pane before the worker ends;
// once its task reads Unknown from the dead terminal, the host wake's prompt
// is the pane's next line within one supervision interval, plus what one run
// of the notify command takes, and it names the drain the coordinator
// answers with.
func TestAWorkerExitWakesACoordinatorInATmuxPaneWithNoListen(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	requireSweepClears(t)
	principles.Cite(t, principles.P10)
	pane := startPaneHost(t)
	h := newHome(t)
	subject := newSubject(t)
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}

	installed := installFromPane(t, h, pane)
	wake, _ := installed["wake_command"].(string)
	if wake == "" {
		t.Fatalf("assistant host install from inside a tmux pane printed no host wake line, so nothing the service runs can wake the host: %v", installed)
	}
	encoded, err := json.Marshal(wake)
	if err != nil {
		t.Fatal(err)
	}
	config := `{"supervision_beacon_interval": "1s", "notify": {"command": ` + string(encoded) + `}}` + "\n"
	if err := os.WriteFile(h.ConfigFile(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	serveCoordinated(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	repository := repositoryOf(t, h, subject)

	var created machine.TaskCreated
	coordinate(t, h, subject, &created, "create", "--shape", "delivery", "--project", "one",
		"--instructions", "Do the work the coordinator asked for.", "--repository", repository, "--branch", "task/one")
	id := created.Record.ID
	waitSessions(t, h, id, 1)
	if typed := pane.typedLines(); len(typed) != 0 {
		t.Fatalf("the host was typed into before any worker ended: %q", typed)
	}

	var messaged machine.TaskMessaged
	coordinate(t, h, subject, &messaged, "message", id, "--text", endWorker)
	deadline := time.Now().Add(30 * time.Second)
	for {
		state := taskOf(t, h, subject, id).State
		if state.State == taskstate.Unknown && state.Basis.Process == taskstate.Dead {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the ended worker's task never read Unknown from a dead terminal: %+v", state)
		}
		time.Sleep(50 * time.Millisecond)
	}
	ended := time.Now()

	// One supervision interval for the loop to record the wake, and the
	// bound one run of the command is given: the test binary starting as
	// the command under the race detector, and its two tmux clients.
	const interval, oneRun = time.Second, 10 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), interval+oneRun)
	defer cancel()
	for {
		if typed := pane.typedLines(); len(typed) > 0 {
			if len(typed) != 1 || !strings.Contains(typed[0], "coordinator wakes") {
				t.Fatalf("the host's pane was typed %q, want the one prompt naming the drain", typed)
			}
			t.Logf("the host was woken %s after the worker's task read dead", time.Since(ended).Round(time.Millisecond))
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no wake reached the host's pane within %s of the worker's exit, with %s configured as notify.command", interval+oneRun, wake)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// wakePane runs the wake line's command with flags as given, through cli.Run
// as the service's notify command would run it, and returns its exit.
func wakePane(t *testing.T, flags ...string) invocation {
	t.Helper()
	return runArgs(t, t.TempDir(), append([]string{"host", "wake"}, flags...)...)
}

// The host wake types only into the pane host install read: a server on
// the socket that is not the process it read, or a pane whose program is
// not the one it read, is refused with nothing typed, and the same command
// with what was read types the notice once, as the positive control that
// the refusals were not refusing everything.
func TestTheHostWakeTypesOnlyIntoThePaneTheInstallerRead(t *testing.T) {
	t.Parallel()
	pane := startPaneHost(t)
	base := []string{"--tmux", pane.tmux, "--socket", pane.socket, "--pane", pane.pane}
	for _, wrong := range [][]string{
		{"--server-pid", "1", "--program", "cat"},
		{"--server-pid", pane.pid, "--program", "claude"},
	} {
		got := wakePane(t, append(base, wrong...)...)
		if got.code != machine.ExitFailure || !strings.Contains(got.stderr, "nothing was typed") {
			t.Fatalf("host wake %q exited %s, want an operational failure saying nothing was typed:\n%s%s", wrong, got.code, got.stdout, got.stderr)
		}
	}
	got := wakePane(t, append(base, "--server-pid", pane.pid, "--program", "cat")...)
	if got.code != machine.ExitOK {
		t.Fatalf("host wake with what was read exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(pane.typedLines()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if typed := pane.typedLines(); len(typed) != 1 || typed[0] != machine.HostWakeNotice() {
		t.Fatalf("the pane was typed %q, want the notice once, after two refusals typed nothing", typed)
	}
}

// host install run outside a tmux pane offers no host wake and says why,
// and still answers with the stop guard's line.
func TestHostInstallOutsideTmuxOffersNoHostWake(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	userHome := t.TempDir()
	var answer machine.HostInstall
	installWith(t, h, func(name string) string {
		if name == "HOME" {
			return userHome
		}
		return ""
	}, &answer)
	if answer.WakeCommand != "" || !strings.Contains(answer.WakeAbsent, "tmux pane") || answer.Line == "" {
		t.Fatalf("host install outside tmux answered %+v, want the stop line, no wake and why", answer)
	}
}
