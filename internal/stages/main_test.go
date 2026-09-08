package stages_test

import (
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
)

// The stand-in agent is this test binary re-executed, so a package whose tests
// run an agent has to answer as one. internal/agents/standin's first New in a
// process refuses loudly when this is missing, rather than letting the
// resulting agent failures read as the stage under test misbehaving.
func TestMain(m *testing.M) {
	standin.Main()
	os.Exit(m.Run())
}
