package journey_test

import (
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/journey"
)

// TestMain lets this binary act as the executables the product resolves by
// name off PATH, which is the only seam a harness in another process has into
// either of them.
//
// The order is load-bearing. internal/agents/standin answers an agent
// invocation carrying its own control flag and returns for everything else, so
// journey.ActAsShim sees only the invocations nothing scripted and refuses
// them rather than letting this binary run its own test suite as somebody's
// agent.
func TestMain(m *testing.M) {
	standin.Main()
	journey.ActAsShim()
	code := m.Run()
	// The fixture, the shims and the product binary belong to the process
	// rather than to any test, so no test can take them away and a run that
	// left them would leave tens of megabytes behind on every machine that
	// ever ran this.
	journey.Cleanup()
	os.Exit(code)
}
