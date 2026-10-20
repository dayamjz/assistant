//go:build windows

package agents_test

import (
	"os"
	"os/exec"
)

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

// helperDetach has no counterpart on Windows, where this package has no
// sweep to end what it would start. No test runs the mode here, and this exit
// says so rather than starting a process nothing ends.
func helperDetach() {
	os.Exit(9)
}

// processGroup has no counterpart on Windows: join leaves cmd as
// exec.CommandContext made it, so a context ending kills cmd and reaches
// nothing it started, and end does nothing. No test on Windows exercises it.
type processGroup struct{}

func startGroup() (*processGroup, error) { return &processGroup{}, nil }

func (*processGroup) join(*exec.Cmd) {}

func (*processGroup) end() {}
