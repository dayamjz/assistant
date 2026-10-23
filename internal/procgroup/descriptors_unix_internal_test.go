//go:build linux || darwin

package procgroup

import (
	"os"
	"testing"
)

// openDescriptors counts this process's open file descriptors. A pipe is
// opened and closed first, so a descriptor the runtime opens for its first
// pipe, such as its poller's, is open in every count rather than only in a
// later one. counted is true because this platform lists them.
func openDescriptors(t *testing.T) (n int, counted bool) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("opening a pipe: %v", err)
	}
	_ = r.Close()
	_ = w.Close()
	d, err := os.Open("/dev/fd")
	if err != nil {
		t.Fatalf("listing descriptors: %v", err)
	}
	defer func() { _ = d.Close() }()
	names, err := d.Readdirnames(-1)
	if err != nil {
		t.Fatalf("listing descriptors: %v", err)
	}
	return len(names), true
}
