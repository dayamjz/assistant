//go:build !unix

package stages

import "os/exec"

// endGroupOnCancel leaves cmd as os/exec builds it on this platform:
// cancelling it ends the interpreter alone, and a descendant holding the
// output pipe is not ended. The unix twin ends the process group; this
// platform has no spelling of that here, and nothing pretends otherwise.
func endGroupOnCancel(*exec.Cmd) {}
