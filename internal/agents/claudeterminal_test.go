package agents_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/principles"
)

// claudeTerminalEnv is a base environment for a Claude Code terminal worker
// over the stand-in: a PATH, a HOME of the test's own, and the stand-in key.
func claudeTerminalEnv(t *testing.T) []string {
	t.Helper()
	return withStandinKey([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()})
}

// openClaudeTerminal starts a Claude Code terminal worker over the
// stand-in's interactive mode on a fresh terminal, and closes it when the
// test ends. opts come after the defaults, so a test may replace the base
// environment.
func openClaudeTerminal(t *testing.T, script standin.Script, spec agents.WorkerSpec,
	opts ...agents.ClaudeOption) (agents.TerminalWorker, agents.Terminal, *standin.Agent) {
	t.Helper()
	term := terminalFor(t)
	agent := standin.New(t, script, append([]agents.ClaudeOption{
		agents.WithTerminationGrace(300 * time.Millisecond),
		agents.WithBaseEnvironment(claudeTerminalEnv(t)),
	}, opts...)...)
	worker, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, spec)
	if err != nil {
		t.Fatalf("starting a Claude Code terminal worker: %v", err)
	}
	t.Cleanup(worker.Close)
	return worker, term, agent
}

// awaitInteractive waits until the stand-in has recorded an interactive
// worker whose call satisfies ok, and returns that call.
func awaitInteractive(t *testing.T, agent *standin.Agent, ok func(standin.Call) bool) standin.Call {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, call := range agent.Calls() {
			if call.Interactive && ok(call) {
				return call
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the interactive worker never recorded what was waited for; it recorded %v", agent.Calls())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// answered is a predicate on a call that has answered at least n messages.
func answered(n int) func(standin.Call) bool {
	return func(c standin.Call) bool { return len(c.Lines) >= n }
}

// typeAsAPerson types text into the worker's pane straight through tmux,
// around the adapter, as a person attached to it would paste it: one
// bracketed paste and Enter.
func typeAsAPerson(t *testing.T, term agents.Terminal, text string) {
	t.Helper()
	cmd := exec.Command(term.Tmux, "-S", term.Socket,
		"load-buffer", "-b", "person", "-", ";",
		"paste-buffer", "-p", "-d", "-b", "person", "-t", "worker", ";",
		"send-keys", "-t", "worker", "Enter")
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("typing into the pane as a person: %v: %s", err, out)
	}
}

// A Claude Code terminal worker reaches its conversation with nobody at the
// pane: the instructions are its first message, its command line is the
// interactive interface's with the grant a pipe worker gets, and its state
// lives in a directory of its own that is gone once the worker is. The
// stand-in opens on a first-run screen unless that directory answers every
// question the screen would ask, so reaching the first message is what
// shows the seeding answered them.
func TestAClaudeTerminalWorkerReachesItsConversationWithNobodyThere(t *testing.T) {
	const route = "/opt/assistant/bin/assistant worker"
	spec := workerSpec(t)
	spec.Route = route
	home := t.TempDir()
	// The operator's own state file and configuration directory, which the
	// worker must neither read nor write: either would block the stand-in
	// on its theme screen.
	userConfig := t.TempDir()
	blocking := []byte(`{"hasCompletedOnboarding":false}`)
	for _, dir := range []string{home, userConfig} {
		if err := os.WriteFile(filepath.Join(dir, ".claude.json"), blocking, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := withStandinKey([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CLAUDE_CONFIG_DIR=" + userConfig})
	worker, _, agent := openClaudeTerminal(t, standin.Script{}, spec, agents.WithBaseEnvironment(env))

	call := awaitInteractive(t, agent, answered(1))
	if call.Lines[0] != spec.Instructions {
		t.Errorf("the first message was %q, want the instructions %q", call.Lines[0], spec.Instructions)
	}
	if call.ConfigDir == "" || call.ConfigDir == userConfig {
		t.Errorf("the worker read its state from %q, want a directory of its own", call.ConfigDir)
	}
	if slices.Contains(call.Args, "--print") || call.Session() != "" {
		t.Errorf("the terminal command line %q carries --print or --resume", call.Args)
	}
	if !slices.Contains(call.Args, "--ax-screen-reader") {
		t.Errorf("the terminal command line %q does not draw the flat interface", call.Args)
	}
	if got := call.Args[len(call.Args)-2:]; got[0] != "--" || got[1] != spec.Instructions {
		t.Errorf("the command line ends %q, want the instructions after --", got)
	}

	// The pipe worker's grant, from the same runner and the same route.
	pipe, err := agents.OpenWorker(t.Context(), agent.Runner(), spec)
	if err != nil {
		t.Fatalf("starting the pipe worker to compare grants with: %v", err)
	}
	defer pipe.Close()
	var pipeCall standin.Call
	deadline := time.Now().Add(30 * time.Second)
	for pipeCall.Args == nil && time.Now().Before(deadline) {
		for _, c := range agent.Calls() {
			if c.Stream {
				pipeCall = c
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	want := workerGrant
	want.allowed += ",Bash(" + route + ":*)"
	if got := grantOf(pipeCall.Args); got != want {
		t.Errorf("the pipe worker's grant is %+v, want %+v", got, want)
	}
	if got := grantOf(call.Args); got != want {
		t.Errorf("the terminal worker's grant is %+v, want the pipe worker's %+v", got, want)
	}

	worker.Close()
	if _, err := os.Stat(call.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the worker's configuration directory outlived it: %v", err)
	}
	for _, dir := range []string{home, userConfig} {
		got, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
		if err != nil || string(got) != string(blocking) {
			t.Errorf("the operator's state file in %s reads %q (%v) after the worker, want it untouched", dir, got, err)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 1 {
			t.Errorf("the worker wrote into the operator's %s: %v", dir, entries)
		}
	}
}

// Each terminal worker opens a conversation of its own under an identifier
// drawn for it, and resumes none, so no worker's memory is another session's.
// That is the fixer side of P4 for a worker: its memory is its own and lasts
// as long as it runs.
func TestEachClaudeTerminalWorkerOpensAConversationOfItsOwn(t *testing.T) {
	principles.Cite(t, principles.P4)
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	_, _, first := openClaudeTerminal(t, standin.Script{}, workerSpec(t))
	_, _, second := openClaudeTerminal(t, standin.Script{}, workerSpec(t))
	a := awaitInteractive(t, first, answered(1))
	b := awaitInteractive(t, second, answered(1))
	ids := []string{}
	for _, call := range []standin.Call{a, b} {
		id := ""
		if i := slices.Index(call.Args, "--session-id"); i >= 0 && i+1 < len(call.Args) {
			id = call.Args[i+1]
		}
		if !uuid.MatchString(id) {
			t.Errorf("the worker was started with session identifier %q, want a version 4 UUID", id)
		}
		if call.Session() != "" {
			t.Errorf("the worker resumed session %q", call.Session())
		}
		ids = append(ids, id)
	}
	if ids[0] == ids[1] {
		t.Errorf("two workers share the conversation %q", ids[0])
	}
}

// A send is one message however many lines it has, because it is one
// bracketed paste; and a send beginning with "!" or "/" reaches the
// conversation as a message, not as a shell command or a slash command.
// The positive controls are typed around the adapter: the same text pasted
// as a person would paste it lands as a shell command and a slash command,
// and a paste without the markers lands as one message per line, so the
// stand-in tells each shape apart and the adapter's are not its blindness.
func TestAClaudeTerminalSendIsOneMessageAndNeverACommand(t *testing.T) {
	worker, term, agent := openClaudeTerminal(t, standin.Script{}, workerSpec(t))
	sends := []string{"first line\nsecond line\n\nfourth line", "!touch typed-by-the-adapter", "/clear the context"}
	want := []string{sends[0], " " + sends[1], " " + sends[2]}
	for _, text := range sends {
		if err := worker.Send(t.Context(), text); err != nil {
			t.Fatalf("Send(%q): %v", text, err)
		}
	}
	call := awaitInteractive(t, agent, answered(1+len(want)))
	if got := call.Lines[1:]; !slices.Equal(got, want) {
		t.Errorf("the worker answered %q, want %q", got, want)
	}

	typeAsAPerson(t, term, "!touch typed-by-a-person")
	typeAsAPerson(t, term, "/clear")
	call = awaitInteractive(t, agent, func(c standin.Call) bool { return len(c.Shell) == 1 && len(c.Slash) == 1 })
	if call.Shell[0] != "touch typed-by-a-person" || call.Slash[0] != "/clear" {
		t.Errorf("a person's paste was taken as shell %q and slash %q", call.Shell, call.Slash)
	}
	if _, err := os.Stat(filepath.Join(call.Dir, "typed-by-the-adapter")); err == nil {
		t.Error("the adapter's send ran as a shell command")
	}

	// Pasted without the markers, as the scripted adapter's framing pastes,
	// every line break is an Enter.
	unmarked := exec.Command(term.Tmux, "-S", term.Socket,
		"load-buffer", "-b", "plain", "-", ";", "paste-buffer", "-d", "-b", "plain", "-t", "worker")
	unmarked.Stdin = strings.NewReader("one of two\ntwo of two\n")
	if out, err := unmarked.CombinedOutput(); err != nil {
		t.Fatalf("pasting without the markers: %v: %s", err, out)
	}
	call = awaitInteractive(t, agent, answered(1+len(want)+2))
	if got := call.Lines[len(call.Lines)-2:]; !slices.Equal(got, []string{"one of two", "two of two"}) {
		t.Errorf("a paste without the markers was answered as %q, want one message per line", got)
	}
}

// The Claude Code framing refuses, before anything is typed, text a
// terminal would take as keys rather than text, text that is not UTF-8, and
// text that submits nothing; a line break is the one control character it
// passes, as part of the message.
func TestAClaudeTerminalSendRefusesWhatATerminalWouldTakeAsKeys(t *testing.T) {
	worker, _, agent := openClaudeTerminal(t, standin.Script{}, workerSpec(t))
	for _, text := range []string{
		"a tab\there", "an escape \x1b[201~ ending the paste", "a carriage return\rhere",
		"a delete\x7f", "a next line\u0085", "\xff not UTF-8", " \n\t ", "",
	} {
		if err := worker.Send(t.Context(), text); !errors.Is(err, agents.ErrInvalidInvocation) {
			t.Errorf("Send(%q) answered %v, want ErrInvalidInvocation", text, err)
		}
	}
	if err := worker.Send(t.Context(), "after the refusals"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	call := awaitInteractive(t, agent, answered(2))
	if got := call.Lines; len(got) != 2 || got[1] != "after the refusals" {
		t.Errorf("the worker answered %q, want the instructions and the one send nothing refused", got)
	}
}

// A send waits for the turn running when it is made to end before it types,
// so nothing is typed into a running turn; and a send whose context ends
// first is refused with ErrTerminalNotReady, nothing typed. The stand-in
// records what was submitted to it during a turn, which is what fails here
// if the wait goes.
func TestAClaudeTerminalSendWaitsForTheTurnToEnd(t *testing.T) {
	const turn = 1500 * time.Millisecond
	script := standin.Script{Worker: &standin.WorkerScript{Turn: turn}}
	worker, _, agent := openClaudeTerminal(t, script, workerSpec(t))

	// The start returned with the instructions' turn running.
	short, cancel := context.WithTimeout(t.Context(), turn/5)
	err := worker.Send(short, "too impatient")
	cancel()
	if !errors.Is(err, agents.ErrTerminalNotReady) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a send whose context ended during the turn answered %v, want ErrTerminalNotReady and the deadline", err)
	}
	if err := worker.Send(t.Context(), "patient"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	awaitInteractive(t, agent, answered(2))
	// Long enough for a send typed into the turn to have been answered too.
	time.Sleep(turn)
	call := awaitInteractive(t, agent, answered(2))
	if !slices.Equal(call.Lines, []string{"work on the task", "patient"}) {
		t.Errorf("the worker answered %q, want the instructions and the patient send only", call.Lines)
	}
	if len(call.Queued) != 0 {
		t.Errorf("%q was typed into a running turn", call.Queued)
	}
}

// A start whose interface is not drawn within its context is refused with
// ErrTerminalNotReady, and the server, its socket and the worker's
// configuration directory go with it.
func TestAClaudeTerminalStartIsRefusedWhenNoInterfaceIsDrawn(t *testing.T) {
	script := standin.Script{Worker: &standin.WorkerScript{Startup: 30 * time.Second}}
	term := terminalFor(t)
	agent := standin.New(t, script, agents.WithTerminationGrace(300*time.Millisecond),
		agents.WithBaseEnvironment(claudeTerminalEnv(t)))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err := agents.OpenTerminalWorker(ctx, agent.Runner(), term, workerSpec(t))
	if !errors.Is(err, agents.ErrTerminalNotReady) {
		t.Fatalf("a start whose interface never drew answered %v, want ErrTerminalNotReady", err)
	}
	if _, err := os.Lstat(term.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket outlived the refused start: %v", err)
	}
	call := awaitInteractive(t, agent, func(standin.Call) bool { return true })
	if _, err := os.Stat(call.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the configuration directory outlived the refused start: %v", err)
	}
}

// Without a key in its environment a Claude Code terminal worker is refused
// before anything starts, by an error naming the variable, because the
// directory it would run with carries no login.
func TestAClaudeTerminalWorkerIsRefusedWithoutAKey(t *testing.T) {
	term := terminalFor(t)
	agent := standin.New(t, standin.Script{},
		agents.WithBaseEnvironment([]string{"PATH=" + os.Getenv("PATH"), agents.ClaudeKeyVariable + "="}))
	_, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, workerSpec(t))
	if !errors.Is(err, agents.ErrNoCredential) || !strings.Contains(err.Error(), agents.ClaudeKeyVariable) {
		t.Fatalf("a start without a key answered %v, want ErrNoCredential naming %s", err, agents.ClaudeKeyVariable)
	}
	if _, err := os.Lstat(term.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a server was started without a key: %v", err)
	}
	if calls := agent.Calls(); len(calls) != 0 {
		t.Errorf("a worker without a key started %d processes", len(calls))
	}
}

// Instructions too long for the command that starts the terminal are
// refused before anything starts, rather than by tmux in a message about
// something else.
func TestAClaudeTerminalRefusesInstructionsTooLongForTheCommand(t *testing.T) {
	term := terminalFor(t)
	agent := standin.New(t, standin.Script{}, agents.WithBaseEnvironment(claudeTerminalEnv(t)))
	spec := workerSpec(t)
	spec.Instructions = strings.Repeat("long instructions ", 1000)
	if _, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, spec); !errors.Is(err, agents.ErrInvalidInvocation) {
		t.Fatalf("%d bytes of instructions answered %v, want ErrInvalidInvocation", len(spec.Instructions), err)
	}
	if calls := agent.Calls(); len(calls) != 0 {
		t.Errorf("instructions too long started %d processes", len(calls))
	}
}

// The screen reader is held to screens Claude Code drew, copied from captures
// of version agents.ClaudeObservedVersion in a tmux pane with
// --ax-screen-reader. A line naming the model, and the warnings this
// machine's host added, were left out of each, and a conversation line
// quoting the footer was added above one, which is where the CLI draws its
// conversation.
func TestTheClaudeScreenReaderReadsScreensTheCLIDrew(t *testing.T) {
	for _, c := range []struct{ name, screen, want string }{
		{"starting", "[Screen Reader Mode: on via flag]\n", "not drawn"},
		{"workspace trust", "[Screen Reader Mode: on via flag]\nPermission Required: Accessing workspace:\n/work\n" +
			"Claude Code'll be able to read, edit, and execute files here.\nSecurity guide\n" +
			"y. Yes, I trust this folder\nn. No, exit\nEnter y/n:\nEnter to confirm · Esc to cancel\n", "not drawn"},
		{"api key", "[Screen Reader Mode: on via flag]\nDetected a custom API key in your environment\n" +
			"Do you want to use this API key?\ny. Yes\nn. No (recommended)\nEnter y/n:\nEnter to confirm · Esc to cancel\n", "not drawn"},
		{"running", "[Screen Reader Mode: on via flag]\nClaude Code v2.1.301\n/work\nyou: instructions v\n" +
			"claude: a partial reply\nCrafting…\naccept edits on (shift+tab to cycle)  ·  esc to interrupt\n" +
			"effort: medium · /effort\n$\n", "drawn"},
		{"done", "[Screen Reader Mode: on via flag]\nClaude Code v2.1.301\n/work\nyou: instructions v\n" +
			"claude: the reply\nBrewed for 8s · done 10:23 PM\naccept edits on (shift+tab to cycle)\n" +
			"effort: medium · /effort\n$\n", "ready"},
		{"quoted above", "you: what does accept edits on (shift+tab to cycle)  ·  esc to interrupt mean\n" +
			"claude: the reply\naccept edits on (shift+tab to cycle)\n$\n", "ready"},
		{"text in the input", "claude: the reply\nBrewed for 8s · done 10:23 PM\naccept edits on (shift+tab to cycle)\n" +
			"effort: medium · /effort\n$\u00a0  /probe-slash command\n", "drawn"},
		{"the message just sent still in the input", "you:  !touch probe-bang\nclaude: probe reply 15\n" +
			"Sautéed for 0s · done 10:50 PM\naccept edits on (shift+tab to cycle)\n" +
			"tmux focus-events off · add 'set -g focus-events on' to ~/.tmux.conf and reattach for focus tracking\n" +
			"$\u00a0  !touch probe-bang\n", "drawn"},
		{"just after a paste", "claude: probe reply 59\nWorked for 0s · done 10:53 PM\npaste again to expand\n" +
			"effort: medium · /effort\n$\n", "not drawn"},
		{"a notice below the footer", "claude: the reply\nWorked for 0s · done 10:53 PM\n" +
			"accept edits on (shift+tab to cycle)\n" +
			"tmux focus-events off · add 'set -g focus-events on' to ~/.tmux.conf and reattach for focus tracking\n" +
			"$\n\n\n", "ready"},
	} {
		if got := agents.ReadClaudeScreen(c.screen); got != c.want {
			t.Errorf("the %s screen reads %q, want %q", c.name, got, c.want)
		}
	}
}

// A send never types into an input a person left text in: it waits for the
// input to be empty, and is refused with ErrTerminalNotReady, nothing typed,
// when its context ends first. The person's text is then still theirs to
// send, and is answered as they wrote it.
func TestAClaudeTerminalSendLeavesAPersonsUnsentTextAlone(t *testing.T) {
	worker, term, agent := openClaudeTerminal(t, standin.Script{}, workerSpec(t))
	awaitInteractive(t, agent, answered(1))
	if err := worker.Send(t.Context(), "settle"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	awaitInteractive(t, agent, answered(2))
	half := exec.Command(term.Tmux, "-S", term.Socket, "send-keys", "-t", "worker", "-l", "half a thought")
	if out, err := half.CombinedOutput(); err != nil {
		t.Fatalf("typing as a person: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := worker.Send(ctx, "from the adapter"); !errors.Is(err, agents.ErrTerminalNotReady) {
		t.Fatalf("a send over a person's unsent text answered %v, want ErrTerminalNotReady", err)
	}
	enter := exec.Command(term.Tmux, "-S", term.Socket, "send-keys", "-t", "worker", "Enter")
	if out, err := enter.CombinedOutput(); err != nil {
		t.Fatalf("pressing Enter as a person: %v: %s", err, out)
	}
	call := awaitInteractive(t, agent, answered(3))
	if got := call.Lines[2]; got != "half a thought" {
		t.Errorf("the person's text was answered as %q, want it as they typed it", got)
	}
}
