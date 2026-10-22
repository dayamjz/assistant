package hangbuild

import (
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/journey"
)

// TestHang is the child a timeout test spawns under a real testing alarm.
// Without JOURNEY_HANG_BUILD it does nothing, so a normal suite run skips it.
func TestHang(t *testing.T) {
	if os.Getenv("JOURNEY_HANG_BUILD") != "1" {
		t.Skip("spawned with a hanging go on PATH")
	}
	if err := os.Setenv("PATH", os.Getenv("JOURNEY_HANG_PATH")); err != nil {
		t.Fatal(err)
	}
	_, err := journey.Binary()
	t.Fatal(err)
}
