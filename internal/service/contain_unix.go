//go:build unix

package service

import "syscall"

// processGroup returns the process group a process belongs to.
//
// The group is the relation this service can establish rather than assert:
// internal/agents starts every agent as the leader of a new group, so a
// process the agent started is in that group unless it deliberately left it.
// A process that is gone reports an error, which refuses the request it was
// being asked about, because a peer whose process has already exited is not
// one this service can say anything about.
func processGroup(pid int) (int, error) {
	return syscall.Getpgid(pid)
}
