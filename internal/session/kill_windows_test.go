//go:build windows

package session_test

import (
	"os/exec"
	"strconv"
	"testing"

	"github.com/dayamjz/assistant/internal/session"
)

// kill ends the process the reference names the way something outside the
// provider would on this platform, with taskkill, which is also what
// internal/agents' forceful termination runs here.
func kill(t *testing.T, ref session.Reference) {
	t.Helper()
	if out, err := exec.Command("taskkill", "/F", "/PID", strconv.Itoa(ref.PID)).CombinedOutput(); err != nil {
		t.Fatalf("taskkill %d: %v\n%s", ref.PID, err, out)
	}
}
