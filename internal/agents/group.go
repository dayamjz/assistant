package agents

import (
	"errors"
	"fmt"
	"time"
)

// ErrGroupSurvived is EndGroup's answer when the forceful step ran and a
// process of the group was still there afterwards: one this process may not
// signal, or one not yet reaped when the grace ran out. The group was
// signalled and is not reported ended.
var ErrGroupSurvived = errors.New("agents: the process group still holds a process after the forceful step")

// GroupEnd is what EndGroup did about the group a recorded identity leads.
// It is a closed set, and no value in it is an error: a read that failed, or
// a group that survived the forceful step, is an error beside the zero
// GroupEnd, never a member.
type GroupEnd string

const (
	// GroupEnded is the leader verified present and the group then ended,
	// politely first and forcefully after the grace, with nothing of it
	// still running that this platform can see. On Linux a member that has
	// exited and waits to be reaped counts as ended; on macOS and another
	// unix it does not, and the answer is ErrGroupSurvived instead.
	GroupEnded GroupEnd = "ended"
	// GroupGone is no running process with the leader's identifier and
	// nothing still running in the group, counted as GroupEnded counts it,
	// so there was nothing to end and nothing was signalled. On Windows it
	// is never answered; GroupLeaderGone is that platform's answer.
	GroupGone GroupEnd = "gone"
	// GroupLeaderGone is no running process with the leader's identifier on
	// a platform that cannot ask what is left in the group, which is
	// Windows. A descendant that outlived the leader is not reached, and
	// nothing was signalled.
	GroupLeaderGone GroupEnd = "leader-gone"
	// GroupOutlivedLeader is no running process with the leader's
	// identifier and a process still in the group, on a platform that can
	// ask. Nothing was signalled: the identity names the leader and nothing
	// recorded names the survivor, so the survivor cannot be verified to be
	// the invocation's and is left alone.
	GroupOutlivedLeader GroupEnd = "outlived-leader"
	// GroupReplaced is a running process with the leader's identifier and
	// another start time, so the identifier was reused. Nothing was
	// signalled.
	GroupReplaced GroupEnd = "replaced"
)

// String renders the result.
func (g GroupEnd) String() string { return string(g) }

// EndGroup ends the process group led by the process id names, and only
// after verifying id still names it. It is the one way to end a group this
// package started from outside the invocation that started it, which is
// what a service recovering a home needs for a group an earlier service
// recorded and did not live to terminate.
//
// It signals by group identifier and never by pattern: nothing here reads
// a command line, a working directory, or a user. What it signals is decided
// by Identity.Presence first. A reused identifier is GroupReplaced and a
// leader that is gone is GroupGone, GroupOutlivedLeader or GroupLeaderGone,
// per platform as each says, and in all four nothing is signalled. Only
// PresencePresent is followed by the escalation runProcess applies to its
// own tree, and the result is GroupEnded when nothing of the group is left
// afterwards that this platform can see, or ErrGroupSurvived when something
// is.
//
// An error is a refusal to decide: ErrIdentityUnreadable for an identity
// with no start time or on a platform with no read, and every failure to
// read as it came. Nothing is signalled on an error.
//
// Three residual gaps remain, and none is closed here. The first is the one
// terminateTree names and the identity narrows rather than closes: the
// leader could exit and its identifier be reused between the presence read
// and the signal, and a reuse within the start time's resolution, which
// Identity names per platform, reads as present. The second is a descendant
// that moved itself to another process group: the group signal does not
// reach it, nothing recorded names it, and only Sweeper finds it, by the
// copy its working directory is in, on Linux and macOS, and when a caller
// asks it about that copy. The third is Windows: there is no process group to
// signal, so what is ended is whatever taskkill's tree option ends under the
// verified leader, nothing here claims that reaches a descendant whose
// parent already exited, and the polite step is a console control event, which reaches
// a group sharing this process's console and not one another process
// started, so at recovery the forceful step is what is delivered.
func EndGroup(id Identity, grace time.Duration) (GroupEnd, error) {
	if id.PID <= 0 {
		return "", fmt.Errorf("agents: process group %d is not one this package started", id.PID)
	}
	presence, err := id.Presence()
	if err != nil {
		return "", err
	}
	switch presence {
	case PresenceReplaced:
		return GroupReplaced, nil
	case PresenceAbsent:
		return endAbsentGroup(id.PID), nil
	}
	return endPresentGroup(id, grace)
}

// ForceGroup is EndGroup without the polite step, for a caller whose grace
// has already been spent, which is a service whose stop_grace ran out: the
// group was asked to end politely when the stop began, and asking again would
// only spend time the caller no longer has. After the same verification, the
// group is sent the forceful signal at once, and the result is GroupEnded
// when nothing of it this platform can see is left within wait, or
// ErrGroupSurvived when something is.
//
// It differs from EndGroup in one more case, and the difference rests on who
// may call it. A leader that is gone while its group still holds a process
// is GroupOutlivedLeader to EndGroup, which answers for a record an earlier
// service left and so cannot say the survivor is the invocation's. ForceGroup
// is for a group its caller registered in this process's lifetime, from an
// identity verified when the group was registered, and it ends that group
// anyway: on unix a process group's identifier is not given to another
// process while any member of the group remains, so a group signal zero
// still reaches is the group the verified leader led. That is the case a
// polite step leaves behind when the leader ends on it and a child that
// ignores it does not. The argument does not cover the moment between that
// read and the signal: a group that empties in between and whose identifier
// is given to a new group leader is signalled in its place, the same window
// terminateTree names. On Windows there is no group to ask, so a gone leader
// is GroupLeaderGone and nothing is signalled.
//
// Everything else is EndGroup's: a reused identifier and an unreadable
// identity signal nothing, and a descendant that moved to a group of its own
// is not reached.
func ForceGroup(id Identity, wait time.Duration) (GroupEnd, error) {
	if id.PID <= 0 {
		return "", fmt.Errorf("agents: process group %d is not one this package started", id.PID)
	}
	presence, err := id.Presence()
	if err != nil {
		return "", err
	}
	switch presence {
	case PresenceReplaced:
		return GroupReplaced, nil
	case PresenceAbsent:
		return forceAbsentGroup(id.PID, wait)
	}
	return forcePresentGroup(id, wait)
}
