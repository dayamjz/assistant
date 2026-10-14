package vcs

import (
	"context"
	"io"
	"os"
	"os/exec"
)

// ServeReceivePack runs the receiving side of one push into this bare
// repository, with the pushing git on the other end of in and out.
//
// It is the one operation here whose standard streams are not this package's:
// standard input and output are the push protocol and belong to the git that
// started the push, so they are handed through untouched rather than collected,
// and standard error goes to errOut, which is where that git shows a pusher
// what the receiving side said. The two rules every invocation here follows
// still hold: the repository is named with --git-dir, and the environment is
// envFor's, so a GIT_DIR the pushing git left in the environment does not
// redirect the receive. The hooks the repository runs during the receive are
// started by git and inherit that environment.
//
// A receive git refuses comes back as a *CommandError carrying git's exit
// status. Its message is empty, because what git said went to errOut, which
// is where the person pushing reads it.
func (r *Repository) ServeReceivePack(ctx context.Context, in io.Reader, out, errOut io.Writer) error {
	if r.kind != KindBare {
		return &argumentError{what: "repository", value: r.path, reason: "must be bare to receive a push"}
	}
	full := make([]string, 0, len(globalArgs)+3)
	full = append(full, r.addressing()...)
	full = append(full, globalArgs...)
	full = append(full, "receive-pack", r.path)

	cmd := exec.CommandContext(ctx, r.set.git, full...)
	cmd.Dir = r.path
	cmd.Env = envFor(os.Environ(), r.set.ssh)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = errOut
	cmd.WaitDelay = killGrace

	runErr := cmd.Run()
	if runErr == nil {
		return nil
	}
	code := exitStatus(cmd)
	return r.commandError("receive-pack", full, code, "", false, invocationCause(runErr, code, ctx.Err()))
}
