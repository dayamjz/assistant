//go:build !linux && !darwin

package standin

// atTerminalMain has no counterpart here: AtTerminal runs nothing again on
// these platforms, so no test starts this binary as a package calling it.
func atTerminalMain() {}

// tmuxOwnerMain has no counterpart here: OwnProcesses starts no sweep on
// these platforms, so no test starts this binary as an owner of one.
func tmuxOwnerMain() {}

// withOwner returns env unchanged: nothing here carries an owner identity.
func withOwner(env []string) []string { return env }
