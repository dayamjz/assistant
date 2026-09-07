//go:build unix

package service_test

import "syscall"

// processGroupOfThisProcess is the group the containment guard is registered
// against in the test that shows it firing. It is the same identifier a stage
// launcher will register for the group it starts an agent in, read the same
// way the guard reads a peer's.
func processGroupOfThisProcess() (int, error) {
	return syscall.Getpgid(syscall.Getpid())
}
