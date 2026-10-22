// Package broken does not type-check, so the guard cannot read it.
package broken

import "os/exec"

var started = exec.Command("true").Run(1)
