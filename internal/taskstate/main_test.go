package taskstate

import (
	"fmt"
	"os"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/store/storetest"
)

// TestMain registers this package's temporary directory namespace, which the
// database template internal/store/storetest makes lives in, reclaims what
// a killed run of this package left there, and removes the template once
// every test has run.
func TestMain(m *testing.M) {
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
