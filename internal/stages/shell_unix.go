//go:build !windows

package stages

import (
	"context"
	"os/exec"
)

// shellPath is the interpreter a configured command line runs through on this
// platform. It is an absolute path rather than a name looked up on PATH, so
// which interpreter runs does not depend on the environment the service
// happened to be started with.
const shellPath = "/bin/sh"

// shellCommand builds the process one configured command line runs as.
//
// The line reaches the interpreter as a single argument, so the interpreter
// splits it and nothing here does. That is what PRD section 10 asks for: a
// commands.* value is a line of shell rather than an argument list, and a
// splitting rule invented here would be a second, quieter one.
func shellCommand(ctx context.Context, command, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, shellPath, "-c", command)
	cmd.Dir = dir
	return cmd
}

// interpreterCouldNotRun reports whether an exit status is one the interpreter
// uses for a command line it could not run, and in what sense it could not.
//
// POSIX gives a shell two statuses for that: 127 where the command name found
// nothing, and 126 where it found something that could not be executed. Both
// mean no check ran, which is a different thing to report than a check that
// ran and objected.
//
// What it reads is the status and not who set it, so a command that exits 126
// or 127 on its own account is read the same way. That direction is the safe
// one - the caller holds for a person rather than passing or prescribing a fix
// - and the caller's documentation names it as the gap it is.
func interpreterCouldNotRun(code int) (sense string, ok bool) {
	switch code {
	case 127:
		return "the command interpreter found nothing to run under that name", true
	case 126:
		return "the command interpreter found something under that name but could not execute it", true
	}
	return "", false
}
