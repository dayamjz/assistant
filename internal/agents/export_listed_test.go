//go:build linux || darwin

package agents

import "os"

// ClassifyRefused is the decision the listing makes about pid were its
// working directory refused, from pid's real identity and user identifiers
// and this process's real viewer, with signal standing for the zero
// signal's answer.
func ClassifyRefused(pid int, signal error) (bool, error) {
	id, _, err := Identify(pid)
	if err != nil {
		return false, err
	}
	uids, _, err := procUids(pid)
	if err != nil {
		return false, err
	}
	return ClassifyUsers(id, uids, signal), nil
}

// ClassifyUsers is ClassifyRefused's decision about a process already read
// as id with uids, reading nothing, so a test can judge a snapshot it took.
func ClassifyUsers(id Identity, uids []int, signal error) bool {
	return currentViewer().blocks(refused{id: id, uids: uids, signal: signal})
}

// ProcUids exposes the user identifiers the classification reads for pid,
// and whether pid was found.
func ProcUids(pid int) ([]int, bool, error) { return procUids(pid) }

// Ancestry exposes the parent chain the listing sets aside.
func Ancestry() []Identity { return ancestry(os.Getpid()) }

// BlockerOf exposes blockerOf for a refused process the classification read
// as id with uids, so a test can hand it a real identity and a mutation of
// one and see what the name read records.
func BlockerOf(id Identity, uids []int) Blocker { return blockerOf(refused{id: id, uids: uids}) }

// ErrNameReplaced exposes errNameReplaced, the name read's answer for an
// identifier that names another process.
var ErrNameReplaced = errNameReplaced
