//go:build !darwin && !linux

package managed

// platformKind is no manager on every other operating system, Windows among
// them, so ForThisPlatform refuses with ReasonUnsupported there.
func platformKind() (Kind, bool) { return "", false }
