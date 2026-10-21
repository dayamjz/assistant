//go:build !linux && !darwin

package procgroup

import (
	"context"
	"os/exec"
	"time"
)

// Run starts cmd and waits for it, on a platform where this package starts no
// process group. When ctx ends, the program itself is killed through its
// os.Process, which refuses to signal one already reaped; no descendant is
// reached. cmd.WaitDelay is set to grace, which bounds the wait for a pipe a
// descendant still holds. The errors are as on the platforms that group,
// except that ErrAbandoned is never returned: the wait for a killed program
// is bounded by WaitDelay alone.
func Run(ctx context.Context, cmd *exec.Cmd, grace time.Duration) error {
	if err := check(cmd, grace); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd.WaitDelay = grace
	if err := cmd.Start(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = cmd.Process.Kill() })
	defer stop()
	return cmd.Wait()
}
