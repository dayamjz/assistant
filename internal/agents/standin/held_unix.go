//go:build linux || darwin

package standin

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// A process a test has a shell hold until it is released is this binary
// started with heldFlag, never a shell loop: #432 found 23 loops waiting on
// a release file left running with parent 1 on macOS, and #440 found the
// same loop in internal/cli. A loop is /bin/sh, whose start environment
// macOS does not show, so neither the owner sweep nor the check RunOwned
// runs could see it, and nothing ended it once its test binary was killed,
// timed out, or failed before the release. The held process is started
// through env with the owner identity assigned in its environment
// (HeldWait), so its arguments are only heldFlag and the file; it ends with
// the owner that variable names (WatchOwner, which a consuming TestMain
// arms before Main), and it is a process of this binary, which the check
// lists by executable.
//
// What it does not cover: whatever the shell runs after the held process
// returns is the shell's and not this binary's, so a command released into
// runs under no owner watch of its own and ends only when it ends; and the
// shell that started the held process waits on it as /bin/sh, invisible to
// the check, until the held process exits.

// heldFlag is the argument that makes this binary the held process, followed
// by the file whose existence releases it.
const heldFlag = "--standin-held="

// heldMain makes this process, started with heldFlag FILE as its one
// argument, the held process: it exits 0 once FILE exists, and otherwise
// runs until it is killed or its owner is gone, either of which is a
// nonzero exit. Without OwnerVar it refuses at once with exit 2, since
// nothing would end it. It returns at once in every process not started
// for that.
func heldMain(args []string) {
	if len(args) != 1 {
		return
	}
	file, ok := strings.CutPrefix(args[0], heldFlag)
	if !ok {
		return
	}
	if file == "" || os.Getenv(OwnerVar) == "" {
		fmt.Fprintf(os.Stderr, "standin: a held process needs a release file and %s, so nothing would end this one with its test\n", OwnerVar)
		os.Exit(2)
	}
	for {
		if _, err := os.Stat(file); err == nil {
			os.Exit(0)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// HeldWait is the shell command that runs this binary as the held process
// until file exists, given the owner identity explicitly so that no
// environment a worker or a shell builds can drop it, and that exits
// nonzero when the held process was ended rather than released, so a shell
// that goes on with && after it goes no further. It fails tb outside
// RunOwned, where there is no owner for the process to end with. The held
// process only ends with its owner when the package's TestMain calls
// WatchOwner and then Main, which every RunOwned package's does.
func HeldWait(tb testing.TB, file string) string {
	tb.Helper()
	owner := os.Getenv(OwnerVar)
	if owner == "" {
		tb.Fatalf("%s is unset, so a held process would end with nothing; run the tests under standin.RunOwned", OwnerVar)
	}
	self, err := os.Executable()
	if err != nil {
		tb.Fatal(err)
	}
	return "env " + OwnerVar + "=" + shellWord(owner) + " " + shellWord(self) + " " + shellWord(heldFlag+file)
}

// shellWord renders s as one POSIX shell word.
func shellWord(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
