//go:build unix

package forge_test

import (
	"os"
	"syscall"
	"time"
)

// standInSelfKill ends the stand-in provider with a signal, so it reports no
// exit status of its own. That is what a provider the system kills looks like
// to this package. Exiting instead would make the mode prove nothing, so what
// follows the signal is a loud failure rather than a fallback.
func standInSelfKill() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	time.Sleep(2 * time.Second)
	os.Exit(fakeGHNoScript)
}
