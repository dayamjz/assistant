//go:build darwin || linux

package compilehold

import (
	"fmt"
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/journey"
)

// TestMain is the journey package's owner watch around one test: WatchOwner,
// Main, and RunOwned. It does not call AtTerminal, so the test process is
// not a session leader and its exit does not hang the foreground group up.
func TestMain(m *testing.M) {
	standin.WatchOwner()
	standin.Main()
	if err := standin.RegisterTempTag(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := standin.SweepTempDirs(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(standin.RunOwned(m.Run))
}

// TestCompile runs the harness compile. Without JOURNEY_COMPILE_HOLD the
// suite skips it, so a normal run does not invoke go.
func TestCompile(t *testing.T) {
	if os.Getenv("JOURNEY_COMPILE_HOLD") != "1" {
		t.Skip("JOURNEY_COMPILE_HOLD is unset")
	}
	out, err := journey.RunHeld(os.Getenv("ROOT"), nil, "go", "test", "-c", "-o", os.Getenv("OUT"), "./internal/journey/hangbuild")
	t.Fatalf("compile returned %v\n%s", err, out)
}
