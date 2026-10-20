//go:build !linux && !darwin

package standin

import "testing"

// RunOwned, WatchOwner and the check RunOwned runs have no counterpart here.
// AtTerminal runs no binary again on these platforms, and every test that
// starts a lasting process of its binary skips on a platform with no local
// socket peer credentials, which is every platform this file builds for, so
// nothing here outlives the command that started it; the check is not run
// rather than run against a process list this file has no read for.
// owner_unix.go is the contract.

// RunOwned is run's status here, and records no owner, starts no owner
// sweep and runs no check.
func RunOwned(run func() int) int { return run() }

// WatchOwner does nothing here.
func WatchOwner() {}

// sweep is never reached here, because RunOwned starts no sweep, and
// refuses if a process is started with sweepFlag regardless.
func sweep(string, string) int { return 1 }

// requireOwned asks nothing here. On Windows, Tmux skips the test before it
// asks; no other platform this file builds for is a supported one, and none
// has the process reads the sweep would end a server through.
func requireOwned(testing.TB) {}

// ownedTmux is empty here, since RunOwned writes no tmux wrapper, so Tmux
// skips a terminal test on a platform this file builds for.
func ownedTmux() string { return "" }
