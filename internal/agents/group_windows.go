//go:build windows

package agents

import (
	"os"
	"time"
)

// endAbsentGroup answers for a group whose leader is gone. This platform has
// no cheap way to ask what is left in the group, so the answer is the
// leader's alone: GroupLeaderGone, with nothing signalled and a descendant
// that outlived the leader not reached.
func endAbsentGroup(int) GroupEnd { return GroupLeaderGone }

// endPresentGroup ends the tree rooted at the verified leader: the console
// control event first, which reaches only a group sharing this process's
// console and so is best effort from a recovering service, then the
// forceful tree kill after grace, which is what this path delivers. The
// leader is looked for again afterwards and a leader still present is
// ErrGroupSurvived; what this platform cannot say is whether a descendant
// survived a leader that did not.
func endPresentGroup(id Identity, grace time.Duration, hurry <-chan struct{}) (GroupEnd, error) {
	_ = sendCtrlBreak(id.PID)
	if waitAbsentOr(id, grace, hurry) {
		return GroupEnded, nil
	}
	if err := runTreeKill(taskkill(id.PID), treeKillDeadline); err != nil {
		if proc, findErr := os.FindProcess(id.PID); findErr == nil {
			_ = proc.Kill()
		}
	}
	if waitAbsent(id, grace) {
		return GroupEnded, nil
	}
	return "", ErrGroupSurvived
}

// forcePresentGroup is endPresentGroup's forceful step alone, on
// ForceGroup's terms: the tree kill at once, then the wait for the leader.
func forcePresentGroup(id Identity, wait time.Duration) (GroupEnd, error) {
	if err := runTreeKill(taskkill(id.PID), treeKillDeadline); err != nil {
		if proc, findErr := os.FindProcess(id.PID); findErr == nil {
			_ = proc.Kill()
		}
	}
	if waitAbsent(id, wait) {
		return GroupEnded, nil
	}
	return "", ErrGroupSurvived
}

// waitAbsentOr is waitAbsent that gives up as soon as hurry is closed.
func waitAbsentOr(id Identity, grace time.Duration, hurry <-chan struct{}) bool {
	deadline := time.Now().Add(grace)
	for {
		presence, err := id.Presence()
		if err == nil && presence != PresencePresent {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-hurry:
			return false
		case <-time.After(pollInterval):
		}
	}
}

// waitAbsent polls until no running process has the identity, or the
// identifier has been given to another process, or the deadline passes, and
// reports whether the identity's process was gone.
func waitAbsent(id Identity, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		presence, err := id.Presence()
		if err == nil && presence != PresencePresent {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
}

// pollInterval is how often waitAbsent looks again.
const pollInterval = 10 * time.Millisecond
