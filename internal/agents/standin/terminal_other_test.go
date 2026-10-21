//go:build !linux && !darwin

package standin

import "testing"

// atTerminalMain has no counterpart here: AtTerminal runs nothing again on
// these platforms, so no test starts this binary as a package calling it.
func atTerminalMain() {}

// tmuxOwnerMain has no counterpart here: RunOwned starts no sweep on
// these platforms, so no test starts this binary as an owner of one.
func tmuxOwnerMain() {}

// unownedMain has no counterpart here: no test holds a process on these
// platforms, so none starts this binary to try one outside RunOwned.
func unownedMain(*testing.M) {}
