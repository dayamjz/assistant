package hangbuild

import (
	"fmt"
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/journey"
)

// TestMain registers the temp namespace the product build's directory uses.
// The build asks standin.TempDir for it, which refuses when nothing registered.
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

// TestHang is the child a timeout test spawns under a real testing alarm.
// Without JOURNEY_HANG_BUILD it does nothing, so a normal suite run skips it.
func TestHang(t *testing.T) {
	if os.Getenv("JOURNEY_HANG_BUILD") != "1" {
		t.Skip("JOURNEY_HANG_BUILD is unset")
	}
	if err := os.Setenv("PATH", os.Getenv("JOURNEY_HANG_PATH")); err != nil {
		t.Fatal(err)
	}
	_, err := journey.Binary()
	t.Fatal(err)
}
