package tasks

import (
	"fmt"
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/forge/forgetest"
	"github.com/dayamjz/assistant/internal/store/storetest"
)

// TestMain lets this binary act as the stand-in code host, which the
// merged-pull-request proof reads through the real GitHub adapter. It also
// registers this package's temporary directory namespace, which the
// database template internal/store/storetest makes lives in, and removes
// the template once every test has run.
func TestMain(m *testing.M) {
	forgetest.Main()
	if err := standin.RegisterTempTag(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := standin.SweepTempDirs(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	storetest.Remove()
	os.Exit(code)
}
