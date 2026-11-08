package agents_test

import (
	"context"
	"errors"
	"fmt"
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

// draftAsAPerson pastes text into the worker's pane straight through tmux,
// around the adapter, as a person would paste a draft they have not sent:
// one bracketed paste and no Enter, so a draft of several lines sits in the
// input with its later lines on rows of their own.
func draftAsAPerson(t *testing.T, term agents.Terminal, text string) {
	t.Helper()
	cmd := exec.Command(term.Tmux, "-S", term.Socket,
		"load-buffer", "-b", "draft", "-", ";",
		"paste-buffer", "-p", "-d", "-b", "draft", "-t", "worker")
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pasting a draft into the pane as a person: %v: %s", err, out)
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

// A Claude Code terminal worker caches into a directory of its own, which
// is gone once the worker is. The environment is read from the running
// worker, by a command run in its pane, rather than from what the adapter
// says it built, and the directory is the one the adapter derives from the
// terminal rather than a path this test spells. The control is
// CLAUDE_CONFIG_DIR beside it, which the worker has carried since #254: a
// read that found nothing fails here rather than passing.
func TestAClaudeTerminalWorkerCachesIntoADirectoryOfItsOwn(t *testing.T) {
	spec := workerSpec(t)
	script := standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "run:", Run: true}},
	}}
	worker, term, _ := openClaudeTerminal(t, script, spec, agents.WithBaseEnvironment(
		withStandinKey([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "XDG_CACHE_HOME=/the/operator/cache"})))
	if err := worker.Send(t.Context(), "run: env > env.txt"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	env := awaitWorkerEnvironment(t, filepath.Join(spec.Dir, "env.txt"))

	cache := agents.ClaudeCacheFor(term)
	if got := env["XDG_CACHE_HOME"]; got != cache {
		t.Errorf("the worker caches into %q, want its own %q", got, cache)
	}
	if got := env["CLAUDE_CONFIG_DIR"]; got != agents.ClaudeConfigFor(term) {
		t.Errorf("the worker's configuration directory is %q, want %q", got, agents.ClaudeConfigFor(term))
	}
	info, err := os.Stat(cache)
	if err != nil {
		t.Fatalf("the worker's cache directory: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("the worker's cache directory is %v, want owner-only", perm)
	}

	worker.Close()
	for _, dir := range []string{cache, agents.ClaudeConfigFor(term)} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s outlived the worker: %v", dir, err)
		}
	}
}

// A start that cannot make its cache directory is refused, and takes with
// it the configuration directory it had just seeded and nothing else: what
// already stood where the cache directory goes is left exactly as it was.
// It is made here with a file in it, so a start that removed it would be
// removing content, which a test against an empty directory would miss.
func TestAClaudeTerminalStartRefusedByItsCacheDirectoryKeepsWhatStoodThere(t *testing.T) {
	term := terminalFor(t)
	cache := agents.ClaudeCacheFor(term)
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	kept, held := filepath.Join(cache, "kept"), "not this start's"
	if err := os.WriteFile(kept, []byte(held), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := standin.New(t, standin.Script{}, agents.WithTerminationGrace(300*time.Millisecond),
		agents.WithBaseEnvironment(claudeTerminalEnv(t)))

	_, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, workerSpec(t))
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("a start over a cache directory that already exists answered %v, want it refused", err)
	}
	if got, err := os.ReadFile(kept); err != nil || string(got) != held {
		t.Errorf("the refused start left %q (%v) in the cache directory, want %q untouched", got, err, held)
	}
	if _, err := os.Stat(agents.ClaudeConfigFor(term)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the configuration directory the refused start seeded outlived it: %v", err)
	}
	if calls := agent.Calls(); len(calls) != 0 {
		t.Errorf("the refused start ran the CLI %d times: %v", len(calls), calls)
	}
}

// awaitWorkerEnvironment reads the environment a command in the worker's
// pane wrote to path, as names to values.
func awaitWorkerEnvironment(t *testing.T, path string) map[string]string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(raw), "\n") {
			env := map[string]string{}
			for _, line := range strings.Split(string(raw), "\n") {
				if name, value, ok := strings.Cut(line, "="); ok {
					env[name] = value
				}
			}
			return env
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker's command never wrote its environment: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
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

// A Claude Code terminal worker starts on either credential the adapter
// accepts, and is handed exactly one: the API key when it is set and not
// empty, so a key beside a token wins, and the OAuth token otherwise. What
// the worker found is read off the stand-in's own record of its
// environment, which names the credential variables it saw set and holds no
// value, so the key winning is the worker not seeing the token at all
// rather than the adapter saying it chose the key.
func TestAClaudeTerminalWorkerStartsOnTheOneCredentialItIsHanded(t *testing.T) {
	key := agents.ClaudeKeyVariable + "=" + standinKey
	token := agents.ClaudeTokenVariable + "=" + standinToken
	for _, c := range []struct {
		name string
		set  []string
		want []string
	}{
		{"api key", []string{key}, []string{agents.ClaudeKeyVariable}},
		{"oauth token", []string{token}, []string{agents.ClaudeTokenVariable}},
		{"oauth token beside an empty key", []string{agents.ClaudeKeyVariable + "=", token}, []string{agents.ClaudeTokenVariable}},
		{"both, the key wins", []string{token, key}, []string{agents.ClaudeKeyVariable}},
		{"both, the key wins whichever comes first", []string{key, token}, []string{agents.ClaudeKeyVariable}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}, c.set...)
			spec := workerSpec(t)
			_, _, agent := openClaudeTerminal(t, standin.Script{}, spec, agents.WithBaseEnvironment(env))
			call := awaitInteractive(t, agent, answered(1))
			if call.Lines[0] != spec.Instructions {
				t.Errorf("the first message was %q, want the instructions %q", call.Lines[0], spec.Instructions)
			}
			if !slices.Equal(call.Credentials, c.want) {
				t.Errorf("the worker found %q set, want exactly %q", call.Credentials, c.want)
			}
		})
	}
}

// Without a key or a token in its environment a Claude Code terminal worker
// is refused before anything starts, by an error naming both variables,
// because the directory it would run with carries no login. A variable set
// to nothing counts as unset.
func TestAClaudeTerminalWorkerIsRefusedWithoutACredential(t *testing.T) {
	for _, c := range []struct {
		name string
		set  []string
	}{
		{"neither set", nil},
		{"both empty", []string{agents.ClaudeKeyVariable + "=", agents.ClaudeTokenVariable + "="}},
	} {
		t.Run(c.name, func(t *testing.T) {
			term := terminalFor(t)
			agent := standin.New(t, standin.Script{},
				agents.WithBaseEnvironment(append([]string{"PATH=" + os.Getenv("PATH")}, c.set...)))
			_, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, workerSpec(t))
			if !errors.Is(err, agents.ErrNoCredential) {
				t.Fatalf("a start without a credential answered %v, want ErrNoCredential", err)
			}
			for _, name := range []string{agents.ClaudeKeyVariable, agents.ClaudeTokenVariable} {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("the refusal %q does not name %s", err, name)
				}
			}
			if _, err := os.Lstat(term.Socket); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a server was started without a credential: %v", err)
			}
			if calls := agent.Calls(); len(calls) != 0 {
				t.Errorf("a worker without a credential started %d processes", len(calls))
			}
		})
	}
}

// A worker on the OAuth token keeps the token out of everything this
// package and the stand-in write down about it: the stand-in's record of
// the call, which is evidence off the wire, and the worker's capture, which
// is what a person peeking at it reads. The scan has a positive control on
// the same text, the worker's instructions and the message it was sent,
// which both must carry, so a scan that looked at nothing fails rather than
// passing.
func TestAClaudeTerminalWorkersTokenIsInNoRecordOrCapture(t *testing.T) {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), agents.ClaudeTokenVariable + "=" + standinToken}
	spec := workerSpec(t)
	worker, _, agent := openClaudeTerminal(t, standin.Script{}, spec, agents.WithBaseEnvironment(env))
	const sent = "a message after the instructions"
	if err := worker.Send(t.Context(), sent); err != nil {
		t.Fatalf("sending to a worker on the token: %v", err)
	}
	awaitInteractive(t, agent, answered(2))

	recorded := fmt.Sprintf("%+v", agent.Calls())
	captured := awaitWorkerOutput(t, worker, sent)
	for what, text := range map[string]string{"the recorded call": recorded, "the capture": captured} {
		if !strings.Contains(text, spec.Instructions) || !strings.Contains(text, sent) {
			t.Fatalf("%s carries neither what the worker was told nor what it was sent, so it shows nothing: %s", what, text)
		}
		if strings.Contains(text, standinToken) {
			t.Errorf("%s carries the token: %s", what, text)
		}
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

// cursorAt marks the row tmux reported the pane's cursor on in a recorded
// screen below: cursorScreen takes the mark out and gives the reader that
// row's index, as the tmux provider gives it the row the server answered
// with. A screen with no mark is one read with no cursor.
const cursorAt = "\u2038"

// cursorScreen is a recorded screen without its cursor mark, and the index
// of the row the mark was on, or -1 where there was none.
func cursorScreen(t *testing.T, screen string) (string, int) {
	t.Helper()
	cursor := -1
	for i, line := range strings.Split(screen, "\n") {
		if strings.Contains(line, cursorAt) {
			if cursor >= 0 {
				t.Fatalf("the screen marks two cursor rows, %d and %d", cursor, i)
			}
			cursor = i
		}
	}
	return strings.ReplaceAll(screen, cursorAt, ""), cursor
}

// The screen reader is held to screens Claude Code drew, copied from captures
// of version agents.ClaudeObservedVersion and, where a row says so, of
// 2.1.308 in a tmux pane with --ax-screen-reader. A line naming the model,
// and the warnings this machine's host added, were left out of each.
//
// The cursor's row is marked where the capture recorded it, which the four
// rows taken from the 2.1.308 captures of the cursor do. On every one of
// those the server had the cursor where the input's text ended: on the
// prompt's own row for an empty input, after the text for one line of it,
// and on the input's last row for several. The older captures recorded the
// screen alone, so their mark is that row, the one every capture that did
// record a cursor had it on.
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
			"effort: medium · /effort\n" + cursorAt + "$\n", "busy"},
		{"done", "[Screen Reader Mode: on via flag]\nClaude Code v2.1.301\n/work\nyou: instructions v\n" +
			"claude: the reply\nBrewed for 8s · done 10:23 PM\naccept edits on (shift+tab to cycle)\n" +
			"effort: medium · /effort\n" + cursorAt + "$\n", "ready"},
		{"quoted above", "you: what does accept edits on (shift+tab to cycle)  ·  esc to interrupt mean\n" +
			"claude: the reply\naccept edits on (shift+tab to cycle)\n" + cursorAt + "$\n", "ready"},
		{"text in the input", "claude: the reply\nBrewed for 8s · done 10:23 PM\naccept edits on (shift+tab to cycle)\n" +
			"effort: medium · /effort\n" + cursorAt + "$\u00a0  /probe-slash command\n", "drawn"},
		{"the message just sent still in the input", "you:  !touch probe-bang\nclaude: probe reply 15\n" +
			"Sautéed for 0s · done 10:50 PM\naccept edits on (shift+tab to cycle)\n" +
			"tmux focus-events off · add 'set -g focus-events on' to ~/.tmux.conf and reattach for focus tracking\n" +
			cursorAt + "$\u00a0  !touch probe-bang\n", "drawn"},
		{"just after a paste", "claude: probe reply 59\nWorked for 0s · done 10:53 PM\npaste again to expand\n" +
			"effort: medium · /effort\n" + cursorAt + "$\n", "not drawn"},
		// Drawn by Claude Code 2.1.308 around the Escape key, a path replaced.
		{"running, 2.1.308", "[Screen Reader Mode: on via flag]\nClaude Code v2.1.308\nOpus 5.5 · API Usage Billing\n/work\n" +
			"you: LONGTURN please work for a long time\nclaude: working 0. working 1. working 2.\nWaddling…\n" +
			"accept edits on (shift+tab to cycle)  ·  esc to interrupt\neffort: medium · /effort\n" + cursorAt + "$\n", "busy"},
		{"running a tool, 2.1.308", "you: TOOLTURN: git -c alias.zz='!sleep 40' zz\ntool: Bash (git -c alias.zz='!sleep 40' zz)\n" +
			"Running…  (5s)\n(ctrl+b ctrl+b (twice) to run in background)\nHashing…\n" +
			"Tip: Use /memory to view and manage Claude memory\naccept edits on (shift+tab to cycle)  ·  esc to interrupt\n" +
			cursorAt + "$\n", "busy"},
		{"text typed into a running turn, 2.1.308", "you: LONGTURN second long turn\n" +
			"Tip: Use Plan Mode to prepare for a complex request before making changes. Press shift+tab twice to enable.\n" +
			"accept edits on (shift+tab to cycle)\neffort: medium · /effort\n" + cursorAt + "$\u00a0 half typed\n", "drawn"},
		{"interrupted, 2.1.308", "[Screen Reader Mode: on via flag]\nClaude Code v2.1.308\nOpus 5.5 · API Usage Billing\n/work\n" +
			"you: LONGTURN please work for a long time\nclaude: working 0. working 1. working 2. working 3.\n" +
			"Interrupted  · What should Claude do instead?\naccept edits on (shift+tab to cycle)\n" + cursorAt + "$\n", "ready"},
		{"interrupted over typed text, 2.1.308", "you: LONGTURN second long turn\nclaude: working 0. working 1.\n" +
			"Interrupted  · What should Claude do instead?\naccept edits on (shift+tab to cycle)\n" +
			"effort: medium · /effort\n" + cursorAt + "$\u00a0 half typed\n", "drawn"},
		{"interrupted on a tool, 2.1.308", "you: TOOLTURN: git -c alias.zz='!sleep 40' zz\n" +
			"tool error: Bash (git -c alias.zz='!sleep 40' zz)\nInterrupted  · What should Claude do instead?\n" +
			"accept edits on (shift+tab to cycle)\n" + cursorAt + "$\n", "ready"},
		{"interrupted before any reply, 2.1.308", "you: hello first\nclaude: ack 2: hello first\n" +
			"Cogitated for 0s · done 3:41 AM\naccept edits on (shift+tab to cycle)\n" +
			cursorAt + "$\u00a0 SLOWSTART nothing streams yet\n", "drawn"},
		{"a paste interrupted before any reply, 2.1.308", "claude: ack 2: hello first\nCogitated for 0s · done 3:41 AM\n" +
			"paste again to expand\n" + cursorAt + "$\u00a0 [Pasted text #2 +3 lines]\n", "not drawn"},
		{"a paste interrupted before any reply, settled, 2.1.308", "claude: ack 2: hello first\n" +
			"Cogitated for 0s · done 3:41 AM\naccept edits on (shift+tab to cycle)\n" +
			cursorAt + "$\u00a0 [Pasted text #4 +3 lines]\n", "drawn"},
		{"a message of several lines put back in the input, 2.1.308", "claude: ack 5:\nCooked for 0s · done 3:55 AM\n" +
			"accept edits on (shift+tab to cycle)\n$\u00a0 ### Message 3\n\nSLOWSTART: think for a long time before answering\n\n" +
			cursorAt + "\n", "drawn"},
		{"its first line left after one clear, 2.1.308", "claude: ack 5:\nCooked for 0s · done 3:55 AM\n" +
			"accept edits on (shift+tab to cycle)\n$\u00a0 ### Message 3\n\n\n" + cursorAt + "\n", "drawn"},
		{"a message put back in the input that quotes the footer", "claude: the reply\n" +
			"accept edits on (shift+tab to cycle)\n$\u00a0 LONG explain\n" + cursorAt + "(shift+tab to cycle)\n", "drawn"},
		{"text in the input that quotes the footer and then the prompt", "claude: the reply\n" +
			"accept edits on (shift+tab to cycle)\n$\u00a0 LONG explain\n(shift+tab to cycle)\n" + cursorAt + "$\n", "drawn"},
		{"running before any of the reply is drawn, 2.1.308", "you: SLOWSTART nothing streams yet\nSimmering…\n" +
			"Tip: Hit Enter to queue up additional messages while Claude is working.\n" +
			"accept edits on (shift+tab to cycle)  ·  esc to interrupt\neffort: medium · /effort\n" + cursorAt + "$\n", "drawn"},
		{"a notice below the footer", "claude: the reply\nWorked for 0s · done 10:53 PM\n" +
			"accept edits on (shift+tab to cycle)\n" +
			"tmux focus-events off · add 'set -g focus-events on' to ~/.tmux.conf and reattach for focus tracking\n" +
			cursorAt + "$\n\n\n", "ready"},
		// Captured from 2.1.308 with the cursor the server reported, a path
		// and a name replaced. The reply quoting the interface is what a
		// reading that took the first footer and prompt on the screen for
		// the interface read as an input holding text, so every later send
		// was refused while it was drawn.
		{"a reply quoting the footer and the prompt, 2.1.308",
			"you: QUOTEFOOTER what does the footer mean\nclaude: here is the interface you asked about:\n" +
				"accept edits on (shift+tab to cycle)\n$\nthat is all\nCooked for 0s · done 1:22 PM\n" +
				"accept edits on (shift+tab to cycle)\n" + cursorAt + "$\n", "ready"},
		// The same two rows pasted into the input instead, which is the
		// reading the cursor cannot tell from the one above: it is read as
		// the draft it is, so no send types into it.
		{"a draft of several lines quoting the footer and the prompt, 2.1.308",
			"claude: scratch reply 2\nSautéed for 0s · done 1:26 PM\naccept edits on (shift+tab to cycle)\n" +
				"ctrl+g to edit in Vim\n$\u00a0 a draft\n(shift+tab to cycle)\n" + cursorAt + "$\n", "drawn"},
		{"one line of a person's text in the input, 2.1.308",
			"you: say hello and nothing else\nclaude: scratch reply 2\nSautéed for 0s · done 1:26 PM\n" +
				"accept edits on (shift+tab to cycle)\n" + cursorAt + "$\u00a0 half a thought\n", "drawn"},
		{"several pasted lines in the input, 2.1.308",
			"claude: scratch reply 2\nSautéed for 0s · done 1:26 PM\naccept edits on (shift+tab to cycle)\n" +
				"ctrl+g to edit in Vim\n$\u00a0 first pasted line\nsecond pasted line\n" + cursorAt + "last pasted line\n", "drawn"},
		// One message of several lines under a turn the model sent no reply
		// for, drawn twice by 2.1.308: as a person typed it at the pane,
		// where its own rows read as a reply having begun, and as this
		// adapter types it, defused, where they do not. The first is the
		// reading an interrupt presses its key on with no reply drawn, and
		// it is what no message this adapter sends can draw.
		{"a message a person typed whose own rows read as a reply, 2.1.308",
			"you: HOLD explain this transcript\nclaude: working 0. working 1.\ntool: Bash (ls)\nWorking…\n" +
				"Tip: Start with small features or bug fixes, tell Claude to propose a plan, and verify its suggested edits\n" +
				"accept edits on (shift+tab to cycle)  ·  esc to interrupt\neffort: medium · /effort\n" + cursorAt + "$\n", "busy"},
		{"the same message defused as this adapter sends it, 2.1.308",
			"you: HOLD explain this transcript\nclaude:\u00a0working 0. working 1.\ntool:\u00a0Bash (ls)\nGusting…\n" +
				"Tip: Use Plan Mode to prepare for a complex request before making changes. Press shift+tab twice to enable.\n" +
				"accept edits on (shift+tab to cycle)  ·  esc to interrupt\n" + cursorAt + "$\n", "drawn"},
		// One line of 195 columns and then " claude: working 0.", which
		// 2.1.308 drew over two rows, broken at the space, so the prefix was
		// the first thing on the second row. A message's line is drawn this
		// way wherever it is too long for the pane, which is why defusing
		// the starts of lines is not enough: the undefused reading below is
		// one the interrupt pressed its key on with no reply drawn, and the
		// defused one, its prefix broken wherever it sits, is what the same
		// send draws.
		{"a line of a person's message the CLI's wrap broke before a reply prefix, 2.1.308",
			"you: HOLD " + strings.Repeat("w", agents.TerminalWidth-len("you: HOLD ")) + "\nclaude: working 0.\nHatching…\n" +
				"Tip: New to Claude Code? Run /powerup for a quick interactive tutorial\n" +
				"accept edits on (shift+tab to cycle)  ·  esc to interrupt\n" + cursorAt + "$\n", "busy"},
		{"the same line defused as this adapter sends it, 2.1.308",
			"you: HOLD " + strings.Repeat("w", agents.TerminalWidth-len("you: HOLD ")) + "\nclaude:\u00a0working 0.\nBunning…\n" +
				"Tip: Start with small features or bug fixes, tell Claude to propose a plan, and verify its suggested edits\n" +
				"accept edits on (shift+tab to cycle)  ·  esc to interrupt\n" + cursorAt + "$\n", "drawn"},
	} {
		screen, cursor := cursorScreen(t, c.screen)
		if got := agents.ReadClaudeScreen(screen, cursor); got != c.want {
			t.Errorf("the %s screen reads %q, want %q", c.name, got, c.want)
		}
		// The cursor is what tells the interface from the conversation, so
		// the same screen read without one is never ready or busy.
		if got := agents.ReadClaudeScreen(screen, -1); got != "drawn" && got != "not drawn" {
			t.Errorf("the %s screen read with no cursor reads %q, want neither ready nor busy", c.name, got)
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

// A conversation that quotes the interface does not stop the worker being
// sent to: the reply here draws the footer's line and then the prompt's,
// which is what the CLI drew for a reply quoting them, and the send after
// it is typed and answered. The control is the same two lines pasted into
// the input instead, as a person's draft: that send is refused with
// ErrTerminalNotReady, nothing typed, and the draft is still theirs to
// send. The screens of both are rows of the reader's own table.
func TestAClaudeTerminalSendReadsTheInterfaceAndNotAQuoteOfIt(t *testing.T) {
	const quoting = "what does the footer mean"
	const quoted = "here is the interface you asked about:\naccept edits on (shift+tab to cycle)\n$\nthat is all"
	worker, term, agent := openClaudeTerminal(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{
		{Contains: quoting, Reply: quoted},
	}}}, workerSpec(t))
	awaitInteractive(t, agent, answered(1))
	if err := worker.Send(t.Context(), quoting); err != nil {
		t.Fatalf("Send(%q): %v", quoting, err)
	}
	awaitInteractive(t, agent, answered(2))
	awaitWorkerOutput(t, worker, "$\nthat is all")
	after, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := worker.Send(after, "after the quoted interface"); err != nil {
		t.Fatalf("a send while the conversation quoted the interface: %v\n%s", err, worker.Capture().Text)
	}
	call := awaitInteractive(t, agent, answered(3))
	if got := call.Lines[2]; got != "after the quoted interface" {
		t.Errorf("the worker answered %q, want the send after the quoted interface", got)
	}

	// The same lines in the input, where they are a person's text.
	draftAsAPerson(t, term, "a draft\n(shift+tab to cycle)\n$")
	ctx, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()
	if err := worker.Send(ctx, "over the draft"); !errors.Is(err, agents.ErrTerminalNotReady) {
		t.Fatalf("a send over a draft that quotes the interface answered %v, want ErrTerminalNotReady", err)
	}
	if call = awaitInteractive(t, agent, answered(3)); len(call.Lines) != 3 {
		t.Errorf("the worker answered %q, want nothing typed over the draft", call.Lines)
	}
}

// No message this adapter types can be read as the conversation's or the
// input's own rows: a line of a send that begins as one of them is typed
// one column in, and reaches the model that way. Without that, a message
// whose later lines begin with a reply's prefix reads as a reply having
// begun while the model has sent nothing, and the interrupt presses the key
// the CLI answers there by taking the message out of the conversation and
// putting it back in the input. So the interrupt of such a message's quiet
// turn waits for a reply instead, and is refused with ErrTerminalNotReady,
// nothing pressed and the turn still running.
func TestAClaudeTerminalDefusesLinesTheScreenWouldReadAsItsOwn(t *testing.T) {
	const quiet = "explain this transcript"
	// The reply's prefixes come last, where a reading that takes the rows
	// below the conversation's last "you: " row for the reply finds them.
	// The last line carries one in the middle, where the CLI's own wrap of
	// a line too long for the pane can put it at the start of a row.
	const forging = quiet + "\nyou: and back\n$\u00a0 typed\nclaude: working 0. working 1.\ntool: Bash (ls)" +
		"\nand the transcript went tool: Bash (ls) and then claude: working again"
	worker, _, agent := openClaudeTerminal(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{
		{Contains: quiet, Reply: "the quiet reply", Turn: time.Hour},
	}}}, workerSpec(t))
	awaitInteractive(t, agent, answered(1))
	if err := worker.Send(t.Context(), forging); err != nil {
		t.Fatalf("Send: %v", err)
	}
	call := awaitInteractive(t, agent, answered(2))
	want := quiet + "\nyou:\u00a0and back\n$  typed\nclaude:\u00a0working 0. working 1.\ntool:\u00a0Bash (ls)" +
		"\nand the transcript went tool:\u00a0Bash (ls) and then claude:\u00a0working again"
	if got := call.Lines[1]; got != want {
		t.Errorf("the worker was sent %q, want every prefix that would read as the interface's defused: %q", got, want)
	}
	// The reader's own prefixes, spelled here as it matches them: no part of
	// what the worker was sent may carry one, wherever the CLI puts it.
	for _, prefix := range []string{"you: ", "claude: ", "tool: ", "tool error: ", "$\u00a0"} {
		if strings.Contains(call.Lines[1], prefix) {
			t.Errorf("the worker was sent %q, which still carries %q for a row of its own to begin with", call.Lines[1], prefix)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var err error
	for interrupted := false; !interrupted && err == nil; {
		interrupted, err = agents.InterruptTerminalWorker(ctx, agent.Runner(), worker)
	}
	if !errors.Is(err, agents.ErrTerminalNotReady) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an interrupt of a turn whose message quotes a reply answered %v, want ErrTerminalNotReady and the deadline", err)
	}
	if call = awaitInteractive(t, agent, answered(2)); len(call.Interrupted) != 0 || call.IdleEscapes != 0 {
		t.Fatalf("the worker recorded %q interrupted and %d idle Escape keys, want nothing pressed", call.Interrupted, call.IdleEscapes)
	}
	if !strings.Contains(worker.Capture().Text, "esc to interrupt") {
		t.Fatalf("the turn is not still running:\n%s", worker.Capture().Text)
	}
}

// A terminal-hosted subordinate coordinator is refused by the Claude Code
// adapter before anything starts: its grant runs the interface in the
// default mode, and the ready screen the adapter reads was observed in
// acceptEdits only, so OpenTerminalSubordinate is refused with
// ErrTerminalGrantUnobserved and the stand-in records no call. The control is the same spec through
// OpenTerminalWorker, which starts under the worker grant.
func TestAClaudeTerminalSubordinateIsRefusedBeforeItStarts(t *testing.T) {
	principles.Cite(t, principles.P4)
	const route = "/opt/assistant/bin/assistant --home /var/lib/assistant coordinator"
	spec := workerSpec(t)
	spec.Route = route
	term := terminalFor(t)
	agent := standin.New(t, standin.Script{},
		agents.WithTerminationGrace(300*time.Millisecond), agents.WithBaseEnvironment(claudeTerminalEnv(t)))
	if _, err := agents.OpenTerminalSubordinate(t.Context(), agent.Runner(), term, spec); !errors.Is(err, agents.ErrTerminalGrantUnobserved) {
		t.Fatalf("OpenTerminalSubordinate = %v, want ErrTerminalGrantUnobserved", err)
	}
	if calls := agent.Calls(); len(calls) != 0 {
		t.Fatalf("the refused subordinate started %d agent processes, want none", len(calls))
	}
	worker, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, spec)
	if err != nil {
		t.Fatalf("the control worker: %v", err)
	}
	call := awaitInteractive(t, agent, answered(1))
	worker.Close()
	want := workerGrant
	want.allowed += ",Bash(" + route + ":*)"
	if got := grantOf(call.Args); got != want {
		t.Errorf("the terminal worker ran under %+v, want %+v", got, want)
	}
}

// An interrupt presses Escape once, only on a screen that reads a turn
// running that has drawn part of its reply with an empty input, and answers
// once the screen reads ready with the worker still running; the turn is
// ended unanswered and the next send is answered.
// With no turn running it answers so and presses nothing, and over a
// person's unsent text it presses nothing and is refused with
// ErrTerminalNotReady once its context ends, leaving the text theirs:
// Claude Code 2.1.308 was observed to open a menu on a second Escape at an
// idle screen and to offer to clear typed text on one.
func TestAClaudeTerminalInterruptPressesOneKeyWhileATurnRuns(t *testing.T) {
	interruptOneTurn(t, "the reply begins")
}

// interruptOneTurn is one run of
// TestAClaudeTerminalInterruptPressesOneKeyWhileATurnRuns, with progress as
// what the long turn draws of its reply.
func interruptOneTurn(t *testing.T, progress string) {
	// Several lines, as every message the service sends is, so a message
	// put back in the input takes a clear per line.
	const long = "a turn to interrupt\n\nwith a second paragraph"
	worker, term, agent := openClaudeTerminal(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{
		{Contains: "a turn to interrupt", Reply: "the long turn's reply", Turn: time.Hour, Progress: progress},
	}}}, workerSpec(t))
	runner := agent.Runner()
	awaitInteractive(t, agent, answered(1))
	if err := worker.Send(t.Context(), long); err != nil {
		t.Fatalf("Send: %v", err)
	}
	awaitInteractive(t, agent, answered(2))
	// One asked before the turn is drawn finds none and presses nothing.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		interrupted, err := agents.InterruptTerminalWorker(t.Context(), runner, worker)
		if err != nil {
			t.Fatalf("interrupting the long turn: %v\n%s", err, worker.Capture().Text)
		}
		if interrupted {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the interrupt never found the long turn running:\n%s", worker.Capture().Text)
		}
	}
	call := awaitInteractive(t, agent, func(c standin.Call) bool { return len(c.Interrupted) > 0 })
	if !slices.Equal(call.Interrupted, []string{long}) || call.IdleEscapes != 0 {
		t.Fatalf("the worker recorded %q interrupted and %d idle Escape keys, want the long turn and none", call.Interrupted, call.IdleEscapes)
	}
	select {
	case <-worker.Done():
		t.Fatal("the worker ended on the interrupt")
	default:
	}
	if interrupted, err := agents.InterruptTerminalWorker(t.Context(), runner, worker); err != nil || interrupted {
		t.Fatalf("an interrupt with no turn running answered %t, %v, want false", interrupted, err)
	}
	if err := worker.Send(t.Context(), "and then"); err != nil {
		t.Fatalf("Send after the interrupt: %v", err)
	}
	awaitInteractive(t, agent, answered(3))

	half := exec.Command(term.Tmux, "-S", term.Socket, "send-keys", "-t", "worker", "-l", "half a thought")
	if out, err := half.CombinedOutput(); err != nil {
		t.Fatalf("typing as a person: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := agents.InterruptTerminalWorker(ctx, runner, worker); !errors.Is(err, agents.ErrTerminalNotReady) {
		t.Fatalf("an interrupt over a person's unsent text answered %v, want ErrTerminalNotReady", err)
	}
	enter := exec.Command(term.Tmux, "-S", term.Socket, "send-keys", "-t", "worker", "Enter")
	if out, err := enter.CombinedOutput(); err != nil {
		t.Fatalf("pressing Enter as a person: %v: %s", err, out)
	}
	call = awaitInteractive(t, agent, answered(4))
	if call.Lines[3] != "half a thought" || call.IdleEscapes != 0 || strings.Contains(worker.Capture().Text, "the long turn's reply") {
		t.Fatalf("the worker answered %q with %d idle Escape keys, want the person's text as typed and none:\n%s",
			call.Lines, call.IdleEscapes, worker.Capture().Text)
	}
}

// An interrupt presses nothing while the running turn has drawn none of its
// reply, because Claude Code 2.1.308 answered Escape (and Control-C) there by
// putting the message back in the input, which nothing can then take out
// without risking a person's text: it waits, and is refused with
// ErrTerminalNotReady once its context ends, saying that nothing was
// pressed, the turn still running. Once the reply has begun, the same turn
// is interrupted.
func TestAClaudeTerminalInterruptWaitsForTheReplyToBegin(t *testing.T) {
	const quiet = "a quiet long turn"
	worker, _, agent := openClaudeTerminal(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{
		{Contains: quiet, Reply: "the quiet reply", Turn: time.Hour},
	}}}, workerSpec(t))
	awaitInteractive(t, agent, answered(1))
	if err := worker.Send(t.Context(), quiet); err != nil {
		t.Fatalf("Send: %v", err)
	}
	awaitInteractive(t, agent, answered(2))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var err error
	for interrupted := false; !interrupted && err == nil; {
		interrupted, err = agents.InterruptTerminalWorker(ctx, agent.Runner(), worker)
	}
	if !errors.Is(err, agents.ErrTerminalNotReady) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("an interrupt of a turn that drew no reply answered %v, want ErrTerminalNotReady and the deadline", err)
	}
	// The contract is that a refusal carries what the key did, and this is
	// the refusal that comes before any press.
	var refused *agents.InterruptRefused
	if !errors.As(err, &refused) || refused.Key != agents.InterruptKeyUnpressed {
		t.Errorf("the refusal %v carries %v, want an *InterruptRefused of %v", err, refused, agents.InterruptKeyUnpressed)
	}
	call := awaitInteractive(t, agent, answered(2))
	if len(call.Interrupted) != 0 || call.IdleEscapes != 0 {
		t.Fatalf("the worker recorded %q interrupted and %d idle Escape keys, want nothing pressed", call.Interrupted, call.IdleEscapes)
	}
	if !strings.Contains(worker.Capture().Text, "esc to interrupt") {
		t.Fatalf("the turn is not still running:\n%s", worker.Capture().Text)
	}
}

// An interrupt waits its turn behind a send that waits for the running turn
// to end, and only for as long as its own context allows: it is refused
// with ErrTerminalNotReady and the context's error when that ends first. The
// send is given a moment to take its turn first; an interrupt that takes it
// first anyway ends the turn, which is also an answer within its deadline.
func TestAClaudeTerminalInterruptBehindAWaitingSendKeepsItsDeadline(t *testing.T) {
	const long = "a turn to wait behind"
	worker, _, agent := openClaudeTerminal(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{
		{Contains: long, Reply: "the long reply", Turn: time.Hour, Progress: "the long reply begins"},
	}}}, workerSpec(t))
	awaitInteractive(t, agent, answered(1))
	if err := worker.Send(t.Context(), long); err != nil {
		t.Fatalf("Send: %v", err)
	}
	awaitInteractive(t, agent, answered(2))
	pending, stop := context.WithCancel(t.Context())
	defer stop()
	go func() { _ = worker.Send(pending, "queued behind the turn") }()
	time.Sleep(500 * time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := agents.InterruptTerminalWorker(ctx, agent.Runner(), worker); done <- err }()
	select {
	case err := <-done:
		if err != nil && (!errors.Is(err, agents.ErrTerminalNotReady) || !errors.Is(err, context.DeadlineExceeded)) {
			t.Fatalf("an interrupt behind a waiting send answered %v, want ErrTerminalNotReady and the deadline", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("an interrupt behind a waiting send was still waiting long after its deadline")
	}
}
