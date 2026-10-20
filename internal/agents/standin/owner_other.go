//go:build !linux && !darwin

package standin

// OwnProcesses, WatchOwner and LeftAfterTests have no counterpart here.
// AtTerminal runs no binary again on these platforms, and every test that
// starts a lasting process of its binary skips on a platform with no local
// socket peer credentials, which is every platform this file builds for, so
// nothing here outlives the command that started it; the check is not run
// rather than run against a process list this file has no read for.
// owner_unix.go is the contract.

// OwnProcesses does nothing here.
func OwnProcesses() error { return nil }

// WatchOwner does nothing here.
func WatchOwner() {}

// LeftAfterTests reports true here without reading anything.
func LeftAfterTests() bool { return true }
