//go:build !linux && !darwin && !windows

package agents

// startTime has no read on this platform, so it answers
// ErrIdentityUnreadable: Identify reads nothing and Identity.Presence
// refuses to compare.
func startTime(int) (uint64, bool, error) { return 0, false, ErrIdentityUnreadable }
