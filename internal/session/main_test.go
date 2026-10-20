package session_test

import (
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
)

// TestMain turns this binary into the stand-in agent when it was started as
// one. The tests start tmux servers, which leave this binary's process group,
// so they run inside standin.RunOwned, which owns what they start and fails
// the package once they have returned if anything they started is still
// running.
func TestMain(m *testing.M) {
	standin.Main()
	os.Exit(standin.RunOwned(m.Run))
}
