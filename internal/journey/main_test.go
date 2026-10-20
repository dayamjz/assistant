package journey_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/judge"
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
//
// Every process of this binary the tests start ends with the test binary
// that started it (standin.WatchOwner), and once the tests have returned the
// package fails if any is still running (standin.LeftAfterTests).
func TestMain(m *testing.M) {
	// The decision-model key is the operator's consent to send changed lines
	// to a third party, so a test must never inherit one from whoever runs it.
	_ = os.Unsetenv(judge.KeyVariable)
	standin.WatchOwner()
	standin.Main()
	journey.ActAsShim()
	// After both, because a shim is the process it is and takes no terminal
	// of its own; a push this harness makes is a person's, and the gate
	// places one by a terminal's foreground on the pushing chain, which
	// README.md's running section states.
	standin.AtTerminal()
	if err := standin.OwnProcesses(); err != nil {
		fmt.Fprintln(os.Stderr, "recording this test binary as the owner of what its tests start:", err)
		os.Exit(1)
	}
	code := m.Run()
	// The fixture, the shims and the product binary belong to the process
	// rather than to any test, so no test can take them away and a run that
	// left them would leave tens of megabytes behind on every machine that
	// ever ran this.
	journey.Cleanup()
	if !standin.LeftAfterTests() && code == 0 {
		code = 1
	}
	os.Exit(code)
}
