package gate_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The hooks a gate installs invoke a command by absolute path, so what these
// tests need is a real executable on disk rather than an in-process fake: the
// property most of them establish is that git runs the command at all, and
// nothing that never leaves this process can show that.
//
// The executable is a copy of this test binary running in stand-in mode,
// which is the arrangement internal/vcs already uses for its stand-in git, so
// the tests run the same way on every platform CI covers. A shell script does
// not: Initialize checks the hook command with exec.LookPath, and on Windows
// that only accepts a path whose extension is in PATHEXT, so an extensionless
// script would fail every initialization in this package before git was ever
// reached.

const (
	// stubMode names the environment variable that puts this binary into
	// stand-in mode. It is inherited all the way down the push: the test sets
	// it, git inherits it, the hook shell inherits it, and the copy of this
	// binary the hook invokes reads it.
	stubMode = "GATE_TEST_STUB"
	// stubName is the base name every stand-in is installed under, without
	// the extension this host needs. One name for all of them is what lets a
	// test put a decoy of the same name on PATH.
	stubName = "assistant-stub"
	// stubLogName is the file, beside the stand-in, that it appends what it
	// was invoked with to.
	stubLogName = "invocations.log"
	// stubStatusName is the file, beside the stand-in, holding the exit
	// status it reports. Configuration travels beside the executable rather
	// than in the environment so that two stand-ins running under one test,
	// such as a recorder and the decoy that must not displace it, cannot read
	// each other's.
	stubStatusName = "exit-status"
)

// exeSuffix is what a path has to end in for this host to treat it as an
// executable. On Windows exec.LookPath resolves nothing else, so a stand-in
// without it would be refused as a hook command.
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// TestMain runs the stand-in command when the environment asks for it, and the
// tests otherwise.
func TestMain(m *testing.M) {
	if os.Getenv(stubMode) != "" {
		os.Exit(stubMain())
	}
	os.Exit(m.Run())
}

// stubMain is the stand-in command a gate's hooks invoke. It appends one line
// naming its arguments and one naming the standard input git handed the hook,
// and exits with the status recorded beside it.
//
// Both lines are written with a single append so that a hook chaining to
// another, and a custom hook writing to the same log, cannot interleave with
// half of a record.
func stubMain() int {
	dir, err := stubDir()
	if err != nil {
		return 3
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 3
	}
	record := "command " + strings.Join(os.Args[1:], " ") + "\n" +
		"input " + strings.ReplaceAll(string(input), "\n", ";") + "\n"

	f, err := os.OpenFile(filepath.Join(dir, stubLogName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 3
	}
	if _, err := f.Write([]byte(record)); err != nil {
		_ = f.Close()
		return 3
	}
	if err := f.Close(); err != nil {
		return 3
	}
	return stubStatus(dir)
}

// stubDir is the directory the running stand-in was installed in, which is
// where its log and its exit status live.
//
// It is read from the executable rather than from the invoking argument
// vector, because a stand-in reached through PATH is invoked by a bare name
// and would otherwise log into whatever directory git happened to be in. A
// decoy that logged somewhere nobody looks would be a check that cannot fail.
func stubDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(exe) {
		return "", errors.New("the stand-in cannot locate itself")
	}
	return filepath.Dir(exe), nil
}

// stubStatus is the exit status recorded beside the stand-in. A missing or
// unreadable status is a fixture that was not set up, so it fails rather than
// admitting the push.
func stubStatus(dir string) int {
	data, err := os.ReadFile(filepath.Join(dir, stubStatusName))
	if err != nil {
		return 3
	}
	status, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 3
	}
	return status
}
