package agents_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// A Claude Code terminal worker runs under a HOME of its own, beside the
// configuration and cache directories the adapter already makes from the
// terminal, owner-only and gone once the worker is. The environment is read
// from a command run in the pane, and the directory is the one the adapter
// derives from the terminal. The control is CLAUDE_CONFIG_DIR beside it.
func TestAClaudeTerminalWorkerRunsUnderAHomeOfItsOwn(t *testing.T) {
	spec := workerSpec(t)
	script := standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "run:", Run: true}},
	}}
	operatorHome := t.TempDir()
	worker, term, _ := openClaudeTerminal(t, script, spec, agents.WithBaseEnvironment(
		withStandinKey(withOperatorGitIdentity([]string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + operatorHome,
		}))))
	if err := worker.Send(t.Context(), "run: env > env.txt"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	env := awaitWorkerEnvironment(t, filepath.Join(spec.Dir, "env.txt"))

	home := agents.ClaudeHomeFor(term)
	if got := env["HOME"]; got != home {
		t.Errorf("the worker's HOME is %q, want its own %q", got, home)
	}
	if got := env["HOME"]; got == operatorHome {
		t.Errorf("the worker's HOME is still the operator's %q", got)
	}
	if got := env["CLAUDE_CONFIG_DIR"]; got != agents.ClaudeConfigFor(term) {
		t.Errorf("the worker's configuration directory is %q, want %q", got, agents.ClaudeConfigFor(term))
	}
	if got := env["XDG_CACHE_HOME"]; got != agents.ClaudeCacheFor(term) {
		t.Errorf("the worker's cache directory is %q, want %q", got, agents.ClaudeCacheFor(term))
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatalf("the worker's HOME directory: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("the worker's HOME directory is %v, want owner-only", perm)
	}

	worker.Close()
	for _, dir := range []string{home, agents.ClaudeCacheFor(term), agents.ClaudeConfigFor(term)} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s outlived the worker: %v", dir, err)
		}
	}
}

// A commit a Claude Code terminal worker makes carries the operator's git
// name and email, set on the worker explicitly, even though its HOME is a
// fresh directory that holds none of the operator's git configuration. The
// operator identity is given here through the environment git reads, which
// is how a start that found it in the operator's configuration carries it;
// the HOME the runner was given is empty of a gitconfig so a silent
// host-derived identity would be the only other answer.
func TestAClaudeTerminalWorkerCommitsAsTheOperator(t *testing.T) {
	const (
		operatorName  = "operator-under-test"
		operatorEmail = "operator-under-test@example.invalid"
	)
	spec := workerSpec(t)
	script := standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "run:", Run: true}},
	}}
	// An empty gitconfig under a HOME the process itself uses would still
	// let git derive a host identity on this machine; the operator identity
	// reaches the worker only through the variables the adapter must set.
	worker, term, _ := openClaudeTerminal(t, script, spec, agents.WithBaseEnvironment(
		withStandinKey([]string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + t.TempDir(),
			"GIT_CONFIG_GLOBAL=" + filepath.Join(t.TempDir(), "no-such-gitconfig"),
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=" + operatorName,
			"GIT_AUTHOR_EMAIL=" + operatorEmail,
			"GIT_COMMITTER_NAME=" + operatorName,
			"GIT_COMMITTER_EMAIL=" + operatorEmail,
		})))
	cmd := "git init -q repo && cd repo && " +
		"echo content > file.txt && git add file.txt && " +
		"git -c commit.gpgsign=false commit -q -m measure && " +
		"git log -1 --format='%an <%ae>%n%cn <%ce>' > ../ident.txt && " +
		"printf 'HOME=%s\\n' \"$HOME\" > ../home.txt && " +
		"env | sort > ../env.txt"
	if err := worker.Send(t.Context(), "run: "+cmd); err != nil {
		t.Fatalf("Send: %v", err)
	}
	ident := awaitMeasureFile(t, filepath.Join(spec.Dir, "ident.txt"))
	homeLine := awaitMeasureFile(t, filepath.Join(spec.Dir, "home.txt"))
	envText := awaitMeasureFile(t, filepath.Join(spec.Dir, "env.txt"))

	want := operatorName + " <" + operatorEmail + ">\n" + operatorName + " <" + operatorEmail + ">\n"
	if ident != want {
		t.Errorf("the worker committed as %q, want the operator %q", ident, want)
	}
	if got := strings.TrimSpace(homeLine); got != "HOME="+agents.ClaudeHomeFor(term) {
		t.Errorf("the commit was made under %s, want HOME=%s", got, agents.ClaudeHomeFor(term))
	}
	for _, name := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		found := false
		for _, line := range strings.Split(envText, "\n") {
			if strings.HasPrefix(line, name+"=") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the worker's environment carries no %s", name)
		}
	}
}

// A start whose environment and the process's git configuration name no
// operator identity is refused before any directory is made, with an error
// wrapping ErrNoGitIdentity, so a host-derived identity never reaches a
// commit and a commit never fails later for want of one.
func TestAClaudeTerminalStartRefusedWithoutAGitIdentity(t *testing.T) {
	term := terminalFor(t)
	empty := filepath.Join(t.TempDir(), "empty-gitconfig")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	for _, name := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL",
	} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
	agent := standin.New(t, standin.Script{}, agents.WithTerminationGrace(300*time.Millisecond),
		agents.WithBaseEnvironment(withStandinKey([]string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + t.TempDir(),
			"GIT_CONFIG_GLOBAL=" + empty,
			"GIT_CONFIG_SYSTEM=/dev/null",
		})))

	_, err := agents.OpenTerminalWorker(t.Context(), agent.Runner(), term, workerSpec(t))
	if !errors.Is(err, agents.ErrNoGitIdentity) {
		t.Fatalf("a start without a git identity answered %v, want ErrNoGitIdentity", err)
	}
	for _, dir := range []string{
		agents.ClaudeHomeFor(term), agents.ClaudeCacheFor(term), agents.ClaudeConfigFor(term),
	} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was made for a refused start: %v", dir, err)
		}
	}
	if calls := agent.Calls(); len(calls) != 0 {
		t.Errorf("the refused start ran the CLI %d times: %v", len(calls), calls)
	}
}

// withOperatorGitIdentity is env with a git author and committer the
// adapter can carry into a worker, so a test that does not itself settle
// the process's git configuration still reaches a start.
func withOperatorGitIdentity(env []string) []string {
	return append(append([]string{}, env...),
		"GIT_AUTHOR_NAME=operator-under-test",
		"GIT_AUTHOR_EMAIL=operator-under-test@example.invalid",
		"GIT_COMMITTER_NAME=operator-under-test",
		"GIT_COMMITTER_EMAIL=operator-under-test@example.invalid",
	)
}

func awaitMeasureFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile(path)
		if err == nil && len(raw) > 0 {
			return string(raw)
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("never wrote %s: %v", path, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
