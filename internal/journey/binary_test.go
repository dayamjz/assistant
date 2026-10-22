package journey

import (
	"os"
	"testing"
)

// TestTheProductBuildCompletes is the control that the life-pipe wrapper
// runs a real go build to the end. A watcher that saw a closed pipe at
// start would kill that build before it wrote the binary.
func TestTheProductBuildCompletes(t *testing.T) {
	t.Setenv(BinaryVariable, "")
	path, err := Binary()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() || info.Size() == 0 {
		t.Fatalf("built %s is not a binary", path)
	}
}
