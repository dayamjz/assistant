package standin

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// RequireTmuxVar names the environment variable that turns a missing tmux
// from a skip into a failure. CI sets it on every platform it installs tmux
// on, so a terminal test there cannot read as passed by not having run.
const RequireTmuxVar = "ASSISTANT_REQUIRE_TMUX"

// Tmux is the tmux binary a terminal-hosted stand-in runs under, resolved
// off PATH. Where there is none, the test is skipped with a message naming
// what was not run and why, or failed when RequireTmuxVar is set; it never
// returns an empty path, so no terminal test passes without tmux behind it.
// Windows has no tmux, and is skipped by name rather than by a failed
// lookup.
func Tmux(tb testing.TB) string {
	tb.Helper()
	if runtime.GOOS == "windows" {
		tb.Skip("terminal workers are hosted in tmux, which this platform does not run, so this terminal test was not run")
	}
	path, err := exec.LookPath("tmux")
	if err == nil {
		if abs, aerr := filepath.Abs(path); aerr == nil {
			return abs
		}
		return path
	}
	if os.Getenv(RequireTmuxVar) != "" {
		tb.Fatalf("%s is set and no tmux binary is on PATH: %v", RequireTmuxVar, err)
	}
	tb.Skipf("no tmux binary is on PATH (%v), so this terminal test was not run; set %s to make that a failure",
		err, RequireTmuxVar)
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
