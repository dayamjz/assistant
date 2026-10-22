package ipc_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
)

// TestMain reclaims a socket directory a killed run of this package left.
func TestMain(m *testing.M) {
	if err := standin.RegisterTempTag(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := standin.SweepTempDirs(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// ipcDir is a short directory for a local socket, removed when the test ends
// and reclaimed at the next start if the test binary is killed first.
func ipcDir(t *testing.T) string {
	t.Helper()
	dir, err := standin.TempDir("ipc")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
