//go:build !linux && !darwin && !windows

package session

// startTime has no read on this platform, so it answers errPlatform: Create
// records no start time and Probe answers Unobserved for any reference this
// provider does not hold.
func startTime(int) (uint64, bool, error) { return 0, false, errPlatform }
