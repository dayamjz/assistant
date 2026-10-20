package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/session"
)

// Placement: which task's worker a peer is, or whether it is a shell at a
// terminal, read off the peer's process ancestry. workers.go says which
// rules turn on the answer; this file is the answer.
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
// group is a worker group the set holds, that is itself a worker the set
// holds, or whose identity is the host of a worker's terminal the set
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
// # The three answers, and the terminal read along the chain
//
// The walk has three answers and never a fourth. A worker of a named task,
// placedWorker, is the first process on the chain the set answers for, as
// above. A shell at a terminal, placedPerson, is a chain with no worker on
// it at all and some process on it, the peer included, in the foreground
// of its controlling terminal: the relation foreground.go asks of the peer
// alone for the local landing's approval, asked here of every process the
// walk reads. Neither, placedNobody, is a chain the walk read to its end
// with no worker and no terminal's foreground on it. An error is a fourth
// outcome only in the sense that it is not an answer: a chain the walk
// could not read, which every caller refuses on.
//
// The worker answer is taken over the whole chain and the person answer
// only once the chain is exhausted, so a worker above a terminal's
// foreground still places the peer as the worker's. That ordering is what
// keeps a worker from putting a push in a terminal's foreground, with a
// program that allocates a pseudo-terminal of its own, and having it
// placed as a person's: the chain from such a push still reaches the
// worker, and the worker wins. The one terminal a worker's chain does not
// reach past is one hosted by a process the chain does not descend from,
// below.
//
// The foreground is read along the chain and not of the peer alone because
// a person's push need not be in the foreground itself. A job a shell runs
// in the foreground is, and so is a script that job runs and every command
// of it. A job the shell runs in the background is in a group of its own
// that the terminal was not handed to, and its parent, the shell at its
// prompt, holds the terminal's foreground, so the chain places it; while
// that shell runs another foreground job the background push is placed as
// nobody's and refused, and sending it again is the cure, which the person
// is told. A host agent in a person's terminal, the coordinator's among
// them, runs its commands in a group of their own with the host in the
// terminal's foreground above them, and the same read places those as the
// person's shell. What the read establishes is a terminal's foreground
// somewhere above the peer, which internal/agents' Lineage.Foreground
// bounds: it is evidence of a terminal and not of a person.
//
// # What is read
//
// Each process is read through agents.ReadLineage, which internal/agents
// owns beside the start-time read a process Identity rests on: its parent,
// its process group, the foreground process group of its controlling
// terminal (zero for a process with none), and its start time, from
// /proc/<pid>/stat on Linux and the kern.proc.pid sysctl on macOS, one read
// per process. The walk reads the parent, the group and the terminal's
// foreground group of each; foreground.go reads the last of the peer alone.
// A walk costs one read per ancestor and ends at the first process with no
// parent, or as an error past maxAncestry. The worker view, placeByAncestry,
// runs only while the set holds a worker; the three-way view, placeAlongChain,
// runs for every push, because a chain with no worker on it still has to be
// read for a terminal. Every other platform refuses the read; internal/ipc
// identifies no peer on any of them, so the refusal is upstream of this one.
//
// # Under the tmux provider
//
// A tmux-hosted worker is not this service's child: it is the process of a
// tmux pane, a child of a tmux server that is nobody's descendant here, and
// it leads its own process group, which is what joins the worker set. A
// command the worker runs, in its group or one of its own, descends from
// the pane's process and is placed by the group as a pipe worker's is. The
// server is placed too, as the worker's host: the membership carries the
// server's identity, which internal/agents read as the pane's parent and
// checked against the server's own report, and the walk asks the set about
// each ancestor's identity beside its group. That is needed because the
// worker can reach its own server. tmux sets TMUX and TMUX_PANE in the
// pane's environment, naming the socket, so a "tmux new-window" the worker
// runs opens a shell that is the server's child and not the pane's, and a
// walk that stopped at the pane placed a push from that shell as a
// person's: #301 (c) named the risk and
// placement_tmux_internal_test.go reads it off a real server. So every
// process descended from the server is the worker's, whoever opened the
// window: the worker's own, a person's window on the same socket, and a
// run-shell the server runs. What does not descend from the server stays
// nobody's: the client a person attaches with, and a person's shell that
// types into the pane from outside, while a command the person's typing
// makes the worker run is the worker's. The host is matched by identity
// and not by number, so a process given the server's identifier after the
// server is gone is not the host. Removing the variables tmux sets was not
// taken instead: the socket is a path under the home the worker is told,
// so their absence would be a tripwire and not a boundary, and an
// environment is what a process states, as the alternatives above say.
//
// The pane's process is in the foreground of the pseudo-terminal tmux gave
// it, which internal/agents' lineage_terminal_test.go reads off a real
// pane, so a tmux worker's chain carries a terminal's foreground. The
// worker answer is taken over the whole chain, above, so that foreground
// places nothing as a person's while the worker stands in the set.
//
// # What it does not cover
//
//   - A chain the kernel has already rewritten. When a process between the
//     peer and the worker exits, the processes it leaves behind are
//     reparented to init or to a subreaper, and nothing the peer's chain
//     still records names the worker. A
//     worker's command that backgrounds a push and lets its shell exit,
//     double-forks, or daemonizes is placed as nobody's, and the walk cannot
//     tell that from a daemon's process, whose chain also ends at init with
//     no terminal on it. workers.go says which way each rule then goes: the
//     gate refuses it, so a worker's push on such a chain is refused rather
//     than admitted as a person's, which is the side an admission that
//     cannot be taken back has to err toward. A
//     subreaper this service started as each worker's parent could keep such
//     an orphan under the worker on Linux; nothing here builds one, on any
//     platform.
//   - A rewritten chain that still carries a terminal's foreground. A
//     second tmux server the worker starts on a socket of its own
//     daemonizes, so the chain from a shell in one of its windows ends at
//     init, and that shell is in the foreground of the pseudo-terminal the
//     second server gave it: the walk places it as a person's. The host
//     above is the worker's own server and no other, and a terminal's
//     foreground is not a person, which is the bound Lineage.Foreground
//     states. The same holds of any program on a rewritten chain that
//     allocates a terminal of its own.
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
//     when it was read, and a terminal's foreground is the group it held
//     when that process was read.
//
// Where the walk cannot read a process it was led to, because the process
// is gone, because the platform hides it, or because its start time says the
// identifier was reused, the answer is an error and never "nobody's" or
// "a person's": the caller refuses on it, which workers.go states per rule.

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

// placer is what the walk asks about each process it reads: the worker a
// process group leads, and the worker whose terminal a process hosts.
// *session.Workers is one, and workers.go composes one over every set the
// service holds.
type placer interface {
	Member(group int) (session.Membership, bool)
	Hosted(host agents.Identity) (session.Membership, bool)
}

// subordinatePlacer is placer's shape for the second held set,
// internal/session's Subordinates: the subordinate coordinator a process
// group leads, and the one whose terminal a process hosts. subordinates.go
// composes one over every set the service holds.
type subordinatePlacer interface {
	Member(group int) (session.Placement, bool)
	Hosted(host agents.Identity) (session.Placement, bool)
}

// placed is one of the walk's answers.
type placed int

const (
	// placedNobody is a chain read to its end with no worker, no
	// subordinate coordinator and no terminal's foreground on it.
	placedNobody placed = iota
	// placedWorker is a chain with a worker the set holds on it.
	placedWorker
	// placedSubordinate is a chain with no worker on it and a subordinate
	// coordinator the second set holds on it.
	placedSubordinate
	// placedPerson is a chain with neither on it and a process in the
	// foreground of its controlling terminal on it.
	placedPerson
)

// placement is the walk's answer and what it read to give it.
type placement struct {
	// kind is the answer.
	kind placed
	// worker is the membership, set only for placedWorker.
	worker session.Membership
	// subordinate is the placement in the second set, set only for
	// placedSubordinate.
	subordinate session.Placement
	// chain is every process the walk read, the peer first, which a refusal
	// names so that whoever is refused can see what was decided from.
	chain []agents.Lineage
}

// read renders the chain the way a refusal names it: each process with its
// group and what its terminal's foreground group was, oldest ancestor last.
func (p placement) read() string {
	parts := make([]string, 0, len(p.chain))
	for _, l := range p.chain {
		terminal := "no controlling terminal"
		if l.TerminalGroup > 0 {
			terminal = fmt.Sprintf("terminal foreground group %d", l.TerminalGroup)
		}
		parts = append(parts, fmt.Sprintf("process %d (group %d, %s)", l.PID, l.Group, terminal))
	}
	return strings.Join(parts, " <- ")
}

// placeAlongChain walks the parent chain from pid on the terms above, reading
// each process with read and asking set about each process's group, where
// it differs its own identifier, and its identity as a host, and then
// subordinates the same three questions, nil asking nothing. It answers
// placedWorker with the first membership set answers and placedSubordinate
// with the first placement subordinates answers, the worker sets asked of
// each process first so a peer both would answer for is the worker, the
// side that claims less; otherwise, once a process with no parent is
// reached, placedPerson when some process read was in the foreground of
// its controlling terminal and placedNobody when none was. An error wraps
// errAncestry and means no answer, which a caller refuses on.
func placeAlongChain(pid int, read func(int) (agents.Lineage, error), set placer, subordinates subordinatePlacer) (placement, error) {
	current, err := read(pid)
	if err != nil {
		return placement{}, fmt.Errorf("%w: reading process %d: %w", errAncestry, pid, err)
	}
	out := placement{}
	foreground := false
	for range maxAncestry {
		out.chain = append(out.chain, current)
		if m, ok := set.Member(current.Group); ok {
			out.kind, out.worker = placedWorker, m
			return out, nil
		}
		if current.PID != current.Group {
			if m, ok := set.Member(current.PID); ok {
				out.kind, out.worker = placedWorker, m
				return out, nil
			}
		}
		if m, ok := set.Hosted(current.Identity); ok {
			out.kind, out.worker = placedWorker, m
			return out, nil
		}
		if subordinates != nil {
			if p, ok := subordinates.Member(current.Group); ok {
				out.kind, out.subordinate = placedSubordinate, p
				return out, nil
			}
			if current.PID != current.Group {
				if p, ok := subordinates.Member(current.PID); ok {
					out.kind, out.subordinate = placedSubordinate, p
					return out, nil
				}
			}
			if p, ok := subordinates.Hosted(current.Identity); ok {
				out.kind, out.subordinate = placedSubordinate, p
				return out, nil
			}
		}
		foreground = foreground || current.Foreground()
		if current.Parent <= 0 || current.Parent == current.PID {
			if foreground {
				out.kind = placedPerson
			}
			return out, nil
		}
		parent, err := read(current.Parent)
		if err != nil {
			return placement{}, fmt.Errorf("%w: reading process %d, the parent of %d: %w",
				errAncestry, current.Parent, current.PID, err)
		}
		if parent.Start > current.Start {
			return placement{}, fmt.Errorf(
				"%w: process %d started after process %d, which names it as its parent, so the identifier was reused",
				errAncestry, parent.PID, current.PID)
		}
		current = parent
	}
	return placement{}, fmt.Errorf("%w: the chain from process %d is deeper than %d",
		errAncestry, pid, maxAncestry)
}

// placeByAncestry is the worker view of placeAlongChain, for the rules that
// ask only whether a peer is a task's worker: the membership and true for
// placedWorker, false for either other answer, and the walk's error as it
// stands.
func placeByAncestry(pid int, read func(int) (agents.Lineage, error), set placer) (session.Membership, bool, error) {
	p, err := placeAlongChain(pid, read, set, nil)
	if err != nil {
		return session.Membership{}, false, err
	}
	return p.worker, p.kind == placedWorker, nil
}
