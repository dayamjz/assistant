//go:build windows

package agents_test

import "os"

// helperPoliteSignals are the signals the stand-in agent ignores in its
// stubborn modes, so that only the forceful step of the escalation ends it.
// A console control event arrives here as os.Interrupt.
func helperPoliteSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// helperSelfKill has no counterpart on Windows, where every process that ran
// reports a status. The mode is not used by a test here, and this exit says so
// rather than pretending the signalled path was exercised.
func helperSelfKill() {
	os.Exit(8)
}
