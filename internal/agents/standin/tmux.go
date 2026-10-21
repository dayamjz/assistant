package standin

import (
	"os"
	"runtime"
	"testing"
)

// RequireTmuxVar names the environment variable that turns a missing tmux
// from a skip into a failure. CI sets it on every platform it installs tmux
// on, so a terminal test there cannot read as passed by not having run.
const RequireTmuxVar = "ASSISTANT_REQUIRE_TMUX"

// Tmux is the tmux binary a terminal-hosted stand-in runs under: the
// wrapper RunOwned put first on this process's PATH in front of the first
// real tmux PATH named when the tests began, as an absolute path. Where
// there was none, the test is skipped with a message naming what was not
// run and why, or failed when RequireTmuxVar is set; it never returns an
// empty path, so no terminal test passes without tmux behind it. Windows has
// no tmux, and is skipped by name rather than by a failed lookup.
//
// A tmux server leaves the process group and the session of the test that
// starts it, so a kill of the test binary misses it and whatever runs in its
// panes, and both would keep running with nobody to end them. Tmux therefore
// fails the test unless it runs inside RunOwned, which starts the owner
// sweep that ends them once this binary is gone and runs the check that
// reports one still running after the tests. The wrapper runs the real tmux
// with the owner identity in its environment whatever environment it was
// given, so a server a test starts under an environment of its own, as the
// adapter does under a base environment, carries the identity the sweep and
// the check find it by, and so does what runs in its panes.
//
// What it does not cover: a tmux run by its real path, or looked up on a
// PATH a test wrote without the wrapper's directory, is not the wrapper, and
// a server it starts under an environment without the identity is seen by
// neither the sweep nor the check.
func Tmux(tb testing.TB) string {
	tb.Helper()
	if runtime.GOOS == "windows" {
		tb.Skip("terminal workers are hosted in tmux, which this platform does not run, so this terminal test was not run")
	}
	requireOwned(tb)
	if path := ownedTmux(); path != "" {
		return path
	}
	if os.Getenv(RequireTmuxVar) != "" {
		tb.Fatalf("%s is set and no tmux binary was on PATH when the tests began", RequireTmuxVar)
	}
	tb.Skipf("no tmux binary was on PATH when the tests began, so this terminal test was not run; set %s to make that a failure",
		RequireTmuxVar)
	return ""
}

// SocketDir is a short directory, owner-only, for terminal sockets a test
// starts, removed when the test ends. It is made under the system's
// temporary directory rather than the test's own, because a test's
// directory carries the test's name and a local socket path is bounded.
func SocketDir(tb testing.TB) string {
	tb.Helper()
	dir, err := os.MkdirTemp("", "tx")
	if err != nil {
		tb.Fatalf("making a directory for terminal sockets: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		tb.Fatalf("making %s owner-only: %v", dir, err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
