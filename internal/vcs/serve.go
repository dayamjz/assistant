package vcs

import (
	"context"
	"io"
	"os"
	"os/exec"

	"github.com/dayamjz/assistant/internal/procgroup"
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
// started by git and inherit that environment. It runs through procgroup.Run
// as every invocation here does, so the receive and whatever its hooks leave
// in its process group are ended when it returns, and it runs under the
// handle's invocation deadline when ctx carries none. An *os.File given as
// in, out or errOut reaches git as itself; any other reader or writer is
// copied by procgroup.Run, so one that blocks does not hold the call past the
// bound procgroup.Run states.
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

	ctx, cancel := procgroup.Bound(ctx, r.set.deadline)
	defer cancel()
	cmd := exec.Command(r.set.git, full...)
	cmd.Dir = r.path
	cmd.Env = envFor(os.Environ(), r.set.ssh)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = errOut

	runErr := procgroup.Run(ctx, cmd, killGrace)
	if runErr == nil {
		return nil
	}
	code := exitStatus(cmd)
	return r.commandError("receive-pack", full, code, "", false, invocationCause(runErr, code, ctx.Err()))
}
