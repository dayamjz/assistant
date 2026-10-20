//go:build !linux && !darwin

package cli_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/home"
)

// endServicesOf has no counterpart here. Every test that starts a service
// skips on a platform with no local socket peer credentials, which is every
// platform this file builds for, so nothing here starts a process of this
// binary that outlives the command that started it.
func endServicesOf(*testing.T, *home.Home) {}
