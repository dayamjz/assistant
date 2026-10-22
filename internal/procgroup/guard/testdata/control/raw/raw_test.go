package raw

import (
	"os/exec"
	"testing"
)

// A test file's start is out of the guard's scope.
func TestStart(t *testing.T) { _ = exec.Command("true").Run() }
