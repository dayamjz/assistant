// Command fixture builds the adversarial subject repository the end-to-end
// harness validates against, and writes the catalog of planted conditions
// beside it.
//
// It exists so the fixture is constructible from nothing by a script. No git
// object graph is checked into this repository, so there is none for a test to
// depend on and none that nobody can regenerate.
//
// Usage:
//
//	fixture -out DIR
//
// DIR must not exist, or must be empty. On success the manifest is at
// DIR/manifest.json and its path is printed.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/dayamjz/assistant/internal/fixture"
)

func main() {
	out := flag.String("out", "", "directory to build the fixture into; must not exist or must be empty")
	git := flag.String("git", "", "the git binary to build with; the default is git on PATH")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "fixture: -out is required")
		flag.Usage()
		os.Exit(2)
	}
	var opts []fixture.Option
	if *git != "" {
		opts = append(opts, fixture.WithGitBinary(*git))
	}
	built, err := fixture.Build(*out, opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	manifest, err := built.WriteManifest()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(manifest)
}
