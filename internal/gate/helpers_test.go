package gate_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// gitEnvironment points git at a configuration file this test owns, so that a
// developer's own git configuration cannot change what a test proves. It
// returns the path of that file so a test can append settings to it.
//
// It deliberately does not go through the package under test: a fixture built
// with the code being tested cannot show that code wrong.
func gitEnvironment(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := filepath.Join(home, "gitconfig")
	writeFile(t, cfg, "[user]\n\tname = Test\n\temail = test@example.invalid\n[init]\n\tdefaultBranch = main\n[receive]\n\tdenyCurrentBranch = ignore\n")
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return cfg
}

// appendConfig adds lines to the configuration file gitEnvironment created.
func appendConfig(t *testing.T, cfg, text string) {
	t.Helper()
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", cfg, err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("append to %s: %v", cfg, err)
	}
}

// rawGit runs git directly, without the package under test, and fails the test
// if it does not succeed.
func rawGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := tryRawGit(dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

// tryRawGit runs git directly and returns its combined output and error, for
// the cases where the failure is the point.
func tryRawGit(dir string, args ...string) (string, error) {
	return tryRawGitWithin(context.Background(), dir, args...)
}

// tryRawGitWithin is tryRawGit under a deadline, for a test whose regression
// would be a git invocation that never returns rather than one that fails.
func tryRawGitWithin(c context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(c, "git", args...)
	cmd.Dir = dir
	// Without a wait delay, killing git on the deadline still leaves this
	// call blocked on output pipes a surviving hook process holds open, which
	// turns the failure this bound exists to catch into a hung test run.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if c.Err() != nil {
		return string(out), c.Err()
	}
	return string(out), err
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeScript(t *testing.T, path, content string) {
	t.Helper()
	writeFile(t, path, content)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return c
}

// resolved is the form of a path the package under test compares against. The
// temporary directories these tests run in sit under a symbolic link on some
// hosts, so a test that compared a reported path against the one it passed in
// would fail there for a reason that has nothing to do with gates.
func resolved(t *testing.T, path string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return out
}

// workingCopy is a working copy with one commit on main and an origin remote
// pointing at a bare repository of its own.
type workingCopy struct {
	path   string
	origin string
	commit string
}

func newWorkingCopy(t *testing.T) workingCopy {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	rawGit(t, root, "init", "--quiet", "--bare", origin)

	path := filepath.Join(root, "work")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	rawGit(t, path, "init", "--quiet", ".")
	writeFile(t, filepath.Join(path, "file.txt"), "first\n")
	rawGit(t, path, "add", "-A")
	rawGit(t, path, "commit", "--quiet", "-m", "first")
	rawGit(t, path, "remote", "add", "origin", origin)
	commit := strings.TrimSpace(rawGit(t, path, "rev-parse", "HEAD"))
	return workingCopy{path: path, origin: origin, commit: commit}
}

// commitMore adds another commit and returns its identifier.
func commitMore(t *testing.T, wc workingCopy, text string) string {
	t.Helper()
	writeFile(t, filepath.Join(wc.path, "file.txt"), text)
	rawGit(t, wc.path, "add", "-A")
	rawGit(t, wc.path, "commit", "--quiet", "-m", text)
	return strings.TrimSpace(rawGit(t, wc.path, "rev-parse", "HEAD"))
}

// remoteURL reads a remote's URL with git rather than with the package under
// test, so that what a test checks about the working copy's configuration does
// not depend on the code that wrote it.
func remoteURL(t *testing.T, dir, name string) (string, bool) {
	t.Helper()
	out, err := tryRawGit(dir, "remote", "get-url", name)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// refs lists a repository's branches as "name commit" lines, sorted by git.
func refs(t *testing.T, dir string) []string {
	t.Helper()
	out := strings.TrimSpace(rawGit(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/"))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// recorderCommand writes an executable that appends one line per invocation to
// a log, together with the standard input git handed the hook, and exits with
// status. It stands in for the agent-facing command surface the hooks invoke.
//
// It is a real program on disk rather than a fake object, because what these
// tests need to establish is that git runs it at all, which no in-process
// substitute can show.
func recorderCommand(t *testing.T, status int) (command, log string) {
	t.Helper()
	dir := t.TempDir()
	command = filepath.Join(dir, "assistant-stub")
	log = filepath.Join(dir, "invocations.log")
	writeScript(t, command, "#!/bin/sh\n"+
		"{\n"+
		"  printf 'command %s\\n' \"$*\"\n"+
		"  printf 'input %s\\n' \"$(cat | tr '\\n' ';')\"\n"+
		"} >>"+shellQuoteForTest(log)+"\n"+
		"exit "+strconv.Itoa(status)+"\n")
	return command, log
}

func shellQuoteForTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// invocations returns the lines the recorder wrote, or nothing when it was
// never invoked.
func invocations(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", log, err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// copyTree copies a directory the way a person copying a project would.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	if err := os.CopyFS(to, os.DirFS(from)); err != nil {
		t.Fatalf("copy %s to %s: %v", from, to, err)
	}
}
