package programs

import (
	"os"
	"testing"
)

// A test file's program write is in the write scan's scope.
func TestWrite(t *testing.T) { _ = os.WriteFile("p", nil, 0o755) }
