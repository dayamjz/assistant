// Package owner stands for internal/procgroup: the one package whose uses
// the guard counts as owned.
package owner

import "os/exec"

// Run builds and starts a command, two owned uses.
func Run() error { return exec.Command("true").Run() }
