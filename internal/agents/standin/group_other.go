//go:build !unix

package standin

// groupOf reports no group, which this platform does not have.
func groupOf(int) int { return 0 }
