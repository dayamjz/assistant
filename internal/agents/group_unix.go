//go:build unix

package agents

import (
	"syscall"
	"time"
)

// endAbsentGroup answers for a group whose leader is gone: the group is
// asked whether anything of it is still running, which this platform can
// answer, and a group still holding a running process is left alone rather
// than signalled on the strength of a leader nobody can verify.
func endAbsentGroup(pgid int) GroupEnd {
	if groupGone(pgid) {
		return GroupGone
	}
	return GroupOutlivedLeader
}

// endPresentGroup ends the group on terminateGroup's terms, asking groupGone
// between steps, and reports whether anything was left.
func endPresentGroup(id Identity, grace time.Duration) (GroupEnd, error) {
	if !terminateGroup(id.PID, grace, groupGone, nil) {
		return "", ErrGroupSurvived
	}
	return GroupEnded, nil
}

// forcePresentGroup sends the group SIGKILL and waits up to wait for groupGone
// to answer yes, on ForceGroup's terms.
func forcePresentGroup(id Identity, wait time.Duration) (GroupEnd, error) {
	_ = syscall.Kill(-id.PID, syscall.SIGKILL)
	if !waitForEmpty(id.PID, wait, groupGone, nil) {
		return "", ErrGroupSurvived
	}
	return GroupEnded, nil
}

// groupGone reports whether nothing of the group is still running. It is
// groupEmpty with one widening, and the widening is what a group an earlier
// service started needs: that group's members were orphaned when the service
// died, so a member that exits waits to be reaped by whatever adopted it,
// and where nothing reaps orphans, which is the ordinary state of a container
// with no init, an exited member is a zombie in the group for good. Signal
// zero succeeds on a zombie, so groupEmpty would answer never.
//
// The widening is strict in every other direction. A signal-zero answer of
// no such group is gone. Any other refusal, which is a member this process
// may not signal, is not gone. And a group the signal reaches is gone only
// when exitedOnly lists at least one member and every member it lists has
// exited; a platform with no listing this package trusts, which is every
// unix but Linux, answers no, so there a zombie keeps the group from reading
// gone, which is the over-cautious direction.
func groupGone(pgid int) bool {
	switch err := syscall.Kill(-pgid, 0); err {
	case syscall.ESRCH:
		return true
	case nil:
		return exitedOnly(pgid)
	default:
		return false
	}
}
