// Command assistant is the delivery gate and the fleet, driven from one
// command surface.
//
// The verbs, their flags, and the two renderings of an answer are
// internal/cli's; the background service they talk to is internal/service's.
// This file is the process boundary and nothing else: it reads what the
// command surface needs from the process it is running in, hands it over, and
// exits with the code that came back.
//
// Keeping it that thin is deliberate. PRD section 9 makes a person at a
// terminal and an agent driving programmatically first-class users of the same
// commands, and every decision about what a command does belongs in a package
// a test can drive with its own streams and its own directory rather than in a
// main that only a subprocess can reach.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/buildinfo"
	"github.com/dayamjz/assistant/internal/cli"
	"github.com/mattn/go-isatty"
)

func main() {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "assistant: cannot resolve this binary's own path:", err)
		os.Exit(1)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "assistant: cannot resolve the working directory:", err)
		os.Exit(1)
	}
	code := cli.Run(context.Background(), cli.Environment{
		Args:       os.Args[1:],
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Stdin:      os.Stdin,
		Getenv:     os.Getenv,
		WorkingDir: workingDir,
		Executable: executable,
		Version:    buildinfo.Line(),
		// A person is at a terminal when both the stream init reads an
		// answer from and the one it asks on are one.
		Interactive: terminal(os.Stdin) && terminal(os.Stderr),
		OffTerminal: cli.OffTerminal(os.Getpid()),
	})
	os.Exit(int(code))
}

// terminal reports whether f is a terminal, including the one a Cygwin or
// MSYS shell gives a process on Windows.
func terminal(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
