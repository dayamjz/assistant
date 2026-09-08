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
