//go:build unix

package service

import "syscall"

// processGroup returns the process group a process belongs to.
//
// The group is the relation this service can establish rather than assert:
// internal/agents starts every agent as the leader of a new group, so a
// process the agent started is in that group unless it left it. An agent may
// leave it as a matter of course and not by design: #225 observed the Claude
// Code CLI's Bash tool run a command in a group of its own. Containment reads
// the peer's own group and so does not follow such a command; the worker
// placement in placement.go walks the parent chain instead, and containment
// does not.
// A process that is gone reports an error, which refuses the request it was
// being asked about, because a peer whose process has already exited is not
// one this service can say anything about.
func processGroup(pid int) (int, error) {
	return syscall.Getpgid(pid)
}
