package standin

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// startInteractive starts the stand-in as an interactive worker in a tmux
// pane of a server of its own, with a state file holding state in a
// configuration directory of its own and key as its API key, and returns
// the socket. The command line is the one the Claude Code adapter writes
// for a terminal worker, without the grant's tool list.
func startInteractive(t *testing.T, state, key string) string {
	t.Helper()
	socket, _ := startInteractiveWith(t, Script{}, state, key)
	return socket
}

// startInteractiveWith is startInteractive over script, and also returns
// the stand-in, so a test can read what the worker recorded.
func startInteractiveWith(t *testing.T, script Script, state, key string) (string, *Agent) {
	t.Helper()
	tmux := Tmux(t)
	agent := New(t, script)
	dir := t.TempDir()
	config := t.TempDir()
	if state != "" {
		if err := os.WriteFile(filepath.Join(config, ".claude.json"), []byte(strings.ReplaceAll(state, "DIR", dir)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(SocketDir(t), "i")
	args := append([]string{"-S", socket, "-f", os.DevNull, "new-session", "-d", "-s", "w", "-x", "200", "-y", "50",
		"-c", dir, "--", self}, agent.Arguments()...)
	args = append(args, "--permission-mode", "acceptEdits", screenReaderFlag, "--", "hello there")
	cmd := exec.Command(tmux, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "CLAUDE_CONFIG_DIR=" + config,
		"ANTHROPIC_API_KEY=" + key}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("starting the interactive stand-in: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(tmux, "-S", socket, "kill-server").Run()
		_ = os.Remove(socket)
	})
	return socket, agent
}

// screenOf waits until the pane carries want and returns what it carries,
// or fails with what it carried last.
func screenOf(t *testing.T, socket, want string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, _ := exec.Command(Tmux(t), "-S", socket, "capture-pane", "-p", "-J", "-t", "w").Output()
		if strings.Contains(string(out), want) {
			return string(out)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the pane never carried %q; it carries:\n%s", want, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An interactive worker opens on the first-run screen its state file calls
// for and never draws its interface there, so a typist that has not seeded
// the state is caught; seeded with onboarding, trust for the directory it
// runs in exactly, and the last characters of its key, it draws its
// interface and answers the message after "--". An approval naming the
// whole key is no approval, which is what was observed of the CLI.
func TestAnInteractiveWorkerOpensOnTheScreenItsStateCallsFor(t *testing.T) {
	const key = "standin-placeholder-key-0123456789abcdefghij"
	approved := suffix(key, approvalSuffix)
	trusted := `"projects":{"DIR":{"hasTrustDialogAccepted":true}}`
	for _, c := range []struct {
		name, state, want string
	}{
		{"no state file", "", themeScreen},
		{"not onboarded", `{"hasCompletedOnboarding":false,` + trusted + `}`, themeScreen},
		{"untrusted", `{"hasCompletedOnboarding":true}`, trustChoice},
		{"trusted elsewhere", `{"hasCompletedOnboarding":true,"projects":{"/elsewhere":{"hasTrustDialogAccepted":true}}}`, trustChoice},
		{"key not approved", `{"hasCompletedOnboarding":true,` + trusted + `}`, keyChoice},
		{"whole key approved", `{"hasCompletedOnboarding":true,` + trusted + `,"customApiKeyResponses":{"approved":["` + key + `"]}}`, keyChoice},
		{"seeded", `{"hasCompletedOnboarding":true,` + trusted + `,"customApiKeyResponses":{"approved":["` + approved + `"]}}`, "you: hello there"},
	} {
		t.Run(c.name, func(t *testing.T) {
			socket := startInteractive(t, c.state, key)
			screenOf(t, socket, c.want)
			if c.want != "you: hello there" {
				// Long enough for the interface to have been drawn, had
				// the screen let it.
				time.Sleep(2 * DefaultStartup)
				got := screenOf(t, socket, c.want)
				if strings.Contains(got, "(shift+tab to cycle)") {
					t.Errorf("the worker drew its interface past the %s screen:\n%s", c.name, got)
				}
				return
			}
			screenOf(t, socket, footerIdle+"\n")
		})
	}
}

// The Escape key ends a running turn the way Claude Code 2.1.308 was
// observed to end one: a turn that drew part of its reply gets the
// interrupted line where the rest would have gone and the idle footer; a
// turn that drew none has its message put back in the input, every line of
// it, which Control-U takes out one line at a time. Either is recorded Interrupted and answered with no
// reply, and the worker then answers its next message. An Escape with no
// turn running changes nothing on screen and is counted, so a typist that
// presses one there, which the CLI was seen to take another way, is caught.
func TestAnInteractiveWorkerEndsATurnOnEscape(t *testing.T) {
	const key = "standin-placeholder-key-0123456789abcdefghij"
	state := `{"hasCompletedOnboarding":true,"projects":{"DIR":{"hasTrustDialogAccepted":true}},` +
		`"customApiKeyResponses":{"approved":["` + suffix(key, approvalSuffix) + `"]}}`
	socket, agent := startInteractiveWith(t, Script{Worker: &WorkerScript{Exchanges: []Exchange{
		{Contains: "hello there", Reply: "the long turn's reply", Turn: time.Hour, Progress: "the reply begins"},
		{Contains: "quiet", Reply: "the quiet turn's reply", Turn: time.Hour},
		{Contains: "next", Reply: "the next reply"},
	}}}, state, key)
	keys := func(args ...string) {
		t.Helper()
		cmd := exec.Command(Tmux(t), append([]string{"-S", socket, "send-keys", "-t", "w"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("typing %q: %v: %s", args, err, out)
		}
	}
	interactive := func() Call {
		var call Call
		for _, c := range agent.Calls() {
			if c.Interactive {
				call = c
			}
		}
		return call
	}
	// Each key is waited on before the next is typed, because a key right
	// behind an ESC is read with it as one sequence, as a terminal reads it.
	recorded := func(what string, ok func(Call) bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !ok(interactive()); time.Sleep(20 * time.Millisecond) {
			if !time.Now().Before(deadline) {
				t.Fatalf("%s was never recorded: %+v", what, interactive())
			}
		}
	}
	screenOf(t, socket, footerBusy)
	keys("Escape")
	got := screenOf(t, socket, interruptedLine)
	if strings.Contains(got, "the long turn's reply") || !strings.Contains(got, footerIdle+"\n") {
		t.Fatalf("after Escape the pane carries:\n%s\nwant the interrupted line, the idle footer and no reply", got)
	}
	keys("Escape")
	recorded("the idle Escape", func(c Call) bool { return c.IdleEscapes == 1 })
	const quiet = "quiet\n\na second paragraph"
	paste := exec.Command(Tmux(t), "-S", socket, "load-buffer", "-b", "p", "-", ";", "paste-buffer", "-p", "-d", "-b", "p", "-t", "w")
	paste.Stdin = strings.NewReader(quiet)
	if out, err := paste.CombinedOutput(); err != nil {
		t.Fatalf("pasting: %v: %s", err, out)
	}
	keys("Enter")
	screenOf(t, socket, footerBusy)
	keys("Escape")
	screenOf(t, socket, footerIdle+"\n"+inputLine+"\u00a0quiet\n\na second paragraph")
	recorded("the second interrupt", func(c Call) bool { return len(c.Interrupted) == 2 })
	// A line's text, then each line break, one press each.
	keys("C-u")
	screenOf(t, socket, footerIdle+"\n"+inputLine+"\u00a0quiet\n\n")
	keys("C-u", "C-u", "C-u")
	screenOf(t, socket, footerIdle+"\n"+inputLine+"\n")
	keys("-l", "next")
	keys("Enter")
	got = screenOf(t, socket, "claude: the next reply")
	if strings.Contains(got, "the quiet turn's reply") {
		t.Fatalf("the interrupted quiet turn was answered:\n%s", got)
	}
	call := interactive()
	if !slices.Equal(call.Interrupted, []string{"hello there", quiet}) || call.IdleEscapes != 1 ||
		!slices.Equal(call.Lines, []string{"hello there", quiet, "next"}) {
		t.Fatalf("the worker recorded %q interrupted, %d idle Escape keys and answered %q, "+
			"want both long turns, one, and the three messages", call.Interrupted, call.IdleEscapes, call.Lines)
	}
}

// Two Escape keys that reach the worker in one write are two keys, as
// Claude Code 2.1.308 took them: the first ends the quiet turn running and
// puts its message back in the input, and the second, with no turn left
// running, is an idle one. An ESC right behind an ESC begins a sequence of
// its own rather than ending the first one's.
func TestAnInteractiveWorkerTakesTwoEscapesInOneWriteAsTwoKeys(t *testing.T) {
	const key = "standin-placeholder-key-0123456789abcdefghij"
	state := `{"hasCompletedOnboarding":true,"projects":{"DIR":{"hasTrustDialogAccepted":true}},` +
		`"customApiKeyResponses":{"approved":["` + suffix(key, approvalSuffix) + `"]}}`
	socket, agent := startInteractiveWith(t, Script{Worker: &WorkerScript{Exchanges: []Exchange{
		{Contains: "hello there", Reply: "the quiet turn's reply", Turn: time.Hour},
	}}}, state, key)
	screenOf(t, socket, footerBusy)
	// Both bytes in one tmux command, which queues them to the pane
	// together, and which every tmux CI runs has (paste-buffer's -S, the
	// other way to keep tmux from escaping them, is newer than some).
	both := exec.Command(Tmux(t), "-S", socket, "send-keys", "-t", "w", "-H", "1b", "1b")
	if out, err := both.CombinedOutput(); err != nil {
		t.Fatalf("writing two Escape keys: %v: %s", err, out)
	}
	screenOf(t, socket, footerIdle+"\n"+inputLine+" hello there")
	deadline := time.Now().Add(30 * time.Second)
	for {
		var call Call
		for _, c := range agent.Calls() {
			if c.Interactive {
				call = c
			}
		}
		if slices.Equal(call.Interrupted, []string{"hello there"}) && call.IdleEscapes == 1 {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the worker recorded %q interrupted and %d idle Escape keys, want the turn and one", call.Interrupted, call.IdleEscapes)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
