//go:build darwin

package managed

// platformKind is launchd on macOS.
func platformKind() (Kind, bool) { return KindLaunchd, true }
