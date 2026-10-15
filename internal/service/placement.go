package service

import (
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/session"
)

// Placement: which task's worker a peer is, read off the peer's process
// ancestry. workers.go says which rules turn on the answer; this file is the
// answer.
//
// # Why ancestry and not the peer's own process group
//
// internal/agents starts a worker as the leader of a new process group, and
// placement used to read only the peer's own group. That holds for a worker
// whose commands stay in its group, and the Claude Code CLI's need not: #225
// observed a commit hook run by the CLI's Bash tool in a process group other
// than the CLI's. A worker's git push, and so the hook that reaches this
// service as gate.admit and gate.notify, ran in a group no worker leads, and
// every worker rule placed it as a person.
//
// A process's parent is the other fact the kernel keeps about it that the
// process cannot state, and it survives a new process group: a command the
// CLI runs in a group of its own is still the CLI's descendant. So placement walks
// the parent chain from the peer upward and stops at the first process whose
// group is a worker group the set holds, or that is itself a worker the set
// holds. A process inside a worker's group is placed as before, whoever its
// parent is, because the peer is the first process the walk looks at.
//
// Two alternatives were weighed and not taken. A credential the worker's
// environment carries is something the caller states: the worker writes its
// own environment, and so does anything it starts, so it could neither be
// trusted to say "worker" nor relied on to stay put. A per-worker socket
// only the worker's environment names is the same claim made through a
// path. Both answer "who is this" from what the peer was handed rather than
// from what the kernel attributes to it, which internal/ipc refuses for
// containment for the same reason.
//
// # What is read
//
// Each process is read through agents.ReadLineage, which internal/agents
// owns beside the start-time read a process Identity rests on: its parent,
// its process group, and its start time, from /proc/<pid>/stat on Linux and
// the kern.proc.pid sysctl on macOS, one read per process. A walk costs one
// read per ancestor, it runs only while the set holds a worker, and it ends
// at the first process with no parent, or as an error past maxAncestry.
// Every other platform refuses the read; internal/ipc identifies no peer on
// any of them, so the refusal is upstream of this one.
//
// # What it does not cover
//
//   - A chain the kernel has already rewritten. When a process between the
//     peer and the worker exits, the processes it leaves behind are
//     reparented to init or to a subreaper, and nothing the peer's chain
//     still records names the worker. A
//     worker's command that backgrounds a push and lets its shell exit,
//     double-forks, or daemonizes is placed as a person, and the walk cannot
//     tell that from a person's process, whose chain also ends at init. That
//     is the one shape here that places a worker as a person rather than
//     refusing, and workers.go says which way each rule then goes. A
//     subreaper this service started as each worker's parent could keep such
//     an orphan under the worker on Linux; nothing here builds one, on any
//     platform.
//   - The peer itself. Its process identifier is the one internal/ipc read
//     when the connection was accepted, and a peer that exited and whose
//     identifier was reused before the walk is not detected, as it was not
//     for the group read before. An ancestor's reuse is: a parent started
//     after its child is not that child's parent, and the walk refuses it as
//     a chain it could not read. Start times are as fine as the platform
//     keeps them, so a reused identifier given to a process started within
//     the same tick or microsecond as the child is not caught; on macOS,
//     where the start time is the wall clock, a clock set back between a
//     parent's creation and its child's makes a real chain read as reused,
//     which refuses rather than places. agents.Lineage states both.
//   - A process outside the worker's tree that joined the worker's group is
//     placed as the worker, as the group read always did.
//   - The chain is read one process at a time, not as one snapshot. A process
//     reparented during the walk is followed through whichever parent it had
//     when it was read.
//
// Where the walk cannot read a process it was led to, because the process
// is gone, because the platform hides it, or because its start time says the
// identifier was reused, the answer is an error and never "not a worker":
// the caller refuses on it, which workers.go states per rule.

// readLineage is agents.ReadLineage with a process that is not running read
// as an error: the walk was led to it, and a process that has exited cannot
// say whose it was.
func readLineage(pid int) (agents.Lineage, error) {
	lineage, found, err := agents.ReadLineage(pid)
	if err != nil {
		return agents.Lineage{}, err
	}
	if !found {
		return agents.Lineage{}, fmt.Errorf("no running process has identifier %d", pid)
	}
	return lineage, nil
}

// maxAncestry bounds the walk, so a chain that does not end, which no
// operating system this reads produces, is an error rather than a loop.
const maxAncestry = 1024

// errAncestry is a chain the walk could not read to its end.
var errAncestry = errors.New("the process ancestry could not be read")

// placeByAncestry walks the parent chain from pid on the terms above, reading
// each process with read and asking member about each process's group and,
// where it differs, its own identifier. It returns the first membership
// member answers, or false once a process with no parent is reached; an error
// wraps errAncestry and means no answer, which a caller refuses on.
func placeByAncestry(pid int, read func(int) (agents.Lineage, error),
	member func(int) (session.Membership, bool)) (session.Membership, bool, error) {
	current, err := read(pid)
	if err != nil {
		return session.Membership{}, false, fmt.Errorf("%w: reading process %d: %w", errAncestry, pid, err)
	}
	for range maxAncestry {
		if m, ok := member(current.Group); ok {
			return m, true, nil
		}
		if current.PID != current.Group {
			if m, ok := member(current.PID); ok {
				return m, true, nil
			}
		}
		if current.Parent <= 0 || current.Parent == current.PID {
			return session.Membership{}, false, nil
		}
		parent, err := read(current.Parent)
		if err != nil {
			return session.Membership{}, false, fmt.Errorf("%w: reading process %d, the parent of %d: %w",
				errAncestry, current.Parent, current.PID, err)
		}
		if parent.Start > current.Start {
			return session.Membership{}, false, fmt.Errorf(
				"%w: process %d started after process %d, which names it as its parent, so the identifier was reused",
				errAncestry, parent.PID, current.PID)
		}
		current = parent
	}
	return session.Membership{}, false, fmt.Errorf("%w: the chain from process %d is deeper than %d",
		errAncestry, pid, maxAncestry)
}
