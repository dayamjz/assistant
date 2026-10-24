// Package owner stands for internal/procgroup: the one package whose uses
// and writes the guard counts as owned.
package owner

import (
	"os"
	"os/exec"
)

// Run builds and starts a command, two owned uses.
func Run() error { return exec.Command("true").Run() }

// Write writes a program with a mode the type checker cannot read, an owned
// write.
func Write(mode os.FileMode) error { return os.WriteFile("p", nil, mode) }
