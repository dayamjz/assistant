//go:build !unix

package stages

import "os/exec"

// inGroup leaves cmd as os/exec builds it on this platform: cancelling it,
// at its deadline or otherwise, ends the interpreter alone, a descendant is
// not ended, and the function it returns does nothing. The unix twin ends
// the process group; this platform has no spelling of that here, and
// nothing pretends otherwise.
func inGroup(*exec.Cmd) (end func()) { return func() {} }
