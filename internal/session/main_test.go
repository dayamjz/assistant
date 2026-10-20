package session_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
)

// TestMain turns this binary into the stand-in agent when it was started as
// one. The tests start tmux servers, which leave this binary's process group,
// so it owns what they start (standin.OwnProcesses) and fails once they have
// returned if anything they started is still running
// (standin.LeftAfterTests).
func TestMain(m *testing.M) {
	standin.Main()
	if err := standin.OwnProcesses(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	if !standin.LeftAfterTests() && code == 0 {
		code = 1
	}
	os.Exit(code)
}
