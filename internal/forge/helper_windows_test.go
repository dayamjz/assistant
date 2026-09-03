//go:build windows

package forge_test

import "os"

// standInSelfKill has no counterpart on Windows, where every process that ran
// reports a status, so there is no answer to model in which the provider
// reports none. No test uses the mode on this platform, and this exit says so
// rather than pretending the signalled path was exercised.
func standInSelfKill() {
	os.Exit(fakeGHNoScript)
}
