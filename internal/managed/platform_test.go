package managed

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"
)

// The manager this build writes for is launchd on macOS and systemd on
// Linux, and on every other operating system, Windows among them, a request
// for one is refused as unsupported rather than answered with either.
func TestThisPlatformHasTheManagerItsOperatingSystemRuns(t *testing.T) {
	userHome := t.TempDir()
	getenv := func(k string) string {
		if k == "HOME" {
			return userHome
		}
		return ""
	}
	m, err := ForThisPlatform(getenv, filepath.Join(userHome, ".assistant"))
	var refusal *Refusal
	switch runtime.GOOS {
	case "darwin":
		if err != nil || m.Kind() != KindLaunchd {
			t.Fatalf("on macOS ForThisPlatform = %v, %v, want launchd", m, err)
		}
	case "linux":
		if err != nil || m.Kind() != KindSystemd {
			t.Fatalf("on Linux ForThisPlatform = %v, %v, want systemd", m, err)
		}
	default:
		if !errors.As(err, &refusal) || refusal.Reason != ReasonUnsupported {
			t.Fatalf("on %s ForThisPlatform = %v, want unsupported", runtime.GOOS, err)
		}
	}
}
