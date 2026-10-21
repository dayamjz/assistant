//go:build linux || darwin

package procgroup

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// Run starts cmd as the leader of a process group of its own and waits for
// it. cmd is built with exec.Command and not yet started; its streams,
// directory and environment are the caller's.
//
// When the leader exits, by itself or otherwise, the group is sent SIGKILL
// before the leader is reaped, so a descendant still in the group cannot go
// on holding the call's output pipes or running once the call returned. When
// ctx ends first, the group is sent SIGKILL at once, and once more after the
// leader exits for a descendant forked meanwhile. The leader is reaped by
// cmd.Wait with WaitDelay set to grace, which bounds the wait for a pipe a
// descendant outside the group still holds; past it Wait reports
// exec.ErrWaitDelay. After ctx ends Run returns within about twice grace:
// one grace for the leader to exit after SIGKILL, ErrAbandoned past it, and
// one for the pipes.
//
// cmd.Process is nil exactly when nothing was started, and the error is then
// ErrMisconfigured when Run refused cmd, ctx's error when ctx had already
// ended, or cmd.Start's. Once the program started, the error is ErrAbandoned
// as above, or what cmd.Wait reported, joined with the failure to observe
// the leader's exit when that observation failed, in which case the group was
// still ended before the reap. Run does not put ctx's error in place of what
// a started program's wait reported: the caller reads ctx.Err() to tell an
// expiry from an answer.
func Run(ctx context.Context, cmd *exec.Cmd, grace time.Duration) error {
	if err := check(cmd, grace); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = grace
	if err := cmd.Start(); err != nil {
		return err
	}
	pgid := cmd.Process.Pid
	exited := make(chan error, 1)
	go func() { exited <- AwaitExit(pgid) }()

	var observeErr error
	select {
	case observeErr = <-exited:
		// The leader has exited and is unreaped, or its exit could not be
		// observed and it is unreaped all the same, since only cmd.Wait
		// reaps it; either way the identifier still names this group.
		endGroup(pgid)
	case <-ctx.Done():
		endGroup(pgid)
		timer := time.NewTimer(grace)
		select {
		case observeErr = <-exited:
			timer.Stop()
			endGroup(pgid)
		case <-timer.C:
			go func() {
				<-exited
				endGroup(pgid)
				_ = cmd.Wait()
			}()
			return ErrAbandoned
		}
	}
	waitErr := cmd.Wait()
	if observeErr != nil {
		return errors.Join(fmt.Errorf("procgroup: observing the exit of %d: %w", pgid, observeErr), waitErr)
	}
	return waitErr
}

// endGroup sends SIGKILL to the process group pgid. Its result is not read:
// a group whose members have all exited answers ESRCH, which is the outcome
// wanted, and the caller only reaches here while the leader is unreaped.
func endGroup(pgid int) {
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
