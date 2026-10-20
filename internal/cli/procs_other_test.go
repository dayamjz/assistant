//go:build !linux && !darwin

package cli_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/home"
)

// ownProcesses, watchOwner, endServicesOf and leftAfterTests have no
// counterpart here. Every test that starts a service skips on a platform
// with no local socket peer credentials, which is every platform this file
// builds for, so nothing here starts a process of this binary that outlives
// the command that started it; the check is not run rather than run against
// a process list this file has no read for.
func ownProcesses() error { return nil }

func watchOwner() {}

func endServicesOf(*testing.T, *home.Home) {}

func leftAfterTests() bool { return true }
