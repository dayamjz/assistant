//go:build !unix

package journey_test

import (
	"os/exec"
)

// joinGroup has no process group to join on a platform without them. The one
// caller skips through requiresIdentifiedPeer before it gets here, because
// internal/ipc reads no peer credentials on any such platform, so reaching
// this is a harness bug and it panics rather than starting cmd somewhere the
// product would not place as the worker.
func joinGroup(*exec.Cmd, int) {
	panic("journey: joining a worker's process group needs unix process groups")
}
