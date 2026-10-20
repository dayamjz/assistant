package standin

import (
	"os"
	"os/exec"
	"path/filepath"
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
	tmux := Tmux(t)
	agent := New(t, Script{})
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
	return socket
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
