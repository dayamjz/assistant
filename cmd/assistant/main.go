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
	"runtime/debug"

	"github.com/dayamjz/assistant/internal/cli"
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
		Getenv:     os.Getenv,
		WorkingDir: workingDir,
		Executable: executable,
		Version:    version(),
	})
	os.Exit(int(code))
}

// version is what --version reports: the build information the Go toolchain
// stamped into this binary. A binary carrying none says so rather than
// printing an empty line.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "assistant (no build information)"
	}
	revision, modified := "", ""
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			if setting.Value == "true" {
				modified = "+modified"
			}
		}
	}
	if revision == "" {
		return fmt.Sprintf("assistant %s %s", info.Main.Version, info.GoVersion)
	}
	return fmt.Sprintf("assistant %s %s%s %s", info.Main.Version, revision, modified, info.GoVersion)
}
