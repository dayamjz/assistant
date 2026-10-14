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
	return currentViewer().blocks(refused{id: id, uids: uids, signal: signal}), nil
}

// Ancestry exposes the parent chain the listing sets aside.
func Ancestry() []Identity { return ancestry(os.Getpid()) }

// BlockerOf exposes blockerOf for a refused process the classification read
// as id with uids, so a test can hand it a real identity and a mutation of
// one and see what the name read records.
func BlockerOf(id Identity, uids []int) Blocker { return blockerOf(refused{id: id, uids: uids}) }

// ErrNameReplaced exposes errNameReplaced, the name read's answer for an
// identifier that names another process.
var ErrNameReplaced = errNameReplaced
