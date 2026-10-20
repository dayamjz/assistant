//go:build !linux && !darwin

package standin

// AtTerminal returns at once on a platform internal/ipc identifies no
// socket peer on: the gate refuses every push there before any placement,
// so no terminal would change what a test's push meets. terminal_unix.go
// is the contract.
func AtTerminal() {}
