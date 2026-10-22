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
// directory and environment are the caller's. A stream is a file, a reader
// or writer, or nil, never a pipe from cmd's StdinPipe, StdoutPipe or
// StderrPipe, which Run refuses.
//
// When the leader exits, by itself or otherwise, the group is sent SIGKILL
// before the leader is reaped, so a descendant still in the group cannot go
// on holding the call's output pipes or running once the call returned. When
// ctx ends first, the group is sent SIGKILL at once, and once more after the
// leader exits for a descendant forked meanwhile.
//
// A stream that is neither nil nor an *os.File is copied by Run rather than
// by os/exec, through a pipe whose child end is the only thing the child
// holds, so no wait here is a wait on the caller's reader or writer. Once
// the leader is reaped, the input pipe is closed, and the output pipes are
// waited on for one grace, which a descendant outside the group holding a
// write end can use up; past it they are closed and their copiers waited on
// for one grace more, which only a copier blocked in the caller's writer can
// use up, and either expiry is exec.ErrWaitDelay.
//
// So Run returns within one grace of the leader's reap plus one grace more,
// whatever the caller's reader and writers do, and within one grace of ctx
// ending plus those two once the leader exits after SIGKILL; a leader that
// has not exited one grace after SIGKILL is ErrAbandoned, answered after one
// grace more for the copiers. What it leaves running when it returns is at
// most: a goroutine with one read of the caller's reader in flight, for as
// long as that read does not return, which then ends without reading again
// or writing what it read; an output copier with one write to the caller's
// writer in flight, which then ends without writing again; and, for
// ErrAbandoned, a goroutine that reaps the leader once it exits.
//
// For ErrAbandoned, cmd.ProcessState is nil when Run returns and stays nil
// once that goroutine has reaped the leader, which it does through
// cmd.Process and not cmd.Wait: an abandoned program has no exit status for
// the caller, and nothing Run left running writes a field of cmd. cmd.Wait
// must not be called on it.
//
// cmd.Process is nil exactly when nothing was started, and the error is then
// ErrMisconfigured when Run refused cmd, ctx's error when ctx had already
// ended, or cmd.Start's or a pipe's. Once the program started, the error is
// ErrAbandoned as above, or cmd.Wait's when it reports one, such as the
// *exec.ExitError of a failing status, and otherwise what the copying
// reported; the failure to observe the leader's exit is joined to it when
// that observation failed, in which case the group was still ended before
// the reap. Run does not put ctx's error in place of any of these: the
// caller reads ctx.Err() to tell an expiry from an answer.
func Run(ctx context.Context, cmd *exec.Cmd, grace time.Duration) error {
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		s.abandon()
		return err
	}
	s.started()
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
			// The reap goes through cmd.Process rather than cmd.Wait,
			// which would write cmd.ProcessState after Run returned and
			// race every caller reading it. check refused a context and
			// os/exec's own pipes, and every stream the child holds is a
			// file or nil, so cmd.Wait would have nothing else to finish:
			// no copier, no pipe end to close, no context.
			go func() {
				<-exited
				endGroup(pgid)
				_, _ = cmd.Process.Wait()
			}()
			_ = s.cut(grace)
			return ErrAbandoned
		}
	}
	// Every stream the child holds is a file or nil, so os/exec starts no
	// copying goroutine and this returns once the leader is reaped.
	waitErr := cmd.Wait()
	copyErr := s.finish(grace)
	if waitErr == nil {
		waitErr = copyErr
	}
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
