//go:build unix && !linux && !darwin

package service

import (
	"runtime"
	"testing"
)

// matchingCommandLines fails the test: standin reads command lines from the
// kernel only on Linux and macOS. Its one caller skips first on a platform
// with no process start-time read (requiresIdentity), so reaching this means
// that changed and the control needs a read here.
func matchingCommandLines(t *testing.T, _ string) map[int]bool {
	t.Helper()
	t.Fatalf("listing processes for the positive control: no command-line read on %s", runtime.GOOS)
	return nil
}
