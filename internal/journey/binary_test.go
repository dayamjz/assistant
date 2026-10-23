package journey

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTheProductBuildCompletes is the control that the life-pipe wrapper
// runs a real go build to the end. A watcher that saw a closed pipe at
// start would kill that build before it wrote the binary.
//
// The selector is resolved through Binary first, so the cache already holds
// it. The build is resolveBinary with the selector cleared: that still runs
// the wrapper, and it does not replace the selector Binary returns afterward.
func TestTheProductBuildCompletes(t *testing.T) {
	resetProductBinary()
	t.Cleanup(resetProductBinary)
	selector := "/usr/bin/false"
	t.Setenv(BinaryVariable, selector)
	selected, err := Binary()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(selector)
	if err != nil {
		t.Fatal(err)
	}
	if selected != want {
		t.Fatalf("Binary() = %s, want the selector %s", selected, want)
	}

	t.Setenv(BinaryVariable, "")
	built, err := resolveBinary()
	if err != nil {
		t.Fatal(err)
	}
	if built == selected {
		t.Fatalf("the control returned %s, the cached selector, and did not build", built)
	}
	info, err := os.Stat(built)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() || info.Size() == 0 {
		t.Fatalf("built %s is not a binary", built)
	}

	t.Setenv(BinaryVariable, selector)
	got, err := Binary()
	if err != nil {
		t.Fatal(err)
	}
	if got != selected {
		t.Fatalf("Binary() = %s, want the selector %s already resolved for this process", got, selected)
	}
}
