package tasks

import (
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/forge/forgetest"
)

// TestMain lets this binary act as the stand-in code host, which the
// merged-pull-request proof reads through the real GitHub adapter.
func TestMain(m *testing.M) {
	forgetest.Main()
	os.Exit(m.Run())
}
