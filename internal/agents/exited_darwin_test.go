package agents_test

import "testing"

// waitExitedUnreaped reports false and waits on nothing: golang.org/x/sys
// has no waitid for darwin, so no exited, unreaped child is planted here,
// and a test that needs one establishes that half on Linux only.
func waitExitedUnreaped(*testing.T, int) bool { return false }
