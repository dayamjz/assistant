//go:build windows

package cli

import "os/exec"

// detach is what this platform needs to leave the launched service running
// after the command that started it returns, which is nothing: a process
// started here is not in a process group the console signals.
func detach(*exec.Cmd) {}
