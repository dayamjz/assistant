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
// The residual gap is quoting, and it is this platform's rather than this
// file's. os/exec quotes the argument by the rules the C runtime unquotes by,
// and cmd.exe unquotes its /c argument by rules of its own, so a command line
// carrying its own quote characters can reach the interpreter altered. An
// ordinary line of words does not, which is what a commands.* value normally
// is; nothing here detects the case that does.
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

// interpreterCouldNotRun reports whether an exit status is one the interpreter
// uses for a command line it could not run, and in what sense it could not.
//
// This platform's interpreter has one such status rather than the two POSIX
// gives a shell, and it is a convention of that interpreter rather than a
// specified one. A command line that fails to start for a reason it reports
// differently is therefore read here as a command that ran and objected.
//
// What it reads is the status and not who set it, so a command that exits 9009
// on its own account is read the same way. That direction is the safe one -
// the caller holds for a person rather than passing or prescribing a fix - and
// the caller's documentation names it as the gap it is.
func interpreterCouldNotRun(code int) (sense string, ok bool) {
	if code == 9009 {
		return "the command interpreter did not recognize it as a command it could run", true
	}
	return "", false
}
