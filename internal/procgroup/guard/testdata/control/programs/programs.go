// Package programs holds one of every shape of program write the write scan
// must find, beside the look-alikes and the modes it must not report.
package programs

import (
	"os"
	sys "os"
)

// WriteFile shares a mode setter's name and writes nothing.
func WriteFile(string, []byte, os.FileMode) error { return nil }

// Constant writes a program with a constant mode.
func Constant() error { return os.WriteFile("p", nil, 0o755) }

// Opened opens a program for writing under a renamed import.
func Opened() (*os.File, error) { return sys.OpenFile("p", os.O_CREATE|os.O_WRONLY, 0o700) }

// Changed sets an execute bit on a path.
func Changed() error { return os.Chmod("p", 0o711) }

// Method sets an execute bit through an open file.
func Method(f *os.File) error { return f.Chmod(0o100) }

// Parameter writes with a mode the type checker cannot read.
func Parameter(mode os.FileMode) error { return os.WriteFile("p", nil, mode) }

// Value hands out a mode setter without calling it.
func Value(f *os.File) func(os.FileMode) error { return f.Chmod }

// Data writes and sets modes with no execute bit, and calls the look-alike
// with one, none of which may be reported.
func Data(f *os.File) error {
	_ = WriteFile("p", nil, 0o755)
	_ = f.Chmod(0o600)
	return os.WriteFile("p", nil, 0o644)
}

// Excepted writes a program the control's exception list covers.
func Excepted() error { return os.Chmod("d", 0o700) }
