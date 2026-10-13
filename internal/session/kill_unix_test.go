//go:build unix

package session_test

import (
	"syscall"
	"testing"

	"github.com/dayamjz/assistant/internal/session"
)

// kill ends the process the reference names, the way something outside the
// provider would: a signal it cannot catch.
func kill(t *testing.T, ref session.Reference) {
	t.Helper()
	if err := syscall.Kill(ref.PID, syscall.SIGKILL); err != nil {
		t.Fatalf("killing %d: %v", ref.PID, err)
	}
}
