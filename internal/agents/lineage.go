package agents

import "errors"

// ErrLineageUnreadable is ReadLineage's answer on a platform none of the
// lineage files builds for: no parent or process group is read there.
var ErrLineageUnreadable = errors.New("agents: this platform has no read of a process's parent and group")

// Lineage is one process's place in the process tree as the operating
// system records it: its Identity, its parent's identifier, its process
// group, and the foreground process group of its controlling terminal. The
// process cannot state any of it, which is what makes it an input a decision
// about the process may rest on; internal/service's worker placement is one
// consumer, walking a peer's parents to a worker, and its foreground
// relation is the other, reading Foreground of the peer itself.
//
// Identity.Start is read for order here as well as for equality, and the
// order this package states is the one it can: a process is created after
// its parent, so a recorded parent whose Start reads later than the child's
// is not the process that created the child, and the identifier names some
// other process now. The converse does not hold. A reused identifier given
// to a process that started within the same tick or microsecond as the child
// reads as the parent, and on macOS, where Start is the wall clock, a clock
// set back between two creations can make a real parent read later than its
// child.
type Lineage struct {
	Identity
	// Parent is the parent's process identifier, zero for a process the
	// platform reports with none.
	Parent int
	// Group is the process group the process is in.
	Group int
	// TerminalGroup is the foreground process group of the process's
	// controlling terminal, as the kernel reports it, and zero for a process
	// with no controlling terminal. It is the group the terminal delivers
	// keyboard signals to and lets read from it, which an interactive shell
	// gives the job it runs in the foreground and takes back when that job
	// ends or is put in the background.
	TerminalGroup int
}

// Foreground reports whether the process is in the foreground process group
// of its controlling terminal: it has one, and Group is its foreground group.
// A shell gives a command it runs in the foreground exactly that; a command
// started with no controlling terminal, in a session of its own or in a
// process group the terminal was not handed to, has it not. What it is
// evidence of is bounded: it says a terminal exists and this group holds its
// foreground, and nothing about who is at the terminal, since a program that
// allocates a pseudo-terminal of its own can put any command in that
// terminal's foreground.
func (l Lineage) Foreground() bool {
	return l.TerminalGroup > 0 && l.TerminalGroup == l.Group
}

// ReadLineage reads the lineage of the process pid names now, on Identify's
// terms: found is false when no running process has the identifier, a
// process that has exited and not been reaped among them where the platform
// lets that be seen; err is every failure to read, and ErrLineageUnreadable
// where this platform reads none. Every field comes out of one read of the
// process's entry, so the group and the terminal's foreground group describe
// the process at one instant.
func ReadLineage(pid int) (Lineage, bool, error) {
	return lineage(pid)
}
