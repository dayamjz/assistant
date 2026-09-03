package gate_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// newHome returns an assistant home and a builder for the options every
// operation in that home takes.
//
// The ownership index is a real internal/store rather than a stand-in. What the
// index answers is what decides whether a gate is adopted or deleted, and a
// stand-in that could answer in a shape the real one cannot produce would make
// every test using it unreliable in exactly the direction that matters here.
func newHome(t *testing.T) (home string, opts func(...gate.Option) []gate.Option) {
	t.Helper()
	home, _, opts = homeWithIndex(t)
	return home, opts
}

// homeWithIndex is newHome for a test that reads the index back as well as
// passing it in.
func homeWithIndex(t *testing.T) (home string, index *store.Store, opts func(...gate.Option) []gate.Option) {
	t.Helper()
	index = openIndex(t)
	return t.TempDir(), index, func(extra ...gate.Option) []gate.Option {
		return append([]gate.Option{gate.WithIndex(index)}, extra...)
	}
}

// boundWorkingPaths is the working copies the index records as bound to a gate,
// read back through internal/store's own accessor.
func boundWorkingPaths(t *testing.T, index *store.Store, gateID string) []string {
	t.Helper()
	bindings, err := index.GateBindings(context.Background(), gateID)
	if err != nil {
		t.Fatalf("read the bindings of gate %s: %v", gateID, err)
	}
	paths := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		paths = append(paths, binding.WorkingPath)
	}
	return paths
}

// indexOptions is newHome for a test that builds its own home path, such as one
// checking a home that does not exist yet.
func indexOptions(t *testing.T) func(...gate.Option) []gate.Option {
	t.Helper()
	_, _, opts := homeWithIndex(t)
	return opts
}

// openIndex opens the ownership index on a database of its own. It is not under
// the home under test, because a home is allowed not to exist yet and opening a
// database does not create the directory it sits in.
func openIndex(t *testing.T) *store.Store {
	t.Helper()
	index, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"),
		store.WithRedactor(testRedactor()))
	if err != nil {
		t.Fatalf("open the ownership index: %v", err)
	}
	t.Cleanup(func() { _ = index.Close() })
	return index
}

// seamUserinfo is the shape the redaction seam in internal/vcs covers: the
// userinfo of a URL that carries a scheme.
var seamUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/@\s]+)@`)

// testRedactor is the credential remover internal/store requires of whoever
// opens it. Nothing in this package stores a URL through it; it is here because
// the store refuses to open without one that works.
func testRedactor() vcs.Redactor {
	return vcs.RedactorFunc(func(s string) string {
		return seamUserinfo.ReplaceAllString(s, "${1}REDACTED@")
	})
}

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

// recorderCommand installs a stand-in for the agent-facing command surface the
// hooks invoke, in a directory of its own, and returns the path the hooks
// invoke it by and the log it appends a line per invocation to.
func recorderCommand(t *testing.T, status int) (command, log string) {
	t.Helper()
	return stubCommand(t, t.TempDir(), stubName, status)
}

// stubCommand installs a stand-in command named name in dir and returns the
// path it is invoked by and the log it appends to. See stub_test.go for what
// the stand-in is and why it is a copy of this test binary rather than a
// script.
//
// The name is given without an extension: what this host needs to treat a
// path as executable is added here, so that a test naming a decoy and the
// command it must not displace cannot give them names that differ.
func stubCommand(t *testing.T, dir, name string, status int) (command, log string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test executable: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	command = filepath.Join(dir, name+exeSuffix())
	copyExecutable(t, self, command)
	if err := os.WriteFile(filepath.Join(dir, stubStatusName), []byte(strconv.Itoa(status)), 0o600); err != nil {
		t.Fatalf("write the stand-in exit status: %v", err)
	}
	t.Setenv(stubMode, "1")
	return command, filepath.Join(dir, stubLogName)
}

// copyExecutable copies a file and makes the copy executable. It is a copy
// rather than a link so that the stand-in, which finds its log and its exit
// status beside itself, cannot be told it is somewhere it is not.
func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	source, err := os.Open(from)
	if err != nil {
		t.Fatalf("open %s: %v", from, err)
	}
	defer source.Close()
	destination, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatalf("create %s: %v", to, err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatalf("copy %s to %s: %v", from, to, err)
	}
	if err := destination.Close(); err != nil {
		t.Fatalf("close %s: %v", to, err)
	}
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

// recordName is the file inside a gate repository that names the working copy
// it belongs to. It is spelled here rather than taken from the package under
// test so that a test that damages the record and one that reads it cannot both
// be wrong about which file that is.
const recordName = "assistant-gate.json"

// removeRecord deletes a gate's record, which is the damage that leaves nothing
// inside the gate saying whose it is.
func removeRecord(t *testing.T, repo string) {
	t.Helper()
	if err := os.Remove(filepath.Join(repo, recordName)); err != nil {
		t.Fatalf("remove the record of %s: %v", repo, err)
	}
}

// copyTree copies a directory the way a person copying a project would.
func copyTree(t *testing.T, from, to string) {
	t.Helper()
	if err := os.CopyFS(to, os.DirFS(from)); err != nil {
		t.Fatalf("copy %s to %s: %v", from, to, err)
	}
}
