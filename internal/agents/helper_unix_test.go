//go:build unix

package agents_test

import (
	"os"
	"syscall"
	"time"
)

// helperPoliteSignals are the signals the stand-in agent ignores in its
// stubborn modes, so that only the forceful step of the escalation ends it.
func helperPoliteSignals() []os.Signal {
	return []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}
}

// helperSelfKill ends the stand-in agent with a signal, so it reports no exit
// status of its own. That is what an agent the system kills looks like, and
// exiting instead would make this mode prove nothing, so the exit below is a
// loud failure rather than a fallback.
func helperSelfKill() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	time.Sleep(2 * time.Second)
	os.Exit(8)
}
