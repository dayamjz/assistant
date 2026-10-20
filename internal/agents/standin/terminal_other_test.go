//go:build !linux && !darwin

package standin

// atTerminalMain has no counterpart here: AtTerminal runs nothing again on
// these platforms, so no test starts this binary as a package calling it.
func atTerminalMain() {}
