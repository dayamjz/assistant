//go:build !linux && !darwin

package journey

// endTree is end here: standin lists processes and their parents only on
// Linux and macOS, so a process the service started is not reached.
func (s *serving) endTree() error { return s.end() }
