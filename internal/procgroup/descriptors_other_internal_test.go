//go:build !linux && !darwin

package procgroup

import "testing"

// openDescriptors counts this process's open file descriptors where the
// platform lists them. counted is false here: this file is the build that
// has no such list, and the caller skips the comparison.
func openDescriptors(t *testing.T) (n int, counted bool) {
	t.Helper()
	return 0, false
}
