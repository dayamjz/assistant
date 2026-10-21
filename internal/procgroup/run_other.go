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
// reached. Streams are copied by Run on the same terms as on the platforms
// that group, so once the program is reaped the wait on its streams is
// bounded the same way, by one grace and then one more. The errors are as on
// those platforms, except that ErrAbandoned is never returned: the wait for
// a killed program is not bounded here.
func Run(ctx context.Context, cmd *exec.Cmd, grace time.Duration) error {
	return run(ctx, cmd, grace, nil)
}

// run is Run, calling started, when it is not nil, as RunStarted says.
func run(ctx context.Context, cmd *exec.Cmd, grace time.Duration, started func(pid int)) error {
	if err := check(cmd, grace); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := plumb(cmd)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		s.abandon()
		return err
	}
	s.started()
	if started != nil {
		started(cmd.Process.Pid)
	}
	stop := context.AfterFunc(ctx, func() { _ = cmd.Process.Kill() })
	defer stop()
	waitErr := cmd.Wait()
	if copyErr := s.finish(grace); waitErr == nil {
		waitErr = copyErr
	}
	return waitErr
}
