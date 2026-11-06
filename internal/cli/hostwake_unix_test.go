//go:build unix

package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/procgroup"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// paneHost is a coordinator's host in a tmux pane of the test's own: a
// server on a socket in a short directory, whose one pane runs a program
// that appends every line typed into it to a file, which is how the test
// reads what reached the host's input. Such a program reads its terminal
// line by line, so a line is in the file once Enter was typed after it.
type paneHost struct {
	tmux, socket, pane, pid, start, dir, typed string
}

// startPaneHost starts a pane running cat as the host.
func startPaneHost(t *testing.T) paneHost {
	t.Helper()
	p := newPane(t)
	return p.run(t, nil, "/bin/sh", "-c", "exec cat >> "+procgroup.ShellQuote(p.typed))
}

// newPane names a pane's server and files, and starts nothing.
func newPane(t *testing.T) paneHost {
	t.Helper()
	tmux := standin.Tmux(t)
	dir := standin.SocketDir(t)
	return paneHost{tmux: tmux, socket: filepath.Join(dir, "s"), dir: dir, typed: filepath.Join(dir, "typed")}
}

// run starts p's server with one pane running argv, with environment
// set in it beside what tmux sets, and ends the server when the test ends.
func (p paneHost) run(t *testing.T, environment []string, argv ...string) paneHost {
	t.Helper()
	args := []string{"-S", p.socket, "new-session", "-d", "-x", "200", "-y", "40"}
	for _, variable := range environment {
		args = append(args, "-e", variable)
	}
	if raw, err := exec.Command(p.tmux, append(append(args, "--"), argv...)...).CombinedOutput(); err != nil {
		t.Fatalf("starting the host's pane: %v: %s", err, raw)
	}
	t.Cleanup(func() { _ = exec.Command(p.tmux, "-S", p.socket, "kill-server").Run() })
	raw, err := exec.Command(p.tmux, "-S", p.socket, "display-message", "-p", "#{pid} #{start_time} #{pane_id}").Output()
	fields := strings.Fields(string(raw))
	if err != nil || len(fields) != 3 {
		t.Fatalf("reading the host's pane: %v: %q", err, raw)
	}
	p.pid, p.start, p.pane = fields[0], fields[1], fields[2]
	return p
}

// paneEnvironment is what a pane's program needs set to run this binary
// as assistant against h: the home, a HOME of the test's own, so the
// settings file named is not the user's, and a PATH that finds tmux.
func (p paneHost) paneEnvironment(h *home.Home) []string {
	return []string{home.Var + "=" + h.Root(), "HOME=" + p.dir, "ENV=", "PATH=" + filepath.Dir(p.tmux) + ":/usr/bin:/bin"}
}

// startInstalledPaneHost starts a pane whose host is this binary as
// paneHostMain: it runs assistant --json host install against h as its
// child, as a host's own shell tool runs a command, keeping the pane's
// foreground, and then appends what is typed into the pane to p.typed. It
// returns the pane and the installer's answer, decoded loosely, so a member
// it does not carry reads empty.
func startInstalledPaneHost(t *testing.T, h *home.Home) (paneHost, map[string]any) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := newPane(t)
	p = p.run(t, append(p.paneEnvironment(h), paneHostVar+"="+p.dir), executable)
	installed := filepath.Join(p.dir, paneHostInstalled)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if raw, err := os.ReadFile(installed); err == nil {
			var answer map[string]any
			if err := json.Unmarshal(raw, &answer); err != nil {
				t.Fatalf("decoding the pane host's host install: %v\n%s", err, raw)
			}
			return p, answer
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the pane host wrote no host install answer within 30s")
		}
		time.Sleep(20 * time.Millisecond)
	}
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

// tmuxRun runs one tmux client on the host's server.
func (p paneHost) tmuxRun(t *testing.T, args ...string) {
	t.Helper()
	if raw, err := exec.Command(p.tmux, append([]string{"-S", p.socket}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("tmux %q: %v: %s", args, err, raw)
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
// The host is a pane running this binary as a stand-in host, which runs
// assistant host install as its child, as a host's shell tool runs a
// command inside the pane, and then appends what is typed into the pane to
// a file, so what reaches its input is read back rather than stated. The
// installer prints the notify.command line; the home is served with that line configured and
// a one-second supervision interval, and nothing else: no listen, no await.
// A delivery task's worker is launched and ended through the client's own
// message. The worker's own output before that may queue a wake of its own,
// which is typed like any other, so the notice about the exit is told apart
// by its wake rather than by the pane being empty: once the task reads
// Unknown from the dead terminal, the loop queues a terminal-gone wake about
// it, and within one supervision interval, plus what one run of the notify
// command takes, that wake's notification is settled sent, which the
// notifier records only after host wake exited zero, and the pane holds one
// more line than it did before the worker was ended. Every line typed is
// the notice, which names the drain the coordinator answers with.
func TestAWorkerExitWakesACoordinatorInATmuxPaneWithNoListen(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	requireSweepClears(t)
	principles.Cite(t, principles.P10)
	h := newHome(t)
	subject := newSubject(t)
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}

	pane, installed := startInstalledPaneHost(t, h)
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
	before := len(pane.typedLines())

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
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	for {
		if sent, ok := exitNoticeSent(t, records, id); ok {
			typed := pane.typedLines()
			if len(typed) <= before {
				t.Fatalf("the exit's wake %d was recorded sent and the pane holds %d lines, as it did before the worker ended", sent, len(typed))
			}
			for _, line := range typed {
				if line != machine.HostWakeNotice() {
					t.Fatalf("the host's pane was typed %q, want only the notice naming the drain", typed)
				}
			}
			t.Logf("the exit's wake %d was typed into the host %s after the worker's task read dead", sent, time.Since(ended).Round(time.Millisecond))
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no notice about the worker's exit reached the host's pane within %s, with %s configured as notify.command", interval+oneRun, wake)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// exitNoticeSent is the queued terminal-gone wake about task id whose
// notification is settled sent, if there is one yet.
func exitNoticeSent(t *testing.T, records *store.Store, id string) (int64, bool) {
	t.Helper()
	queued, err := records.QueuedWakes(t.Context(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range queued {
		if w.TaskID != id || w.Kind != string(supervisor.KindTerminalGone) {
			continue
		}
		n, err := records.Notification(t.Context(), w.ID)
		if err == nil && n.Outcome == store.Known(store.NotificationSent) {
			return w.ID, true
		}
	}
	return 0, false
}

// wakePane runs the wake line's command with flags as given, through cli.Run
// as the service's notify command would run it, and returns its exit.
func wakePane(t *testing.T, flags ...string) invocation {
	t.Helper()
	return runArgs(t, t.TempDir(), append([]string{"host", "wake"}, flags...)...)
}

// The host wake types only into the pane host install read: a server on
// the socket that is not the process it read, one that is that process but
// did not start when the one it read did, which is a later server given a
// reused process identifier, or a pane whose program is not the one it
// read, is refused with nothing typed. So is the pane read, while tmux has
// its input disabled (select-pane -d), where a paste reaches nothing and
// tmux still answers success. The same command with what was read, the
// input enabled again, types the notice once, as the positive control that
// the refusals were not refusing everything.
func TestTheHostWakeTypesOnlyIntoThePaneTheInstallerRead(t *testing.T) {
	t.Parallel()
	pane := startPaneHost(t)
	base := []string{"--tmux", pane.tmux, "--socket", pane.socket, "--pane", pane.pane}
	started, err := strconv.ParseInt(pane.start, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	earlier := strconv.FormatInt(started-1, 10)
	for _, wrong := range [][]string{
		{"--server-pid", "1", "--server-start", pane.start, "--program", "cat"},
		{"--server-pid", pane.pid, "--server-start", earlier, "--program", "cat"},
		{"--server-pid", pane.pid, "--server-start", pane.start, "--program", "claude"},
	} {
		got := wakePane(t, append(base, wrong...)...)
		if got.code != machine.ExitFailure || !strings.Contains(got.stderr, "nothing was typed") {
			t.Fatalf("host wake %q exited %s, want an operational failure saying nothing was typed:\n%s%s", wrong, got.code, got.stdout, got.stderr)
		}
	}
	read := append(base, "--server-pid", pane.pid, "--server-start", pane.start, "--program", "cat")
	pane.tmuxRun(t, "select-pane", "-d", "-t", pane.pane)
	if got := wakePane(t, read...); got.code != machine.ExitFailure || !strings.Contains(got.stderr, "nothing was typed") {
		t.Fatalf("host wake into a pane whose input tmux disabled exited %s, want an operational failure saying nothing was typed:\n%s%s", got.code, got.stdout, got.stderr)
	}
	pane.tmuxRun(t, "select-pane", "-e", "-t", pane.pane)
	got := wakePane(t, read...)
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

// host install run with a pane's tmux variables but outside that pane,
// as a program started from the pane and running elsewhere inherits them,
// offers no host wake and says why: the variables name where its starter
// ran, not where it runs, and the pane's program is not its host. Here that
// program is this test process, which the pane's tmux server did not start.
// The same pane's own host, run by this binary as a stand-in that runs the
// installer as its child inside the pane, is offered the line, as the
// positive control that the refusal is not refusing every pane.
func TestHostInstallOutsideThePaneItsVariablesNameOffersNoWake(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	pane, inside := startInstalledPaneHost(t, h)
	if wake, _ := inside["wake_command"].(string); wake == "" {
		t.Fatalf("host install run by the pane's own host offered no host wake: %v", inside)
	}
	vars := map[string]string{
		"TMUX":      pane.socket + "," + pane.pid + ",0",
		"TMUX_PANE": pane.pane,
		"PATH":      filepath.Dir(pane.tmux) + ":/usr/bin:/bin",
		"HOME":      pane.dir,
	}
	var outside machine.HostInstall
	installWith(t, h, func(name string) string { return vars[name] }, &outside)
	if outside.WakeCommand != "" || !strings.Contains(outside.WakeAbsent, "does not descend from pane "+pane.pane) || outside.WakeInstallerForeground {
		t.Fatalf("host install outside the pane its variables name answered %+v, want no wake and that it is not in the pane", outside)
	}
}

// Ending the notify command ends every tmux client host wake started, P15's
// "ended together with everything it started": the notifier ends a notify
// command it gave up on, at its bound or as the service stops, by sending
// its process group SIGKILL, which leaves host wake no moment to end a
// client itself, so a client is ended only if that signal reaches it.
//
// The tmux server is stopped with SIGSTOP, so host wake's first client
// connects and waits on an answer that does not come. The wake line runs
// through stages.RunOperatorCommand, the notifier's own call, and is ended
// once that client runs. Each client is started through a wrapper that
// writes its own process identifier and then execs tmux, so each is looked
// for afterwards by its identifier, and none may still run five seconds
// after the call returned, half of the client's own ten-second read bound,
// which a client left without host wake has nobody to enforce.
func TestEndingTheNotifyCommandEndsTheHostWakesTmuxClients(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	pane := startPaneHost(t)
	dir := t.TempDir()
	clients, wrapper := filepath.Join(dir, "clients"), filepath.Join(dir, "tmux")
	script := "#!/bin/sh\necho $$ >> " + procgroup.ShellQuote(clients) + "\nexec " + procgroup.ShellQuote(pane.tmux) + ` "$@"` + "\n"
	if err := procgroup.WriteProgram(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	server, err := strconv.Atoi(pane.pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(server, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	// Registered after startPaneHost's, so it runs first and the server
	// answers the kill-server that ends it.
	t.Cleanup(func() { _ = syscall.Kill(server, syscall.SIGCONT) })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join([]string{procgroup.ShellQuote(executable), "host", "wake",
		"--tmux", procgroup.ShellQuote(wrapper), "--socket", procgroup.ShellQuote(pane.socket), "--pane", pane.pane,
		"--server-pid", pane.pid, "--server-start", pane.start, "--program", "cat"}, " ")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan stages.OperatorResult, 1)
	go func() {
		done <- stages.RunOperatorCommand(ctx, stages.OperatorCommand{Line: line, Dir: dir, Grace: time.Second})
	}()
	started := func() []int {
		raw, _ := os.ReadFile(clients)
		var pids []int
		for _, field := range strings.Fields(string(raw)) {
			if pid, err := strconv.Atoi(field); err == nil {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	deadline := time.Now().Add(30 * time.Second)
	for len(started()) == 0 {
		if !time.Now().Before(deadline) {
			cancel()
			t.Fatalf("host wake started no tmux client within 30s: %+v", <-done)
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if res := <-done; res.Exited && res.Code == 0 {
		t.Fatalf("host wake answered success against a stopped tmux server: %+v", res)
	}

	gone := time.Now().Add(5 * time.Second)
	for _, pid := range started() {
		for syscall.Kill(pid, 0) == nil {
			if !time.Now().Before(gone) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("tmux client %d was still running 5s after the notify command was ended, with no host wake left to end it", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// host install typed at the prompt of a shell in the coordinator's pane,
// the way an operator runs a command, offers no host wake: the shell hands
// the pane's terminal to the command it runs, so the installer leads the
// pane's foreground process group, and the program tmux names for the pane
// is the installer itself, which a host started there afterwards never
// matches; it says so instead. The same installer run by a program that
// leads the pane's foreground and runs it as a child without handing it the
// terminal, as a host's shell tool can, is offered the line, naming that program and not the installer,
// as the positive control that the refusal is not refusing every pane.
//
// The pane runs an interactive shell, and the installer is this test binary
// answering host verbs as assistant does (TestMain), typed into that shell
// with send-keys; what it printed is read back from a file.
func TestHostInstallTypedAtThePanesShellOffersNoWakeNamingItself(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := newHome(t)
	pane := newPane(t)
	pane = pane.run(t, pane.paneEnvironment(h), "/bin/sh", "-i")

	// install types command at the pane's shell, with its output and then its
	// exit status written to files, and returns the output once the status
	// is there.
	install := func(name, command string) string {
		t.Helper()
		out, status := filepath.Join(pane.dir, name+".out"), filepath.Join(pane.dir, name+".status")
		line := command + " > " + procgroup.ShellQuote(out) + " 2>&1; echo $? > " + procgroup.ShellQuote(status)
		pane.tmuxRun(t, "send-keys", "-t", pane.pane, "-l", line)
		pane.tmuxRun(t, "send-keys", "-t", pane.pane, "Enter")
		deadline := time.Now().Add(30 * time.Second)
		for {
			if code, err := os.ReadFile(status); err == nil && strings.HasSuffix(string(code), "\n") {
				printed, _ := os.ReadFile(out)
				if strings.TrimSpace(string(code)) != "0" {
					t.Fatalf("%s exited %s:\n%s", command, code, printed)
				}
				return string(printed)
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("%s did not finish within 30s", command)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	commandLine := func(printed string) string {
		for _, line := range strings.Split(printed, "\n") {
			if rest, ok := strings.CutPrefix(line, "Command: "); ok {
				return rest
			}
		}
		return ""
	}
	installer := procgroup.ShellQuote(executable) + " host install"
	self := "--program " + procgroup.ShellQuote(filepath.Base(executable))

	typed := install("typed", installer)
	if line := commandLine(typed); line != "" {
		t.Fatalf("host install typed at the pane's shell offered %q, which names the installer as the pane's program:\n%s", line, typed)
	}
	if why := machine.HostInstallWakePhrase(machine.HostInstall{WakeInstallerForeground: true}).String(); !strings.Contains(typed, why) {
		t.Fatalf("host install typed at the pane's shell did not say it led the pane's foreground (%q):\n%s", why, typed)
	}

	byHost := install("host", "/bin/sh -c "+procgroup.ShellQuote(installer+"; :"))
	if line := commandLine(byHost); line == "" || strings.HasSuffix(line, self) {
		t.Fatalf("host install run by the program leading the pane offered %q, want a line naming that program:\n%s", line, byHost)
	}
}
