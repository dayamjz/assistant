//go:build !linux && !darwin

package standin

import (
	"errors"
	"runtime"
)

// groupRefusal refuses, because internal/procgroup starts no process group
// on this platform, and a command run in the worker's own would be the shape
// runCommand exists not to produce.
func groupRefusal() error {
	return errors.New("standin: " + runtime.GOOS + " has no process group to run a worker's command in")
}
