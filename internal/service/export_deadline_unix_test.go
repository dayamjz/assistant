//go:build unix

package service

import "testing"

// HangOnPathForTest is hangOnPath, for this package's external tests.
func HangOnPathForTest(t *testing.T, name, marker string) func() []int {
	return hangOnPath(t, name, marker)
}

// RequireEndedForTest is requireEnded, for this package's external tests.
func RequireEndedForTest(t *testing.T, pids []int) {
	t.Helper()
	requireEnded(t, pids)
}
