// Package raw holds one of every shape of start the guard must find, and a
// look-alike it must not.
package raw

import (
	"os"
	run "os/exec"
)

// Command shares a constructor's name and starts nothing.
func Command(string) {}

type wrapped struct{ *run.Cmd }

// Direct builds and runs a command under a renamed import.
func Direct() error { return run.Command("true").Run() }

// Value hands out a starting method without calling it.
func Value() func() ([]byte, error) {
	cmd := &run.Cmd{}
	return cmd.Output
}

// Expression hands out a starting method as a method expression.
func Expression() func(*run.Cmd) error { return (*run.Cmd).Run }

// Promoted starts a command through an embedded *exec.Cmd, beside a call to
// the look-alike.
func Promoted(w wrapped) error {
	Command("x")
	return w.Start()
}

// Process starts a program through package os.
func Process() {
	_, _ = os.StartProcess("/bin/true", nil, &os.ProcAttr{})
}

// Excepted builds a command, and the control's exception list covers it.
func Excepted() *run.Cmd { return run.Command("true") }

// Method is a method, so its use is placed in "(*wrapped).Method".
func (w *wrapped) Method() error { return w.Cmd.Run() }

// Initialized names a constructor outside every function.
var Initialized = run.CommandContext
