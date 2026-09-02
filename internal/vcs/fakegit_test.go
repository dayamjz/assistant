package vcs_test

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The tests need to see the exact argument vector, environment, and standard
// input that reach git, because that is where the two rules this package
// exists to apply actually land. A stand-in git makes those observable through
// the ordinary exported surface: a test opens a repository whose git binary is
// this test executable running in stand-in mode, calls a normal method, and
// then reads what the stand-in recorded.
//
// The stand-in is this test binary rather than a shell script so the tests run
// the same way on every platform CI covers.

const (
	fakeGitMode   = "VCS_TEST_FAKE_GIT"
	fakeGitLog    = "VCS_TEST_FAKE_GIT_LOG"
	fakeGitStdout = "VCS_TEST_FAKE_GIT_STDOUT_FILE"
	fakeGitStderr = "VCS_TEST_FAKE_GIT_STDERR_FILE"
	fakeGitExit   = "VCS_TEST_FAKE_GIT_EXIT"
	fakeGitDieOn  = "VCS_TEST_FAKE_GIT_DIE_ON"
	fakeGitHoldOn = "VCS_TEST_FAKE_GIT_HOLD_PIPES_ON"
	fakeGitHolder = "VCS_TEST_FAKE_GIT_PIPE_HOLDER"
)

// pipeHold is how long the grandchild keeps the pipes it inherited open. It
// only has to outlast the package's own grace period by a clear margin.
const pipeHold = 10 * time.Second

// invocation is one recorded call to the stand-in git.
type invocation struct {
	Args       []string
	Env        []string
	StdinBytes int
}

// TestMain runs the stand-in git when the environment asks for it, and the
// tests otherwise.
func TestMain(m *testing.M) {
	if os.Getenv(fakeGitMode) != "" {
		os.Exit(fakeGitMain())
	}
	os.Exit(m.Run())
}

func fakeGitMain() int {
	if os.Getenv(fakeGitHolder) != "" {
		// The grandchild that inherited git's standard output and error.
		// Keeping them open while git itself exits is the whole of its job.
		time.Sleep(pipeHold)
		return 0
	}

	args := os.Args[1:]

	// Standard input is read to the end so the recorded byte count shows
	// whether anything was waiting there. A terminal would block here;
	// os.DevNull returns immediately.
	stdin, _ := io.ReadAll(os.Stdin)

	rec := invocation{Args: args, Env: os.Environ(), StdinBytes: len(stdin)}
	if path := os.Getenv(fakeGitLog); path != "" {
		line, err := json.Marshal(rec)
		if err != nil {
			return 3
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return 3
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			f.Close()
			return 3
		}
		if err := f.Close(); err != nil {
			return 3
		}
	}

	// Dying without an exit status is what a signal does to a process, and it
	// is a case this package has to describe rather than swallow. The marker
	// names the subcommand that dies, so the probes an open still needs can
	// answer normally.
	if marker := os.Getenv(fakeGitDieOn); marker != "" && slices.Contains(args, marker) {
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			_ = p.Kill()
		}
		// Kill does not take effect synchronously, so wait to be killed rather
		// than returning a status that would defeat the point.
		time.Sleep(time.Minute)
		return 3
	}

	// A git that exits cleanly while something else holds its pipes open. The
	// marker names the subcommand that does it, so the probes an open needs
	// still answer normally.
	if marker := os.Getenv(fakeGitHoldOn); marker != "" && slices.Contains(args, marker) {
		exe, err := os.Executable()
		if err != nil {
			return 3
		}
		holder := exec.Command(exe)
		holder.Env = append(os.Environ(), fakeGitHolder+"=1")
		holder.Stdout = os.Stdout
		holder.Stderr = os.Stderr
		if err := holder.Start(); err != nil {
			return 3
		}
		// Exit without waiting, leaving the pipes open behind this process.
		return 0
	}

	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "--is-bare-repository"),
		strings.Contains(joined, "--is-inside-work-tree"):
		os.Stdout.WriteString("true\n")
		return 0
	case strings.Contains(joined, "rev-parse"):
		os.Stdout.WriteString(fakeCommit + "\n")
		return 0
	}

	if path := os.Getenv(fakeGitStdout); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return 3
		}
		os.Stdout.Write(data)
	}
	if path := os.Getenv(fakeGitStderr); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return 3
		}
		os.Stderr.Write(data)
	}
	if code := os.Getenv(fakeGitExit); code != "" {
		n, err := strconv.Atoi(code)
		if err != nil {
			return 3
		}
		return n
	}
	return 0
}

// fakeCommit is the commit identifier the stand-in resolves every revision to.
const fakeCommit = "0123456789abcdef0123456789abcdef01234567"

// readInvocations returns everything the stand-in recorded to path.
func readInvocations(t *testing.T, path string) []invocation {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stand-in git log: %v", err)
	}
	var out []invocation
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec invocation
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode stand-in git record %q: %v", line, err)
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		t.Fatal("the stand-in git recorded no invocation, so nothing was checked")
	}
	return out
}

// lookupEnv finds a variable in a recorded environment.
func lookupEnv(env []string, name string) (string, bool) {
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

// fakeGitOutput makes the stand-in git write the given bytes as the standard
// output of every invocation that is not one of the probes it answers itself.
// The bytes travel through a file because an environment variable cannot carry
// the NUL separators git's own formats use.
func fakeGitOutput(t *testing.T, out string) {
	t.Helper()
	path := t.TempDir() + string(os.PathSeparator) + "stdout"
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatalf("write stand-in git output: %v", err)
	}
	t.Setenv(fakeGitStdout, path)
}

// fakeGitStderrOutput makes the stand-in git write the given bytes to standard
// error, and exit with the given status, for every invocation that is not one
// of the probes it answers itself.
func fakeGitStderrOutput(t *testing.T, text string, exit int) {
	t.Helper()
	path := t.TempDir() + string(os.PathSeparator) + "stderr"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write stand-in git standard error: %v", err)
	}
	t.Setenv(fakeGitStderr, path)
	t.Setenv(fakeGitExit, strconv.Itoa(exit))
}

// useFakeGit puts the test binary into stand-in mode for the git invocations
// that follow and returns the log path and the option that selects it.
func useFakeGit(t *testing.T) (logPath string, binary string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test executable: %v", err)
	}
	logPath = t.TempDir() + string(os.PathSeparator) + "invocations.jsonl"
	t.Setenv(fakeGitMode, "1")
	t.Setenv(fakeGitLog, logPath)
	return logPath, exe
}

// fakeBareDir returns an existing directory to stand in for a bare repository.
// The stand-in git answers the bare check itself, so the directory only has to
// exist.
func fakeBareDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir() + string(os.PathSeparator) + "gate.git"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create the stand-in repository directory: %v", err)
	}
	return dir
}
