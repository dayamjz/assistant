//go:build !windows

package stages

import (
	"context"
	"os/exec"
)

// shellPath is the interpreter a configured command line runs through on this
// platform. It is an absolute path rather than a name looked up on PATH, so on
// this platform which interpreter runs does not depend on the environment the
// service happened to be started with.
//
// That is this file's property and not the pair's. The Windows twin names its
// interpreter from the environment, and says so; a reader should not carry the
// sentence above across to it.
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
