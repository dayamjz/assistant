//go:build windows

package cli_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/managed"
)

// On Windows, which this build hands to no service manager, both the install
// and the removal are refused with internal/machine's row for an unsupported
// platform, as operational failures, and change nothing. This file is
// compiled for Windows by CI's cross-compiled vet and is not run there, which
// is a limit of CI and not of the test.
func TestAManagedServiceIsRefusedOnWindows(t *testing.T) {
	t.Parallel()
	h := newHome(t)
	row := machine.ManagedRefusalPhrase(managed.ReasonUnsupported).String()
	for _, args := range [][]string{{"service", "start", "--install"}, {"service", "stop", "--uninstall"}} {
		got := run(t, h, h.Root(), args...)
		if got.code != machine.ExitFailure || !strings.Contains(got.stderr, row) {
			t.Fatalf("%v exited %s:\n%s%s", args, got.code, got.stdout, got.stderr)
		}
	}
}
