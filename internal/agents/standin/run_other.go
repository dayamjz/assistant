//go:build !unix

package standin

import (
	"errors"
	"os/exec"
	"runtime"
)

// ownGroup refuses, because this platform has no process group to start a
// command in, and a command run in the worker's own would be the shape
// runCommand exists not to produce.
func ownGroup(*exec.Cmd) error {
	return errors.New("standin: " + runtime.GOOS + " has no process group to run a worker's command in")
}

// groupOf reports no group, which this platform does not have.
func groupOf(int) int { return 0 }
