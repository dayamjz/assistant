//go:build windows

package stages

import (
	"context"
	"os"
	"os/exec"
)

// shellCommand builds the process one configured command line runs as.
//
// The line reaches the interpreter as a single argument, on the same terms as
// the other platform: a commands.* value is a line of shell per PRD section
// 10, and the interpreter is what splits it.
//
// One residual gap is quoting, and it is this platform's rather than this
// file's. os/exec quotes the argument by the rules the C runtime unquotes by,
// and cmd.exe unquotes its /c argument by rules of its own, so a command line
// carrying its own quote characters can reach the interpreter altered. An
// ordinary line of words does not, which is what a commands.* value normally
// is; nothing here detects the case that does.
//
// The other is which interpreter runs, and it is where this platform differs
// from the other. The interpreter is whatever COMSPEC names, and otherwise the
// bare name cmd.exe, which os/exec resolves through %PATH% - so on this
// platform which interpreter runs depends on the environment the service was
// started with. The unix twin pins an absolute path and has no such
// dependence, so that half of its doc does not carry across to here. COMSPEC
// is this platform's own way of naming the command interpreter, and pinning
// past it would override that convention to buy a determinism property no PRD
// section asks for; it is named here rather than defeated.
func shellCommand(ctx context.Context, command, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, interpreter(), "/c", command)
	cmd.Dir = dir
	return cmd
}

// interpreter is the command interpreter this system names, or cmd.exe when it
// names none.
func interpreter() string {
	if named := os.Getenv("COMSPEC"); named != "" {
		return named
	}
	return "cmd.exe"
}
